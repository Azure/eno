package watch

import (
	"context"
	"fmt"
	"math/rand"
	"path"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/Azure/eno/internal/resource"
	"github.com/go-logr/logr"
	"golang.org/x/time/rate"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

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
	rrc, err := controller.NewUnmanaged("kindWatchController", parent.mgr, controller.Options{
		LogConstructor: manager.NewLogConstructor(parent.mgr, "kindWatchController"),
		RateLimiter: &workqueue.BucketRateLimiter{
			// Be careful about feedback loops - low, hardcoded rate limits make sense here.
			// Maybe expose as a flag in the future.
			Limiter: rate.NewLimiter(rate.Every(time.Second), 2),
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
		handler.TypedEnqueueRequestsFromMapFunc(handler.TypedMapFunc[*apiv1.Composition](func(ctx context.Context, comp *apiv1.Composition) []reconcile.Request {
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
		handler.TypedEnqueueRequestsFromMapFunc(handler.TypedMapFunc[*apiv1.Synthesizer](func(ctx context.Context, synth *apiv1.Synthesizer) []reconcile.Request {
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
	for _, ref := range synth.Spec.Refs {
		keys[ref.Key] = struct{}{}
	}

	reqs := []reconcile.Request{}
	for _, comp := range comps {
		for _, binding := range comp.Spec.Bindings {
			if _, found := keys[binding.Key]; !found {
				continue
			}

			nsn := types.NamespacedName{Namespace: binding.Resource.Namespace, Name: binding.Resource.Name}
			var exists bool
			for _, req := range reqs {
				if req.NamespacedName == nsn {
					exists = true
					break
				}
			}
			if !exists {
				reqs = append(reqs, reconcile.Request{NamespacedName: nsn})
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
	logger := logr.FromContextOrDiscard(ctx)

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
		list := &apiv1.CompositionList{}
		err = k.client.List(ctx, list, client.MatchingFields{
			manager.IdxCompositionsByBinding: path.Join(synth.Name, meta.Namespace, meta.Name),
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("listing compositions: %w", err)
		}

		for _, comp := range list.Items {
			if comp.ShouldIgnoreSideEffects() {
				continue
			}

			key, deferred := findRefKey(&comp, &synth, meta)
			if key == "" {
				logger.V(1).Info("no matching input key found for resource")
				continue
			}

			revs := resource.NewInputRevisions(meta, key)
			if !setInputRevisions(&comp, revs) {
				continue
			}

			if deferred && comp.Status.PendingResynthesis == nil {
				comp.Status.PendingResynthesis = ptr.To(metav1.Now())
			}

			// TODO: Reduce risk of conflict errors here
			err = k.client.Status().Update(ctx, &comp)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("updating input revisions: %w", err)
			}
			logger.V(0).Info("noticed input resource change", "compositionName", comp.Name, "compositionNamespace", comp.Namespace, "ref", key, "deferred", deferred)
			return ctrl.Result{}, nil // wait for requeue
		}
	}

	return ctrl.Result{}, nil
}

func findRefKey(comp *apiv1.Composition, synth *apiv1.Synthesizer, meta *metav1.PartialObjectMetadata) (string, bool) {
	var bindingKey string
	for _, binding := range comp.Spec.Bindings {
		if binding.Resource.Name == meta.GetName() && binding.Resource.Namespace == meta.GetNamespace() {
			bindingKey = binding.Key
			break
		}
	}

	for _, ref := range synth.Spec.Refs {
		gvk := meta.GetObjectKind().GroupVersionKind()
		if bindingKey == ref.Key && ref.Resource.Group == gvk.Group && ref.Resource.Version == gvk.Version && ref.Resource.Kind == gvk.Kind {
			return ref.Key, ref.Defer
		}
	}

	return "", false
}

func setInputRevisions(comp *apiv1.Composition, revs *apiv1.InputRevisions) bool {
	for i, ir := range comp.Status.InputRevisions {
		if ir.Key != revs.Key {
			continue
		}
		if ir == *revs {
			return false // TODO: Unit test for idempotence
		}
		comp.Status.InputRevisions[i] = *revs
		return true
	}
	comp.Status.InputRevisions = append(comp.Status.InputRevisions, *revs)
	return true
}
