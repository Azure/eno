package synthesis

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

type Config struct {
	ExecutorImage     string
	PodNamespace      string
	PodServiceAccount string

	TaintTolerationKey   string
	TaintTolerationValue string

	NodeAffinityKey   string
	NodeAffinityValue string

	PodLabelOverrides      map[string]string
	PodAnnotationOverrides map[string]string
}

type podLifecycleController struct {
	config        *Config
	client        client.Client
	noCacheReader client.Reader
}

// NewPodLifecycleController is responsible for creating and deleting pods as needed to synthesize compositions.
func NewPodLifecycleController(mgr ctrl.Manager, cfg *Config) error {
	c := &podLifecycleController{
		config:        cfg,
		client:        mgr.GetClient(),
		noCacheReader: mgr.GetAPIReader(),
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		WatchesRawSource(source.TypedKind[*corev1.Pod](mgr.GetCache(), &corev1.Pod{}, c.newPodEventHandler())).
		WithLogConstructor(manager.NewLogConstructor(mgr, "podLifecycleController")).
		Complete(c)
}

func (c *podLifecycleController) newPodEventHandler() handler.TypedEventHandler[*corev1.Pod, reconcile.Request] {
	return &handler.TypedFuncs[*corev1.Pod, reconcile.Request]{
		CreateFunc: func(ctx context.Context, e event.TypedCreateEvent[*corev1.Pod], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		},
		UpdateFunc: func(ctx context.Context, e event.TypedUpdateEvent[*corev1.Pod], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if e.ObjectNew.DeletionTimestamp == nil || e.ObjectOld.DeletionTimestamp != nil || e.ObjectNew.Labels == nil {
				return
			}
			nsn := types.NamespacedName{
				Name:      e.ObjectNew.GetLabels()[compositionNameLabelKey],
				Namespace: e.ObjectNew.GetLabels()[compositionNamespaceLabelKey],
			}
			q.Add(reconcile.Request{NamespacedName: nsn})
		},
		DeleteFunc: func(ctx context.Context, e event.TypedDeleteEvent[*corev1.Pod], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if e.DeleteStateUnknown || e.Object.Labels == nil {
				return
			}
			nsn := types.NamespacedName{
				Name:      e.Object.GetLabels()[compositionNameLabelKey],
				Namespace: e.Object.GetLabels()[compositionNamespaceLabelKey],
			}
			q.Add(reconcile.Request{NamespacedName: nsn})
		},
	}
}

func (c *podLifecycleController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if errors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		logger.Error(err, "failed to get composition")
		return ctrl.Result{}, err
	}
	if comp.DeletionTimestamp != nil ||
		!controllerutil.ContainsFinalizer(comp, "eno.azure.io/cleanup") ||
		comp.Status.InFlightSynthesis == nil ||
		comp.Status.InFlightSynthesis.Canceled != nil {
		return ctrl.Result{}, nil
	}

	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation, "synthesisUUID", comp.Status.InFlightSynthesis.UUID,
		"operationID", comp.GetAzureOperationID(), "operationOrigin", comp.GetAzureOperationOrigin())
	ctx = logr.NewContext(ctx, logger)

	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	if err != nil {
		logger.Error(err, "failed to get synthesizer")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if syn != nil {
		logger = logger.WithValues("synthesizerName", syn.Name, "synthesizerGeneration", syn.Generation)
		ctx = logr.NewContext(ctx, logger)
	}

	// Confirm that a pod doesn't already exist for this synthesis without trusting informers.
	// This protects against cases where synthesis has recently started and something causes
	// another tick of this loop before the pod write hits the informer.
	pods := &corev1.PodList{}
	err = c.noCacheReader.List(ctx, pods, client.InNamespace(c.config.PodNamespace), client.MatchingLabels{
		synthesisIDLabelKey: comp.Status.InFlightSynthesis.UUID,
	})
	if err != nil {
		logger.Error(err, fmt.Sprintf("Error while listing Pods in Namespace[%s], SynthesisUUID[%s]",
			c.config.PodNamespace, comp.Status.InFlightSynthesis.UUID))
		return ctrl.Result{}, fmt.Errorf("checking for existing pod: %w", err)
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp == nil {
			logger.Info(fmt.Sprintf("refusing to create new synthesizer pod because the pod [%q] already exists and has not been deleted", pod.Name))
			return ctrl.Result{}, nil
		}
	}

	// If we made it this far it's safe to create a pod
	pod := newPod(c.config, comp, syn)
	err = c.client.Create(ctx, pod)
	if err != nil {
		logger.Error(err, fmt.Sprintf("failed to create pod Name[%s], Namespace[%s]", pod.GetName(), pod.GetNamespace()))
		return ctrl.Result{}, fmt.Errorf("creating pod: %w", err)
	}
	logger.Info("created synthesizer pod", "podName", pod.Name)
	sytheses.Inc()

	return ctrl.Result{}, nil
}
