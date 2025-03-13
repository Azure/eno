package synthesis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
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
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting composition resource: %w", err))
	}

	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation)

	if comp.DeletionTimestamp != nil {
		return c.reconcileDeletedComposition(ctx, comp)
	}

	// It isn't safe to delete compositions until their resource slices have been cleaned up,
	// since reconciling resources necessarily requires the composition.
	if controllerutil.AddFinalizer(comp, "eno.azure.io/cleanup") {
		err = c.client.Update(ctx, comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("updating composition: %w", err)
		}
		logger.V(1).Info("added cleanup finalizer to composition")
		return ctrl.Result{}, nil
	}

	if comp.Status.InFlightSynthesis == nil || comp.Status.InFlightSynthesis.Synthesized != nil {
		return ctrl.Result{}, nil
	}
	logger = logger.WithValues("synthesisID", comp.Status.InFlightSynthesis.UUID)

	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting synthesizer: %w", err))
	}
	if syn != nil {
		logger = logger.WithValues("synthesizerName", syn.Name, "synthesizerGeneration", syn.Generation)
	}

	// Back off to avoid constantly re-synthesizing impossible compositions (unlikely but possible)
	if shouldBackOffPodCreation(comp) {
		const base = time.Millisecond * 250
		wait := base * time.Duration(comp.Status.InFlightSynthesis.Attempts)
		nextAttempt := comp.Status.InFlightSynthesis.PodCreation.Time.Add(wait)
		if time.Since(nextAttempt) < 0 { // positive when past the nextAttempt
			logger.V(1).Info("backing off pod creation", "latency", wait.Abs().Milliseconds())
			return ctrl.Result{RequeueAfter: wait}, nil
		}
	}

	// Confirm that a pod doesn't already exist for this synthesis without trusting informers.
	// This protects against cases where synthesis has recently started and something causes
	// another tick of this loop before the pod write hits the informer.
	pods := &corev1.PodList{}
	err = c.noCacheReader.List(ctx, pods, client.InNamespace(c.config.PodNamespace), client.MatchingLabels{
		synthesisIDLabelKey: comp.Status.InFlightSynthesis.UUID,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("checking for existing pod: %w", err)
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp == nil {
			logger.V(1).Info(fmt.Sprintf("refusing to create new synthesizer pod because the pod %q already exists and has not been deleted", pod.Name))
			return ctrl.Result{}, nil
		}
	}

	// If we made it this far it's safe to create a pod
	pod := newPod(c.config, comp, syn)
	err = c.client.Create(ctx, pod)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("creating pod: %w", err)
	}
	logger.V(0).Info("created synthesizer pod", "podName", pod.Name)
	sytheses.Inc()

	// This metadata is optional - it's safe for the process to crash before reaching this point
	patch := []map[string]any{
		{"op": "test", "path": "/status/inFlightSynthesis/uuid", "value": comp.Status.InFlightSynthesis.UUID},
		{"op": "test", "path": "/status/inFlightSynthesis/synthesized", "value": nil},
		{"op": "replace", "path": "/status/inFlightSynthesis/attempts", "value": comp.Status.InFlightSynthesis.Attempts + 1},
		{"op": "replace", "path": "/status/inFlightSynthesis/podCreation", "value": pod.CreationTimestamp},
	}
	patchJS, err := json.Marshal(&patch)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("encoding patch: %w", err)
	}

	if err := c.client.Status().Patch(ctx, comp, client.RawPatch(types.JSONPatchType, patchJS)); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating composition status after synthesizer pod creation: %w", err)
	}

	return ctrl.Result{}, nil
}

func (c *podLifecycleController) reconcileDeletedComposition(ctx context.Context, comp *apiv1.Composition) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	// Deletion increments the composition's generation, but the reconstitution cache is only invalidated
	// when the synthesized generation (from the status) changes, which will never happen because synthesis
	// is righly disabled for deleted compositions. We break out of this deadlock condition by updating
	// the status without actually synthesizing.
	if shouldUpdateDeletedCompositionStatus(comp) {
		comp.Status.CurrentSynthesis.ObservedCompositionGeneration = comp.Generation
		comp.Status.CurrentSynthesis.UUID = uuid.NewString()
		comp.Status.CurrentSynthesis.Synthesized = ptr.To(metav1.Now())
		comp.Status.CurrentSynthesis.Reconciled = nil
		comp.Status.CurrentSynthesis.Ready = nil
		err := c.client.Status().Update(ctx, comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("updating current composition generation: %w", err)
		}
		logger.V(0).Info("updated composition status to reflect deletion")
		return ctrl.Result{}, nil
	}

	// Remove the finalizer when all pods and slices have been deleted
	if isReconciling(comp) {
		logger.V(1).Info("refusing to remove composition finalizer because it is still being reconciled")
		return ctrl.Result{}, nil
	}
	if controllerutil.RemoveFinalizer(comp, "eno.azure.io/cleanup") {
		err := c.client.Update(ctx, comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
		}

		logger.V(0).Info("removed finalizer from composition")
	}

	return ctrl.Result{}, nil
}

func shouldBackOffPodCreation(comp *apiv1.Composition) bool {
	current := comp.Status.InFlightSynthesis
	return current != nil && current.Attempts > 0 && current.PodCreation != nil
}

func shouldUpdateDeletedCompositionStatus(comp *apiv1.Composition) bool {
	return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.ObservedCompositionGeneration != comp.Generation
}

func isReconciling(comp *apiv1.Composition) bool {
	return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled == nil
}
