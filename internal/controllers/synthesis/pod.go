package synthesis

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
)

// TODO: Finish, add labels, etc.

func newPod(cfg *Config, scheme *runtime.Scheme, comp *apiv1.Composition, syn *apiv1.Synthesizer) *corev1.Pod {
	const wrapperVolumeName = "wrapper"

	hash := sha256.New()
	fmt.Fprintf(hash, "%s-%d-%s-%d", comp.Name, comp.Generation, syn.Name, syn.Generation) // TODO: Generate name?
	hashStr := hex.EncodeToString(hash.Sum(nil))[:7]

	pod := &corev1.Pod{}
	pod.Name = "generate-" + hashStr
	pod.Namespace = comp.Namespace
	if err := controllerutil.SetControllerReference(comp, pod, scheme); err != nil {
		panic(fmt.Sprintf("unable to set owner reference: %s", err))
	}
	pod.Spec = corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyOnFailure,
		InitContainers: []corev1.Container{{
			Name:  "setup",
			Image: cfg.WrapperImage,
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
			Image: syn.Spec.Image,
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
					Value: strconv.FormatInt(syn.Generation, 10),
				},
			},
		}},
		Volumes: []corev1.Volume{{
			Name:         wrapperVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		}},
	}

	if cfg.JobSA != "" {
		pod.Spec.ServiceAccountName = cfg.JobSA
	}

	return pod
}
