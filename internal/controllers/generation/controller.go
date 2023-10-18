package generation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
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

	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Owns(&batchv1.Job{}).
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
		return ctrl.Result{}, fmt.Errorf("listing current jobs: %w", err)
	}

	// Avoid creating duplicate jobs
	job := c.newJob(comp, gen)
	current := &batchv1.Job{}
	err = c.client.Get(ctx, client.ObjectKeyFromObject(job), current)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("listing current jobs: %w", err)
	}
	if err == nil { // !404
		return ctrl.Result{}, nil
	}

	// Skip cases in which the GeneratedResources have already been created
	if comp.Status.ObservedGeneration == comp.Generation {
		return ctrl.Result{}, nil // already in sync
	}

	// Create a job to generate the resouces!
	err = c.client.Create(ctx, job)
	if err != nil {
		return ctrl.Result{}, client.IgnoreAlreadyExists(fmt.Errorf("creating generation job: %w", err))
	}
	c.logger.Info("created job to generate resources for composition", "jobName", job.Name, "compositionName", comp.Name, "compositionGeneration", comp.Generation)

	return ctrl.Result{}, nil
}

func (c *Controller) newJob(comp *apiv1.Composition, gen *apiv1.Generator) *batchv1.Job {
	var (
		parallelism       = int32(1)
		timeout           = int64(c.config.JobTimeout.Seconds())
		ttl               = int32(c.config.JobTTL.Seconds())
		wrapperVolumeName = "wrapper"
	)

	hash := sha256.New()
	fmt.Fprintf(hash, "%s-%d", comp.Name, comp.Generation)
	hashStr := hex.EncodeToString(hash.Sum(nil))[:7]

	job := &batchv1.Job{}
	job.Name = "generate-" + hashStr
	job.Namespace = comp.Namespace
	if err := controllerutil.SetControllerReference(comp, job, c.client.Scheme()); err != nil {
		panic(fmt.Sprintf("unable to set owner reference: %s", err))
	}
	job.Spec.Parallelism = &parallelism
	job.Spec.TTLSecondsAfterFinished = &ttl
	job.Spec.Template = corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			RestartPolicy:         corev1.RestartPolicyNever,
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
				},
			}},
			Volumes: []corev1.Volume{{
				Name:         wrapperVolumeName,
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			}},
		},
	}

	if c.config.JobSA != "" {
		job.Spec.Template.Spec.ServiceAccountName = c.config.JobSA
	}

	return job
}
