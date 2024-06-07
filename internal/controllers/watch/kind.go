package watch

import (
	"context"
	"fmt"
	"math/rand"
	"path"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/rollout"
	"github.com/Azure/eno/internal/manager"
	"github.com/Azure/eno/internal/resource"
	"github.com/go-logr/logr"
	"golang.org/x/time/rate"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
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

	err = rrc.Watch(source.Kind(parent.mgr.GetCache(), ref), &handler.EnqueueRequestForObject{})
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
	rand.Shuffle(len(list.Items), func(i, j int) { list.Items[i] = list.Items[j] })

	for _, synth := range list.Items {
		list := &apiv1.CompositionList{}
		err = k.client.List(ctx, list, client.MatchingFields{
			manager.IdxCompositionsByBinding: path.Join(synth.Name, meta.Namespace, meta.Name),
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("listing compositions: %w", err)
		}
		rand.Shuffle(len(list.Items), func(i, j int) { list.Items[i] = list.Items[j] })

		for _, comp := range list.Items {
			key := findRefKey(&comp, &synth, meta)
			if key == "" {
				logger.V(1).Info("no matching input key found for resource")
				continue
			}

			revs := resource.NewInputRevisions(meta, key)
			if !shouldCauseResynthesis(&comp, revs) {
				continue
			}

			// Input has changed - resynthesize!
			rollout.SwapStates(&comp)
			err = k.client.Status().Update(ctx, &comp)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("swapping composition state: %w", err)
			}
			logger.V(0).Info("started resynthesis because input changed", "compositionName", comp.Name, "compositionNamespace", comp.Namespace, "ref", revs.Key)
		}
	}

	return ctrl.Result{}, nil
}

func shouldCauseResynthesis(comp *apiv1.Composition, revs *apiv1.InputRevisions) bool {
	if comp.DeletionTimestamp != nil || comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil {
		return false
	}

	for _, r := range comp.Status.CurrentSynthesis.InputRevisions {
		if r.Key != revs.Key {
			continue
		}
		if revs.Revision == nil {
			if r.Revision != nil && *r.Revision == *revs.Revision {
				return true
			}
		} else {
			return r.ResourceVersion == revs.ResourceVersion
		}
	}

	return true // no matching keys
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
		if bindingKey == ref.Key && ref.Resource.Group == gvk.Group && ref.Resource.Version == gvk.Version && ref.Resource.Kind == gvk.Kind {
			return ref.Key
		}
	}

	return ""
}
