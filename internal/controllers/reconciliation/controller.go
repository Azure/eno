package reconciliation

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/conf"
)

// TODO: Handle 404 on deletion

type Controller struct {
	config             *conf.Config
	client             client.Client
	logger             logr.Logger
	upstreamKubeconfig string
}

func NewController(mgr ctrl.Manager, config *conf.Config) error {
	c := &Controller{
		config:             config,
		client:             mgr.GetClient(),
		logger:             mgr.GetLogger(),
		upstreamKubeconfig: os.Getenv("UPSTREAM_KUBECONFIG"),
	}

	accumulator := newAccumulatingReconciler(config, mgr.GetClient(), c.ReconcileMany)
	if err := mgr.Add(accumulator); err != nil {
		return err
	}

	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.GeneratedResource{}).
		Build(accumulator)

	return err
}

func (c *Controller) ReconcileMany(ctx context.Context, reqs []ctrl.Request) error {
	if reqs == nil {
		// TODO: Handle resync
		return nil
	}

	c.logger.Info(fmt.Sprintf("reconciling %d resources", len(reqs)))

	forApplication := []*apiv1.GeneratedResource{}
	forDeletion := []*apiv1.GeneratedResource{}
	r, w := io.Pipe()
	go func() {
		defer w.Close()
		for _, req := range reqs {
			gr := &apiv1.GeneratedResource{}
			err := c.client.Get(ctx, req.NamespacedName, gr)
			if errors.IsNotFound(err) {
				continue // has since been removed
			}
			if err != nil {
				panic(err) // TODO: Handle well
			}
			if gr.DeletionTimestamp != nil {
				forDeletion = append(forDeletion, gr)
				continue
			}
			forApplication = append(forApplication, gr)
			w.Write([]byte(gr.Spec.Manifest))
			w.Write([]byte("\n"))
		}
	}()

	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f=-")
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	cmd.Stdin = r
	if c.upstreamKubeconfig != "" {
		cmd.Env = []string{fmt.Sprintf("KUBECONFIG=" + c.upstreamKubeconfig)}
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("applying resources: %w", err)
	}

	// TODO: In the future we need to parse the kubectl output and determine which resources may have failed to apply
	for _, gr := range forApplication {
		cond := meta.FindStatusCondition(gr.Status.Conditions, apiv1.ReconciledConditionType)
		if cond == nil || cond.ObservedGeneration != gr.Generation {
			meta.SetStatusCondition(&gr.Status.Conditions, metav1.Condition{
				Type:               apiv1.ReconciledConditionType,
				Status:             metav1.ConditionTrue,
				ObservedGeneration: gr.Generation,
				LastTransitionTime: metav1.Now(),
				Reason:             "Synced",
				Message:            "Resource is in sync",
			})
			err := c.client.Status().Update(ctx, gr)
			if err != nil {
				return fmt.Errorf("updating generated resource status after application: %w", err) // TODO: Don't break loop here
			}
		}
	}

	if len(forDeletion) == 0 {
		return nil
	}

	r, w = io.Pipe() // TODO: can we pipe only partial resources to kubectl here to avoid the overhead of parsing?
	go func() {
		defer w.Close()
		for _, gr := range forDeletion {
			w.Write([]byte(gr.Spec.Manifest))
			w.Write([]byte("\n"))
		}
	}()

	cmd = exec.CommandContext(ctx, "kubectl", "delete", "-f=-")
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	cmd.Stdin = r
	if c.upstreamKubeconfig != "" {
		cmd.Env = []string{fmt.Sprintf("KUBECONFIG=" + c.upstreamKubeconfig)}
	}
	if err := cmd.Run(); err != nil {
		// TODO: Parse/handle errors
	}

	// TODO: Need same logic as mentioned previously for status updates
	for _, gr := range forDeletion {
		if !controllerutil.RemoveFinalizer(gr, "eno.azure.io/cleanup") {
			continue
		}
		if err := c.client.Update(ctx, gr); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("removing finalizer after deletion: %w", err) // TODO: Don't break loop here
		}
	}

	return nil
}

type accumulatingReconcilerHandler = func(context.Context, []ctrl.Request) error

type accumulatingReconciler struct {
	config  *conf.Config
	client  client.Client
	handler accumulatingReconcilerHandler
	trigger chan struct{}
	join    sync.Cond
	mut     sync.Mutex
	next    []ctrl.Request
}

// TODO: Do we need to disable resync in controller?

func newAccumulatingReconciler(config *conf.Config, client client.Client, handler accumulatingReconcilerHandler) *accumulatingReconciler {
	return &accumulatingReconciler{
		config:  config,
		client:  client,
		trigger: make(chan struct{}, 1),
		join:    *sync.NewCond(&sync.Mutex{}),
		handler: handler,
	}
}

func (a *accumulatingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	a.mut.Lock()
	a.next = append(a.next, req)
	count := len(a.next)
	select {
	case a.trigger <- struct{}{}:
	default:
	}
	a.mut.Unlock()

	if count >= a.config.MaxReconcileResourceCount {
		a.join.L.Lock()
		a.join.Wait() // block until the current batch has been reconciled
		a.join.L.Unlock()
	}

	gr := &apiv1.GeneratedResource{}
	err := a.client.Get(ctx, req.NamespacedName, gr)
	if err != nil {
		return ctrl.Result{}, err
	}
	if gr.DeletionTimestamp != nil {
		return ctrl.Result{}, nil // don't requeue
	} else {
		return ctrl.Result{RequeueAfter: a.config.ResyncInterval}, nil
	}
}

func (a *accumulatingReconciler) Start(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-a.trigger:
		}

		time.Sleep(a.config.AccumulationWindow)

		a.mut.Lock()
		copy := a.next
		a.next = []ctrl.Request{} // TODO: Small optimization would be to swap between two buffers instead of allocating every time
		a.mut.Unlock()
		a.handler(ctx, copy)
		a.join.Broadcast()

		// TODO: Unblock other routine(s) if waiting
	}
}

func addJitter(dur time.Duration) time.Duration {
	maxJitter := dur * 20 / 100 // max of 20% jitter
	jitter := time.Duration(rand.Int63n(int64(maxJitter*2)) - int64(maxJitter))
	return dur + jitter
}
