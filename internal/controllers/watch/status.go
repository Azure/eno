package watch

import (
	"context"
	"fmt"
	"strconv"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// refStatusController reconciles the status of ReferencedResource CRs to that of the actual resources they reference.
// Each instance of this controller reconciles one resource type only, and is expected to follow the lifecycle of an ephemeral watch.
type refStatusController struct {
	ref     *apiv1.ResourceRef
	version string
	client  client.Client

	cancel  context.CancelFunc
	running chan struct{}
}

func newRefStatusController(ctx context.Context, c *controllerController, ref *apiv1.ResourceRef) (*refStatusController, error) {
	rc := &refStatusController{ref: ref, client: c.client, running: make(chan struct{})}
	logger := logr.FromContextOrDiscard(ctx).WithValues("group", ref.Group, "kind", ref.Kind)

	mapping, err := c.client.RESTMapper().RESTMapping(schema.GroupKind{Group: ref.Group, Kind: ref.Kind})
	if err != nil {
		return nil, err
	}
	rc.version = mapping.Resource.Version
	logger = logger.WithValues("version", rc.version)

	rrc, err := controller.NewUnmanaged("watchRefStatusController", c.mgr, controller.Options{
		LogConstructor: manager.NewLogConstructor(c.mgr, "watchRefStatusController"),
		RateLimiter:    c.limiter,
		Reconciler:     rc,
	})
	if err != nil {
		return nil, err
	}

	// Watch ReferencedResources for this group/kind
	err = rrc.Watch(source.Kind(c.mgr.GetCache(), &apiv1.ReferencedResource{}), &handler.EnqueueRequestForObject{},
		predicate.NewPredicateFuncs(func(object client.Object) bool {
			rr, ok := object.(*apiv1.ReferencedResource)
			if !ok {
				return false
			}
			return rr.Spec.Input.Group == ref.Group && rr.Spec.Input.Kind == ref.Kind
		}))
	if err != nil {
		return nil, err
	}

	// Watch the metadata of the actual resources
	obj := &metav1.PartialObjectMetadata{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   ref.Group,
		Version: rc.version,
		Kind:    ref.Kind,
	})
	err = rrc.Watch(source.Kind(c.mgr.GetCache(), obj), handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
		rrl := &apiv1.ReferencedResourceList{}
		err := c.client.List(ctx, rrl, client.MatchingFields{
			manager.IdxReferencedResourcesByRef: manager.ReferencedResourceIdxValueFromInputResource(&apiv1.InputResource{
				Name:      o.GetName(),
				Namespace: o.GetNamespace(),
				Kind:      ref.Kind,
				Group:     ref.Group,
			}),
		})
		if err != nil {
			logr.FromContextOrDiscard(ctx).Error(err, "unexpected error while looking up ReferencedResource for external object")
			return nil
		}

		reqs := make([]ctrl.Request, len(rrl.Items))
		for i, rl := range rrl.Items {
			reqs[i] = ctrl.Request{NamespacedName: types.NamespacedName{Name: rl.Name}}
		}
		return reqs
	}))
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	rc.cancel = cancel
	go func() {
		defer cancel()

		logger.V(1).Info("observing referenced resource")
		err := rrc.Start(ctx)
		if err != nil {
			panic(fmt.Errorf("error while running resource ref controller: %s", err))
		}
		logger.V(1).Info("done observing referenced resource")
		close(rc.running)
	}()

	return rc, nil
}

func (c *refStatusController) Stop(ctx context.Context) {
	logger := logr.FromContextOrDiscard(ctx)
	if c.cancel == nil {
		<-c.running
		return
	}
	logger.V(1).Info("stopping referenced resource controller")
	c.cancel()
	c.cancel = nil
	<-c.running
}

func (c *refStatusController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	rr := &apiv1.ReferencedResource{}
	err := c.client.Get(ctx, req.NamespacedName, rr)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting referenced resource CRs: %w", err))
	}

	logger := logr.FromContextOrDiscard(ctx).WithValues("referencedResourceName", rr.Name, "group", rr.Spec.Input.Group, "kind", rr.Spec.Input.Kind, "name", rr.Spec.Input.Name, "namespace", rr.Spec.Input.Namespace)
	ctx = logr.NewContext(ctx, logger)

	meta := &metav1.PartialObjectMetadata{}
	meta.SetName(rr.Spec.Input.Name)
	meta.SetNamespace(rr.Spec.Input.Namespace)
	meta.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   rr.Spec.Input.Group,
		Version: c.version,
		Kind:    rr.Spec.Input.Kind,
	})
	err = c.client.Get(ctx, client.ObjectKeyFromObject(meta), meta)
	if errors.IsNotFound(err) {
		err = nil
		meta = nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting referenced resource: %w", err)
	}

	// Derive our representation of the resource's status
	state := &apiv1.ReferencedResourceState{
		ObservationTime: metav1.Now(),
	}
	if meta != nil {
		state.ResourceVersion = meta.ResourceVersion

		atomicAnno := meta.GetAnnotations()["eno.azure.io/atomic"]
		state.AtomicVersion, err = strconv.Atoi(atomicAnno)
		if atomicAnno != "" && err != nil {
			logger.Error(err, "invalid atomic annotation")
		}
	} else {
		state.Missing = true
	}

	if ls := rr.Status.LastSeen; ls != nil && state.AtomicVersion == ls.AtomicVersion && state.ResourceVersion == ls.ResourceVersion && state.Missing == ls.Missing {
		return ctrl.Result{}, nil // already in sync
	}

	// Write the update
	rr.Status.LastSeen = state
	err = c.client.Status().Update(ctx, rr)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("writing current state: %w", err)
	}
	logger.V(1).Info("wrote updated state of referenced resource")

	return ctrl.Result{}, nil
}
