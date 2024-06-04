package synthesis

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

var newPodTests = []struct {
	Name     string
	Cfg      *Config
	Expected *corev1.Pod
	Assert   func(*testing.T, *corev1.Pod)
}{
	{
		Name: "basic",
		Cfg:  &Config{},
		Assert: func(t *testing.T, p *corev1.Pod) {
			assert.Equal(t, "123", p.Annotations["eno.azure.io/composition-generation"])
			assert.Equal(t, "234", p.Annotations["eno.azure.io/synthesizer-generation"])
			assert.Equal(t, "eno", p.Labels["app.kubernetes.io/managed-by"])
			assert.Nil(t, p.Spec.Affinity.NodeAffinity)
			assert.Len(t, p.Spec.Tolerations, 0)
		},
	},
	{
		Name: "with affinity key/value",
		Cfg: &Config{
			NodeAffinityKey:   "foo",
			NodeAffinityValue: "bar",
		},
		Assert: func(t *testing.T, p *corev1.Pod) {
			require.NotNil(t, p.Spec.Affinity.NodeAffinity)
			assert.Equal(t, corev1.NodeSelectorOpIn, p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Operator)
			assert.Equal(t, []string{"bar"}, p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Values)
		},
	},
	{
		Name: "with toleration key/value",
		Cfg: &Config{
			TaintTolerationKey:   "foo",
			TaintTolerationValue: "bar",
		},
		Assert: func(t *testing.T, p *corev1.Pod) {
			require.Len(t, p.Spec.Tolerations, 1)
			assert.Equal(t, corev1.TolerationOpEqual, p.Spec.Tolerations[0].Operator)
			assert.Equal(t, "bar", p.Spec.Tolerations[0].Value)
		},
	},
	{
		Name: "with affinity key",
		Cfg: &Config{
			NodeAffinityKey: "foo",
		},
		Assert: func(t *testing.T, p *corev1.Pod) {
			require.NotNil(t, p.Spec.Affinity.NodeAffinity)
			assert.Equal(t, corev1.NodeSelectorOpExists, p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Operator)
		},
	},
	{
		Name: "with toleration key",
		Cfg: &Config{
			TaintTolerationKey: "foo",
		},
		Assert: func(t *testing.T, p *corev1.Pod) {
			require.Len(t, p.Spec.Tolerations, 1)
			assert.Equal(t, corev1.TolerationOpExists, p.Spec.Tolerations[0].Operator)
		},
	},
}

func TestNewPod(t *testing.T) {
	for _, tc := range newPodTests {
		comp := &apiv1.Composition{}
		comp.Name = "test-composition"
		comp.Namespace = "test-composition-ns"
		comp.Generation = 123
		comp.Status.CurrentSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}

		syn := &apiv1.Synthesizer{}
		syn.Name = "test-synth"
		syn.Generation = 234

		t.Run(tc.Name, func(t *testing.T) {
			pod := newPod(tc.Cfg, comp, syn)
			tc.Assert(t, pod)
		})
	}
}
