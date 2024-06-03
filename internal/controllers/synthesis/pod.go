package synthesis

import (
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
	pod.Finalizers = []string{"eno.azure.io/cleanup"}
	pod.Labels = map[string]string{
		manager.CompositionNameLabelKey:      comp.Name,
		manager.CompositionNamespaceLabelKey: comp.Namespace,
		manager.ManagerLabelKey:              manager.ManagerLabelValue,
		"eno.azure.io/synthesis-uuid":        comp.Status.CurrentSynthesis.UUID,
	}
	pod.Annotations = map[string]string{
		"eno.azure.io/composition-generation": strconv.FormatInt(comp.Generation, 10),
		"eno.azure.io/synthesizer-generation": strconv.FormatInt(syn.Generation, 10),
	}

	pod.Spec = corev1.PodSpec{
		ServiceAccountName: cfg.PodServiceAccount,
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
		Containers: []corev1.Container{{
			Name:    "synthesizer",
			Image:   syn.Spec.Image,
			Command: []string{"sleep", "infinity"},
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

func podDerivedFrom(comp *apiv1.Composition, pod *corev1.Pod) bool {
	if pod.Annotations == nil {
		return false
	}

	compGen, _ := strconv.ParseInt(pod.Annotations["eno.azure.io/composition-generation"], 10, 0)
	return compGen == comp.Generation
}
