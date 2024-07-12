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
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
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

	err = rrc.Watch(source.Kind(parent.mgr.GetCache(), ref), &handler.EnqueueRequestForObject{})
	return rrc, err
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

			err = k.client.Status().Update(ctx, &comp)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("swapping composition state: %w", err)
			}
			logger.V(0).Info("noticed input resource change", "compositionName", comp.Name, "compositionNamespace", comp.Namespace, "ref", key, "deferred", deferred)
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
