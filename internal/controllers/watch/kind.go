package watch

import (
	"context"
	"fmt"
	"math/rand"
	"path"
	"slices"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/flowcontrol"
	"github.com/Azure/eno/internal/manager"
	"github.com/Azure/eno/internal/synthref"
	"github.com/go-logr/logr"
	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var rateLimiter = rate.NewLimiter(rate.Every(time.Second), 10)

// SetKindWatchRateLimit configures the shared rate limiter for KindWatchControllers.
func SetKindWatchRateLimit(rps int) {
	rateLimiter = rate.NewLimiter(rate.Every(time.Second), rps)
}

type KindWatchController struct {
	client client.Client
	buffer *flowcontrol.CompositionInputRevisionWriteBuffer
	gvk    schema.GroupVersionKind
	cancel context.CancelFunc
}

func NewKindWatchController(ctx context.Context, parent *WatchController, resource *apiv1.ResourceRef) (*KindWatchController, error) {
	logger := logr.FromContextOrDiscard(ctx).WithValues("group", resource.Group, "version", resource.Version, "kind", resource.Kind)

	k := &KindWatchController{
		client: parent.mgr.GetClient(),
		buffer: parent.buffer,
		gvk: schema.GroupVersionKind{
			Group:   resource.Group,
			Version: resource.Version,
			Kind:    resource.Kind,
		},
	}

	ref := &metav1.PartialObjectMetadata{}
	ref.SetGroupVersionKind(k.gvk)

	rrc, err := k.newResourceWatchController(parent, ref)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	k.cancel = cancel

	go func() {
		logger.Info("starting kind watch controller")
		rrc.Start(ctx)
		logger.Info("kind watch controller stopped")
	}()

	return k, nil
}

func (k *KindWatchController) newResourceWatchController(parent *WatchController, ref *metav1.PartialObjectMetadata) (controller.Controller, error) {
	controllerName := fmt.Sprintf("kindWatchController-%s-%s-%s-%s", ref.APIVersion, ref.Kind, ref.GetNamespace(), ref.GetName())
	skipNameValidation := true
	rrc, err := controller.NewUnmanaged(controllerName, controller.Options{
		LogConstructor:     manager.NewLogConstructor(parent.mgr, controllerName),
		SkipNameValidation: &skipNameValidation, // Allow duplicate names since we create many dynamic controllers
		RateLimiter: &workqueue.TypedBucketRateLimiter[reconcile.Request]{
			Limiter: rateLimiter,
		},
		Reconciler: k,
	})
	if err != nil {
		return nil, err
	}

	// Watch the input resources
	err = rrc.Watch(source.Kind(parent.mgr.GetCache(), ref, &handler.TypedEnqueueRequestForObject[*metav1.PartialObjectMetadata]{}))
	if err != nil {
		return nil, err
	}

	// Watch inputs declared by refs/bindings in synthesizers/compositions
	err = rrc.Watch(source.Kind(parent.mgr.GetCache(), &apiv1.Composition{},
		handler.TypedEnqueueRequestsFromMapFunc(handler.TypedMapFunc[*apiv1.Composition, reconcile.Request](func(ctx context.Context, comp *apiv1.Composition) []reconcile.Request {
			if comp.Spec.Synthesizer.Name == "" && comp.Spec.Synthesizer.LabelSelector == nil {
				return nil
			}

			synth, err := synthref.Resolve(ctx, parent.client, &comp.Spec.Synthesizer)
			if err != nil {
				logr.FromContextOrDiscard(ctx).Error(err, "unable to get synthesizer for composition")
				return nil
			}

			return k.buildRequests(synth, *comp)
		}))))
	if err != nil {
		return nil, err
	}
	err = rrc.Watch(source.Kind(parent.mgr.GetCache(), &apiv1.Synthesizer{},
		handler.TypedEnqueueRequestsFromMapFunc(handler.TypedMapFunc[*apiv1.Synthesizer, reconcile.Request](func(ctx context.Context, synth *apiv1.Synthesizer) []reconcile.Request {
			comps, err := synthref.ListCompositions(ctx, parent.client, synth, manager.IdxCompositionsBySynthesizer, synthref.Identity)
			if err != nil {
				logr.FromContextOrDiscard(ctx).Error(err, "unable to get compositions for synthesizer")
				return nil
			}

			return k.buildRequests(synth, comps...)
		}))))
	if err != nil {
		return nil, err
	}

	return rrc, err
}

// buildRequests returns a reconcile request for every binding to this resource kind.
func (k *KindWatchController) buildRequests(synth *apiv1.Synthesizer, comps ...apiv1.Composition) []reconcile.Request {
	keys := map[string]struct{}{}
	reqs := []reconcile.Request{}
	for _, ref := range synth.Spec.Refs {
		if ref.Resource.Name == "" {
			keys[ref.Key] = struct{}{}
			continue // ref does not have an "implicit" binding
		}

		nsn := types.NamespacedName{Namespace: ref.Resource.Namespace, Name: ref.Resource.Name}
		req := reconcile.Request{NamespacedName: nsn}
		if !slices.Contains(reqs, req) {
			reqs = append(reqs, req)
		}
	}

	for _, comp := range comps {
		for _, binding := range comp.Spec.Bindings {
			if _, found := keys[binding.Key]; !found {
				continue
			}

			nsn := types.NamespacedName{Namespace: binding.Resource.Namespace, Name: binding.Resource.Name}
			req := reconcile.Request{NamespacedName: nsn}
			if !slices.Contains(reqs, req) {
				reqs = append(reqs, req)
			}
		}
	}

	return reqs
}

func (k *KindWatchController) Stop(ctx context.Context) {
	logger := logr.FromContextOrDiscard(ctx)
	if k.cancel != nil {
		k.cancel()
	}
	logger.Info("stopping kind watch controller")
}

func (k *KindWatchController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx).WithValues("group", k.gvk.Group, "version", k.gvk.Version, "kind", k.gvk.Kind)

	meta := &metav1.PartialObjectMetadata{}
	meta.SetGroupVersionKind(k.gvk)
	err := k.client.Get(ctx, req.NamespacedName, meta)
	isDeleted := errors.IsNotFound(err)
	if err != nil && !isDeleted {
		logger.Error(err, "failed to get watched resource")
		return ctrl.Result{}, err
	}

	// If deleted, we still need to process it to remove from compositions
	if isDeleted {
		meta.SetName(req.Name)
		meta.SetNamespace(req.Namespace)
	}
	logger = logger.WithValues("deleted", isDeleted, "resourceVersion", meta.GetResourceVersion())

	list := &apiv1.SynthesizerList{}
	err = k.client.List(ctx, list, client.MatchingFields{
		manager.IdxSynthesizersByRef: path.Join(k.gvk.Group, k.gvk.Version, k.gvk.Kind),
	})
	if err != nil {
		logger.Error(err, "failed to list synthesizers for resource kind")
		return ctrl.Result{}, fmt.Errorf("listing synthesizers: %w", err)
	}
	rand.Shuffle(len(list.Items), func(i, j int) { list.Items[i], list.Items[j] = list.Items[j], list.Items[i] })

	inputChanges := 0
	for _, synth := range list.Items {
		for _, ref := range synth.Spec.Refs {
			if ref.Resource.Name != meta.GetName() || ref.Resource.Namespace != meta.GetNamespace() || ref.Resource.Group != k.gvk.Group || ref.Resource.Kind != k.gvk.Kind || ref.Resource.Version != k.gvk.Version {
				continue
			}

			comps, err := synthref.ListCompositions(ctx, k.client, &synth, manager.IdxCompositionsBySynthesizer, synthref.Identity)
			if err != nil {
				logger.Error(err, "failed to list compositions for synthesizer", "synthesizerName", synth.Name)
				return ctrl.Result{}, fmt.Errorf("listing compositions: %w", err)
			}
			inputChanges += k.updateCompositions(logger, &synth, meta, comps, isDeleted)
		}

		comps, err := synthref.ListCompositions(ctx, k.client, &synth, manager.IdxCompositionsByBinding, func(synthKey string) string {
			return path.Join(synthKey, meta.Namespace, meta.Name)
		})
		if err != nil {
			logger.Error(err, "failed to list compositions by binding", "synthesizerName", synth.Name)
			return ctrl.Result{}, fmt.Errorf("listing compositions: %w", err)
		}
		inputChanges += k.updateCompositions(logger, &synth, meta, comps, isDeleted)
	}

	// Most runs re-process inputs the compositions already have, so only log when an input actually changed.
	if inputChanges > 0 {
		logger.Info("finished reconciling watched resource", "inputChanges", inputChanges)
	}
	return ctrl.Result{}, nil
}

// updateCompositions enqueues an input-revision change for every composition bound to the
// watched resource. Writes are coalesced per composition by the write buffer, so multiple
// inputs changing for the same composition in a short window collapse into a single
// Status().Update instead of one write per input. It returns how many compositions don't already have this input revision recorded.
func (k *KindWatchController) updateCompositions(logger logr.Logger, synth *apiv1.Synthesizer, meta *metav1.PartialObjectMetadata, comps []apiv1.Composition, isDeleted bool) int {
	changes := 0
	for i := range comps {
		comp := comps[i]
		key := findRefKey(&comp, synth, meta)
		if key == "" {
			continue
		}

		compKey := client.ObjectKeyFromObject(&comp)

		if isDeleted {
			// Only remove InputRevisions for optional refs.
			// Required refs should trigger MissingInputs status instead - the
			// composition controller handles that case.
			if isOptionalRef(synth, key) {
				k.buffer.RemoveInputRevisionAsync(compKey, key)
				if flowcontrol.HasInputRevision(&comp, key) {
					changes++
					logger.V(1).Info("buffered input revision removal", "compositionName", comp.Name, "compositionNamespace", comp.Namespace, "ref", key)
				}
			}
			continue
		}

		revs := apiv1.NewInputRevisions(meta, key)
		k.buffer.PatchInputRevisionAsync(compKey, revs)
		if flowcontrol.InputRevisionChanged(&comp, revs) {
			changes++
			logger.V(1).Info("buffered input resource change", "compositionName", comp.Name, "compositionNamespace", comp.Namespace, "ref", key)
		}
	}
	return changes
}

func findRefKey(comp *apiv1.Composition, synth *apiv1.Synthesizer, meta *metav1.PartialObjectMetadata) string {
	var bindingKey string
	for _, binding := range comp.Spec.Bindings {
		if binding.Resource.Name == meta.GetName() && binding.Resource.Namespace == meta.GetNamespace() {
			bindingKey = binding.Key
			break
		}
	}

	for _, ref := range synth.Spec.Refs {
		gvk := meta.GetObjectKind().GroupVersionKind()
		matchesGVK := ref.Resource.Group == gvk.Group && ref.Resource.Version == gvk.Version && ref.Resource.Kind == gvk.Kind
		matchesKey := bindingKey == ref.Key
		matchesNSN := ref.Resource.Name == meta.GetName() && ref.Resource.Namespace == meta.GetNamespace()
		if matchesGVK && (matchesKey || matchesNSN) {
			return ref.Key
		}
	}

	return ""
}

func isOptionalRef(synth *apiv1.Synthesizer, key string) bool {
	for _, ref := range synth.Spec.Refs {
		if ref.Key == key {
			return ref.Optional
		}
	}
	return false
}
