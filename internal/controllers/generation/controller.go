package generation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

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

	// TODO: We need to watch generators for the rollout logic to work

	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Owns(&corev1.Pod{}). // TODO: Need to use a label selector for perf
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

	// Avoid creating duplicate pods
	pod := c.newPod(comp, gen)
	current := &corev1.Pod{}
	logger := c.logger.WithValues("podName", pod.Name, "compositionName", comp.Name, "compositionGeneration", comp.Generation, "generatorGeneration", gen.Generation)

	err = c.client.Get(ctx, client.ObjectKeyFromObject(pod), current)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("getting current pod: %w", err)
	}
	if err == nil {
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

	// Slow-roll generator changes across referencing compositions
	if comp.Status.GeneratorGeneration != gen.Generation {
		logger.Info("regenerating composition because generator changed")
		// TODO: Implement slow-roll logic
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
	var (
		timeout           = int64(c.config.JobTimeout.Seconds())
		wrapperVolumeName = "wrapper"
	)

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
		RestartPolicy:         corev1.RestartPolicyOnFailure,
		ActiveDeadlineSeconds: &timeout,
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

func shouldDeletePod(pod *corev1.Pod) bool {
	for _, cont := range pod.Status.ContainerStatuses {
		// Recreate the pod on another node eventually in case it was scheduled to a broken one
		if cont.RestartCount > 5 {
			return true
		}

		// TODO: Handle pull failures also?

		if cont.State.Terminated == nil || cont.State.Terminated.ExitCode != 0 {
			return false // has not completed yet
		}
	}
	return len(pod.Status.ContainerStatuses) > 0
}
