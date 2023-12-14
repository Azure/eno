package synthesis

import (
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
)

func newPod(cfg *Config, scheme *runtime.Scheme, comp *apiv1.Composition, syn *apiv1.Synthesizer) *corev1.Pod {
	pod := &corev1.Pod{}
	pod.GenerateName = "synthesis-"
	pod.Namespace = comp.Namespace
	pod.Finalizers = []string{"eno.azure.io/cleanup"}
	pod.Labels = map[string]string{"app.kubernetes.io/managed-by": "eno"}
	pod.Annotations = map[string]string{
		"eno.azure.io/composition-generation": strconv.FormatInt(comp.Generation, 10),
		"eno.azure.io/synthesizer-generation": strconv.FormatInt(syn.Generation, 10),
	}
	if err := controllerutil.SetControllerReference(comp, pod, scheme); err != nil {
		panic(fmt.Sprintf("unable to set owner reference: %s", err))
	}

	userID := int64(1000)
	yes := true
	pod.Spec = corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyOnFailure,
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

	if cfg.JobSA != "" {
		pod.Spec.ServiceAccountName = cfg.JobSA
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
