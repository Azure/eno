package synthesis

import (
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

func newPod(cfg *Config, comp *apiv1.Composition, syn *apiv1.Synthesizer) *corev1.Pod {
	pod := &corev1.Pod{}
	pod.GenerateName = "synthesis-"
	pod.Namespace = cfg.PodNamespace
	pod.Labels = map[string]string{
		manager.CompositionNameLabelKey:      comp.Name,
		manager.CompositionNamespaceLabelKey: comp.Namespace,
		manager.ManagerLabelKey:              manager.ManagerLabelValue,
		"eno.azure.io/synthesis-uuid":        comp.Status.CurrentSynthesis.UUID,
	}
	for k, v := range syn.Spec.PodOverrides.Labels {
		pod.Labels[k] = v
	}

	pod.Annotations = map[string]string{}
	for k, v := range syn.Spec.PodOverrides.Annotations {
		pod.Annotations[k] = v
	}

	env := []corev1.EnvVar{
		{
			Name:  "COMPOSITION_NAME",
			Value: comp.Name,
		},
		{
			Name:  "COMPOSITION_NAMESPACE",
			Value: comp.Namespace,
		},
		{
			Name:  "SYNTHESIS_UUID",
			Value: comp.Status.CurrentSynthesis.UUID,
		},
		{
			Name:  "SYNTHESIS_ATTEMPT",
			Value: strconv.Itoa(comp.Status.CurrentSynthesis.Attempts + 1), // we write the next attempt _after_ pod creation
		},
	}

	for _, ev := range filterEnv(env, comp.Spec.SynthesisEnv) {
		env = append(env, corev1.EnvVar{Name: ev.Name, Value: ev.Value})
	}

	pod.Spec = corev1.PodSpec{
		ServiceAccountName: cfg.PodServiceAccount,
		RestartPolicy:      corev1.RestartPolicyOnFailure,
		Affinity: &corev1.Affinity{
			PodAntiAffinity: &corev1.PodAntiAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
					Weight: 100,
					PodAffinityTerm: corev1.PodAffinityTerm{
						TopologyKey: "kubernetes.io/hostname",
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								manager.ManagerLabelKey: manager.ManagerLabelValue,
							},
						},
					},
				}},
			},
		},
		InitContainers: []corev1.Container{{
			Name:    "synth-installer",
			Image:   cfg.ExecutorImage,
			Command: []string{"/eno-controller", "install-executor"},
			VolumeMounts: []corev1.VolumeMount{{
				Name:      "sharedfs",
				MountPath: "/eno",
			}},
		}},
		Containers: []corev1.Container{{
			Name:    "executor",
			Image:   syn.Spec.Image,
			Command: []string{"/eno/executor"},
			VolumeMounts: []corev1.VolumeMount{{
				Name:      "sharedfs",
				MountPath: "/eno",
			}},
			Resources: syn.Spec.PodOverrides.Resources,
			Env:       env,
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: ptr.To(false),
				ReadOnlyRootFilesystem:   ptr.To(true),
				RunAsUser:                ptr.To(int64(65532)),
				RunAsGroup:               ptr.To(int64(65532)),
				RunAsNonRoot:             ptr.To(true),
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			},
		}},
		Volumes: []corev1.Volume{{
			Name: "sharedfs",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium: corev1.StorageMediumMemory,
				},
			},
		}},
	}

	if cfg.TaintTolerationKey != "" {
		toleration := corev1.Toleration{
			Key:      cfg.TaintTolerationKey,
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		}
		if cfg.TaintTolerationValue != "" {
			toleration.Operator = corev1.TolerationOpEqual
			toleration.Value = cfg.TaintTolerationValue
		}
		pod.Spec.Tolerations = append(pod.Spec.Tolerations, toleration)
	}

	if cfg.NodeAffinityKey != "" {
		expr := corev1.NodeSelectorRequirement{
			Key:      cfg.NodeAffinityKey,
			Operator: corev1.NodeSelectorOpExists,
		}
		if cfg.NodeAffinityValue != "" {
			expr.Values = []string{cfg.NodeAffinityValue}
			expr.Operator = corev1.NodeSelectorOpIn
		}
		pod.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{expr},
				}},
			},
		}
	}

	return pod
}

func podIsCurrent(comp *apiv1.Composition, pod *corev1.Pod) bool {
	return pod.Labels != nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.UUID == pod.Labels["eno.azure.io/synthesis-uuid"]
}

// filterEnv returns env taking out any items that have the same name as
// any item in filter.
func filterEnv(filter []corev1.EnvVar, env []apiv1.EnvVar) []apiv1.EnvVar {
	res := []apiv1.EnvVar{}
	for _, ev := range env {
		if slices.ContainsFunc(filter, func(f corev1.EnvVar) bool {
			return f.Name == ev.Name
		}) {
			continue
		}
		res = append(res, ev)
	}
	return res
}
