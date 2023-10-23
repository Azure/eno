package generation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/conf"
)

type Controller struct {
	config *conf.Config
	client client.Client
	logger logr.Logger
}

func NewController(mgr ctrl.Manager, config *conf.Config) error {
	c := &Controller{
		config: config,
		client: mgr.GetClient(),
		logger: mgr.GetLogger(),
	}

	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Owns(&corev1.Pod{}). // TODO: Need to use a label selector for perf
		Watches(&apiv1.Generator{}, &generatorHandler{ctrl: c}).
		Build(c)

	return err
}

func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting composition resource: %w", err))
	}

	if comp.Spec.Generator == nil {
		return ctrl.Result{}, nil // can't generate the composed resources without a generator
	}

	gen := &apiv1.Generator{}
	gen.Name = comp.Spec.Generator.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(gen), gen)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing current pods: %w", err)
	}

	// TODO: Clean up all pods owned by this composition, use generateName for their names

	// Avoid creating duplicate pods
	pod := c.newPod(comp, gen)
	current := &corev1.Pod{}
	logger := c.logger.WithValues("podName", pod.Name, "compositionName", comp.Name, "compositionGeneration", comp.Generation, "generatorGeneration", gen.Generation)

	err = c.client.Get(ctx, client.ObjectKeyFromObject(pod), current)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("getting current pod: %w", err)
	}
	if err == nil {
		if !comp.Status.LastGeneratorCreation.Equal(&current.CreationTimestamp) {
			// TODO: Is there ever a case in which this code would not be reached? I think there is but it isn't obvious
			comp.Status.LastGeneratorCreation = &current.CreationTimestamp
			if err := c.client.Status().Update(ctx, comp); err != nil {
				return ctrl.Result{}, fmt.Errorf("updating composition status: %w", err)
			}
			c.logger.Info("set last generator creation time in composition status")
			return ctrl.Result{}, nil
		}

		if current.DeletionTimestamp == nil && shouldDeletePod(current) {
			err = c.client.Delete(ctx, current)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("error deleting pod: %w", err)
			}
			c.logger.Info("deleted generation pod")
		}
		return ctrl.Result{}, nil
	}

	// Skip cases in which the GeneratedResources have already been created
	if comp.Status.CompositionGeneration == comp.Generation && comp.Status.GeneratorGeneration == gen.Generation {
		return ctrl.Result{}, nil // already in sync
	}

	// Slow-roll generator changes across referencing compositions only when the composition itself hasn't changed
	if comp.Status.GeneratorGeneration != gen.Generation && comp.Status.CompositionGeneration == comp.Generation {
		res, err := c.shouldDeferForRollingUpdate(ctx, gen)
		if err != nil {
			return ctrl.Result{}, err
		}
		if res != nil {
			logger.Info(fmt.Sprintf("deferring re-generation for %s because another composition is within the cooldown window", res.RequeueAfter))
			return *res, nil
		}
		logger.Info("re-generating composition because generator changed")
	}

	// Create a pod to generate the resouces!
	err = c.client.Create(ctx, pod)
	if err != nil {
		return ctrl.Result{}, client.IgnoreAlreadyExists(fmt.Errorf("creating generation pod: %w", err))
	}
	logger.Info("created pod to generate resources for composition")

	return ctrl.Result{}, nil
}

func (c *Controller) newPod(comp *apiv1.Composition, gen *apiv1.Generator) *corev1.Pod {
	const wrapperVolumeName = "wrapper"

	hash := sha256.New()
	fmt.Fprintf(hash, "%s-%d", comp.Name, comp.Generation)
	hashStr := hex.EncodeToString(hash.Sum(nil))[:7]

	pod := &corev1.Pod{}
	pod.Name = "generate-" + hashStr
	pod.Namespace = comp.Namespace
	if err := controllerutil.SetControllerReference(comp, pod, c.client.Scheme()); err != nil {
		panic(fmt.Sprintf("unable to set owner reference: %s", err))
	}
	pod.Spec = corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyOnFailure,
		InitContainers: []corev1.Container{{
			Name:  "setup",
			Image: c.config.WrapperImage,
			Command: []string{
				"/eno-wrapper", "--install=/wrapper/eno-wrapper",
			},
			VolumeMounts: []corev1.VolumeMount{{
				Name:      wrapperVolumeName,
				MountPath: "/wrapper",
			}},
		}},
		Containers: []corev1.Container{{
			Name:  "generator",
			Image: gen.Spec.Image,
			Command: []string{
				"/wrapper/eno-wrapper", "--generate",
			},
			VolumeMounts: []corev1.VolumeMount{{
				Name:      wrapperVolumeName,
				MountPath: "/wrapper",
				ReadOnly:  true,
			}},
			Env: []corev1.EnvVar{
				{
					Name:  "COMPOSITION_NAME",
					Value: comp.Name,
				},
				{
					Name:  "COMPOSITION_NAMESPACE",
					Value: comp.Namespace,
				},
				{
					Name:  "COMPOSITION_GENERATION",
					Value: strconv.FormatInt(comp.Generation, 10),
				},
				{
					Name:  "GENERATOR_GENERATION",
					Value: strconv.FormatInt(gen.Generation, 10),
				},
			},
		}},
		Volumes: []corev1.Volume{{
			Name:         wrapperVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		}},
	}

	if c.config.JobSA != "" {
		pod.Spec.ServiceAccountName = c.config.JobSA
	}

	return pod
}

func (c *Controller) shouldDeferForRollingUpdate(ctx context.Context, gen *apiv1.Generator) (*ctrl.Result, error) {
	list := &apiv1.CompositionList{}
	if err := c.client.List(ctx, list); err != nil { // TODO: Consider an index here
		return nil, err
	}

	for _, item := range list.Items {
		if item.Spec.Generator == nil || item.Spec.Generator.Name != gen.Name || item.Status.LastGeneratorCreation == nil {
			continue
		}
		sinceLastGeneration := time.Since(item.Status.LastGeneratorCreation.Time)
		remainingCooldown := c.config.RolloutCooldown - sinceLastGeneration
		if remainingCooldown > 0 {
			return &ctrl.Result{Requeue: true, RequeueAfter: remainingCooldown}, nil
		}
	}

	return nil, nil
}

func shouldDeletePod(pod *corev1.Pod) bool {
	for _, cont := range pod.Status.ContainerStatuses {
		// Recreate the pod on another node eventually in case it was scheduled to a broken one
		if cont.RestartCount > 5 { // TODO: Expose in config
			return true
		}

		// TODO: Handle pull failures also?

		if cont.State.Terminated == nil || cont.State.Terminated.ExitCode != 0 {
			return false // has not completed yet
		}
	}
	return len(pod.Status.ContainerStatuses) > 0
}

type generatorHandler struct {
	ctrl *Controller
}

func (h *generatorHandler) Generic(ctx context.Context, evt event.GenericEvent, q workqueue.RateLimitingInterface) {
	h.handle(ctx, evt.Object, q)
}

func (h *generatorHandler) Create(ctx context.Context, evt event.CreateEvent, q workqueue.RateLimitingInterface) {
	h.handle(ctx, evt.Object, q)
}

func (h *generatorHandler) Delete(ctx context.Context, evt event.DeleteEvent, q workqueue.RateLimitingInterface) {
	h.handle(ctx, evt.Object, q)
}

func (h *generatorHandler) Update(ctx context.Context, evt event.UpdateEvent, q workqueue.RateLimitingInterface) {
	switch {
	case evt.ObjectNew != nil:
		h.handle(ctx, evt.ObjectNew, q)
	case evt.ObjectOld != nil:
		h.handle(ctx, evt.ObjectOld, q)
	default:
	}
}

func (h *generatorHandler) handle(ctx context.Context, obj client.Object, q workqueue.RateLimitingInterface) {
	if obj == nil {
		h.ctrl.logger.Info("generator handler got nil object")
		return
	}

	list := &apiv1.CompositionList{}
	err := h.ctrl.client.List(ctx, list)
	if err != nil {
		// this should be impossible since we're reading from the informer cache
		h.ctrl.logger.Error(err, "error while listing compositions to be enqueued")
		return
	}
	for _, item := range list.Items {
		if item.Spec.Generator != nil && item.Spec.Generator.Name == obj.GetName() {
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
				Name:      item.GetName(),
				Namespace: item.GetNamespace(),
			}})
		}
	}
}
