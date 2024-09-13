package synthesis

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

var newPodTests = []struct {
	Name     string
	Cfg      *Config
	Synth    *apiv1.Synthesizer
	Comp     *apiv1.Composition
	Expected *corev1.Pod
	Assert   func(*testing.T, *corev1.Pod)
}{
	{
		Name: "basic",
		Cfg:  &Config{},
		Assert: func(t *testing.T, p *corev1.Pod) {
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
	{
		Name: "with full overrides struct",
		Synth: &apiv1.Synthesizer{
			Spec: apiv1.SynthesizerSpec{
				PodOverrides: apiv1.PodOverrides{
					Labels:      map[string]string{"foo": "bar"},
					Annotations: map[string]string{"baz": "something"},
					Resources:   corev1.ResourceRequirements{Limits: corev1.ResourceList{"cpu": resource.MustParse("9001")}},
				},
			},
		},
		Assert: func(t *testing.T, p *corev1.Pod) {
			assert.Equal(t, "bar", p.Labels["foo"])
			assert.Equal(t, "something", p.Annotations["baz"])
			assert.True(t, p.Spec.Containers[0].Resources.Limits["cpu"].Equal(resource.MustParse("9001")))
		},
	},
	{
		Name: "with synthesis env",
		Comp: func() *apiv1.Composition {
			comp := &apiv1.Composition{}
			comp.Name = "test-composition"
			comp.Namespace = "test-composition-ns"
			comp.Generation = 123
			comp.Status.CurrentSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
			comp.Spec.SynthesisEnv = []apiv1.EnvVar{{Name: "some_env", Value: "some-val"}}
			return comp
		}(),
		Assert: func(t *testing.T, p *corev1.Pod) {
			assert.Contains(t, p.Spec.Containers[0].Env, corev1.EnvVar{Name: "some_env", Value: "some-val"})
		},
	},
	{
		Name: "core variables are not stomped by synthesis env",
		Comp: func() *apiv1.Composition {
			comp := &apiv1.Composition{}
			comp.Name = "test-composition"
			comp.Namespace = "test-composition-ns"
			comp.Generation = 123
			comp.Status.CurrentSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
			comp.Spec.SynthesisEnv = []apiv1.EnvVar{
				{Name: "some_env", Value: "some-val"},
				{Name: "COMPOSITION_NAME", Value: "some-comp"},
			}
			return comp
		}(),
		Assert: func(t *testing.T, p *corev1.Pod) {
			assert.Contains(t, p.Spec.Containers[0].Env, corev1.EnvVar{Name: "some_env", Value: "some-val"})
			assert.Contains(t, p.Spec.Containers[0].Env, corev1.EnvVar{Name: "COMPOSITION_NAME", Value: "test-composition"})
		},
	},
}

func TestNewPod(t *testing.T) {
	for _, tc := range newPodTests {
		if tc.Cfg == nil {
			tc.Cfg = minimalTestConfig
		}

		if tc.Comp == nil {
			comp := &apiv1.Composition{}
			comp.Name = "test-composition"
			comp.Namespace = "test-composition-ns"
			comp.Generation = 123
			comp.Status.CurrentSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
			tc.Comp = comp
		}

		syn := &apiv1.Synthesizer{}
		if tc.Synth != nil {
			syn = tc.Synth
		}
		syn.Name = "test-synth"
		syn.Generation = 234

		t.Run(tc.Name, func(t *testing.T) {
			pod := newPod(tc.Cfg, tc.Comp, syn)
			tc.Assert(t, pod)
		})
	}
}
