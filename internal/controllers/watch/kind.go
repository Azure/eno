package watch

import (
	"context"
	"fmt"
	"math/rand"
	"path"
	"reflect"
	"slices"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/Azure/eno/internal/resource"
	"github.com/go-logr/logr"
	"golang.org/x/time/rate"
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

var rateLimiter *rate.Limiter

// SetKindWatchRateLimit configures the shared rate limiter for KindWatchControllers.
func SetKindWatchRateLimit(rps float64, burst int) {
	rateLimiter = rate.NewLimiter(rate.Limit(rps), burst)
}

type KindWatchController struct {
	client client.Client
	gvk    schema.GroupVersionKind
	cancel context.CancelFunc
}

func NewKindWatchController(ctx context.Context, parent *WatchController, resource *apiv1.ResourceRef) (*KindWatchController, error) {
	logger := logr.FromContextOrDiscard(ctx).WithValues("group", resource.Group, "version", resource.Version, "kind", resource.Kind)

	k := &KindWatchController{
		client: parent.mgr.GetClient(),
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
		logger.V(1).Info("starting kind watch controller")
		rrc.Start(ctx)
		logger.V(1).Info("kind watch controller stopped")
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
			if comp.Spec.Synthesizer.Name == "" {
				return nil
			}

			synth := &apiv1.Synthesizer{}
			err = parent.client.Get(ctx, types.NamespacedName{Name: comp.Spec.Synthesizer.Name}, synth)
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
			compList := &apiv1.CompositionList{}
			err = parent.client.List(ctx, compList, client.MatchingFields{
				manager.IdxCompositionsBySynthesizer: synth.Name,
			})
			if err != nil {
				logr.FromContextOrDiscard(ctx).Error(err, "unable to get compositions for synthesizer")
				return nil
			}

			return k.buildRequests(synth, compList.Items...)
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
	logger.V(1).Info("stopping kind watch controller")
}

func (k *KindWatchController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx).WithValues("group", k.gvk.Group, "version", k.gvk.Version, "kind", k.gvk.Kind)

	meta := &metav1.PartialObjectMetadata{}
	meta.SetGroupVersionKind(k.gvk)
	err := k.client.Get(ctx, req.NamespacedName, meta)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	list := &apiv1.SynthesizerList{}
	err = k.client.List(ctx, list, client.MatchingFields{
		manager.IdxSynthesizersByRef: path.Join(k.gvk.Group, k.gvk.Version, k.gvk.Kind),
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing synthesizers: %w", err)
	}
	rand.Shuffle(len(list.Items), func(i, j int) { list.Items[i], list.Items[j] = list.Items[j], list.Items[i] })

	for _, synth := range list.Items {
		for _, ref := range synth.Spec.Refs {
			if ref.Resource.Name != meta.GetName() || ref.Resource.Namespace != meta.GetNamespace() || ref.Resource.Group != k.gvk.Group || ref.Resource.Kind != k.gvk.Kind || ref.Resource.Version != k.gvk.Version {
				continue
			}

			list := &apiv1.CompositionList{}
			err = k.client.List(ctx, list, client.MatchingFields{
				manager.IdxCompositionsBySynthesizer: synth.Name,
			})
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("listing compositions: %w", err)
			}
			modified, err := k.updateCompositions(ctx, logger, &synth, meta, list)
			if modified || err != nil {
				return ctrl.Result{}, err
			}
		}

		list := &apiv1.CompositionList{}
		err = k.client.List(ctx, list, client.MatchingFields{
			manager.IdxCompositionsByBinding: path.Join(synth.Name, meta.Namespace, meta.Name),
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("listing compositions: %w", err)
		}
		modified, err := k.updateCompositions(ctx, logger, &synth, meta, list)
		if modified || err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (k *KindWatchController) updateCompositions(ctx context.Context, logger logr.Logger, synth *apiv1.Synthesizer, meta *metav1.PartialObjectMetadata, list *apiv1.CompositionList) (bool, error) {
	for _, comp := range list.Items {
		key := findRefKey(&comp, synth, meta)
		if key == "" {
			logger.V(1).Info("no matching input key found for resource")
			continue
		}

		revs := resource.NewInputRevisions(meta, key)
		if !setInputRevisions(&comp, revs) {
			continue
		}

		// TODO: Reduce risk of conflict errors here
		err := k.client.Status().Update(ctx, &comp)
		if err != nil {
			return false, fmt.Errorf("updating input revisions: %w", err)
		}
		logger.V(0).Info("noticed input resource change", "compositionName", comp.Name, "compositionNamespace", comp.Namespace, "ref", key)
		return true, nil // wait for requeue
	}

	return false, nil
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

func setInputRevisions(comp *apiv1.Composition, revs *apiv1.InputRevisions) bool {
	for i, ir := range comp.Status.InputRevisions {
		if ir.Key != revs.Key {
			continue
		}
		if reflect.DeepEqual(ir, *revs) {
			return false
		}
		comp.Status.InputRevisions[i] = *revs
		return true
	}
	comp.Status.InputRevisions = append(comp.Status.InputRevisions, *revs)
	return true
}
