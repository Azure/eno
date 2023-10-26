package reconciliation

import (
	"context"

	"github.com/go-logr/logr"
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
}

func NewController(mgr ctrl.Manager, config *conf.Config) error {
	c := &Controller{
		config:         config,
		client:         mgr.GetClient(),
		upstreamClient: mgr.GetClient(), // TODO: Support a second kubeconfig
		logger:         mgr.GetLogger(),
	}

	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.GeneratedResource{}).
		Build(c)

	return err
}

func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	gr := &apiv1.GeneratedResource{}
	err := c.client.Get(ctx, req.NamespacedName, gr)
	if err != nil {
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}
