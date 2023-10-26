package reconciliation

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/conf"
)

// TODO: Handle 404 on deletion

type Controller struct {
	config         *conf.Config
	client         client.Client
	upstreamClient client.Client
	logger         logr.Logger

	// needs a mutex if worker concurrency > 1
	cache map[types.NamespacedName]*unstructured.Unstructured
}

func NewController(mgr ctrl.Manager, config *conf.Config) error {
	c := &Controller{
		config:         config,
		client:         mgr.GetClient(),
		upstreamClient: mgr.GetClient(), // TODO: Support a second kubeconfig
		logger:         mgr.GetLogger(),
		cache:          make(map[types.NamespacedName]*unstructured.Unstructured),
	}

	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.GeneratedResource{}).
		Build(c)

	return err
}

func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	gr := &apiv1.GeneratedResource{}
	err := c.client.Get(ctx, req.NamespacedName, gr)
	if errors.IsNotFound(err) {
		delete(c.cache, req.NamespacedName)
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if gr.DeletionTimestamp != nil {
		// TODO: Finalizer
	}

	// TODO: Invalidate cache when resource version changes
	obj, ok := c.cache[req.NamespacedName]
	if !ok {
		obj = &unstructured.Unstructured{}
		_, _, err = unstructured.UnstructuredJSONScheme.Decode([]byte(gr.Spec.Manifest), nil, obj)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("decoding: %w", err)
		}
		c.cache[req.NamespacedName] = obj
	}

	current := &unstructured.Unstructured{}
	current.SetAPIVersion(obj.GetAPIVersion())
	current.SetKind(obj.GetKind())
	err = c.upstreamClient.Get(ctx, client.ObjectKeyFromObject(obj), current)
	if errors.IsNotFound(err) {
		c.logger.Info("creating resource")
		err = c.upstreamClient.Create(ctx, obj)
		if err != nil {
			return ctrl.Result{}, nil
		}

		gr.Status.PreviousManifest = gr.Spec.Manifest
		return ctrl.Result{}, c.client.Status().Update(ctx, gr)
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting: %w", err)
	}

	currentJS, err := current.MarshalJSON()
	if err != nil {
		return ctrl.Result{}, err
	}

	// TODO: Also support strategic merge patch using openapi
	patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch([]byte(gr.Status.PreviousManifest), []byte(gr.Spec.Manifest), currentJS)
	if err != nil {
		return ctrl.Result{}, err
	}
	// TODO: always patch for the sake of the benchmark
	//
	// if string(patch) == "{}" {
	// 	c.logger.Info("not patching because patch is empty")
	// 	return ctrl.Result{}, nil
	// }

	c.logger.Info("patching resource: " + string(patch))
	err = c.upstreamClient.Patch(ctx, obj, client.RawPatch(types.MergePatchType, patch))
	if err != nil {
		return ctrl.Result{}, err
	}

	gr.Status.PreviousManifest = gr.Spec.Manifest
	return ctrl.Result{RequeueAfter: jitter(c.config.ResyncInterval)}, c.client.Status().Update(ctx, gr)
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

func jitter(dur time.Duration) time.Duration {
	maxJitter := dur * 20 / 100
	jitter := time.Duration(rand.Int63n(int64(maxJitter*2)) - int64(maxJitter))
	return dur + jitter
}
