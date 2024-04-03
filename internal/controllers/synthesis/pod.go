package synthesis

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"

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

	userID := int64(1000)
	yes := true
	pod.Spec = corev1.PodSpec{
		ServiceAccountName: cfg.PodServiceAccount,
		Containers: []corev1.Container{{
			Name:    "synthesizer",
			Image:   syn.Spec.Image,
			Command: []string{"sleep", "infinity"},
			SecurityContext: &corev1.SecurityContext{
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
				RunAsUser:    &userID,
				RunAsNonRoot: &yes,
			},
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
					Name:  "SYNTHESIZER_GENERATION",
					Value: strconv.FormatInt(syn.Generation, 10),
				},
			},
		}},
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
