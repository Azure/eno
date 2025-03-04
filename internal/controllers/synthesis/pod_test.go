package synthesis

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
			comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
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
			comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
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
	{
		Name: "With affinity overrides",
		Assert: func(t *testing.T, p *corev1.Pod) {
			// assert that node affinity is set, and node affinity has both what is hardcoded and what is in the synth
			// basically this is just testing that the merge appends arrays as necessary
			require.NotNil(t, p.Spec.Affinity.NodeAffinity)
			assert.Equal(t, 2, len(p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms))
			assert.Equal(t, corev1.NodeSelectorOpIn, p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Operator)
			assert.Equal(t, "foo", p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Key)
			assert.Equal(t, "bar", p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Values[0])

			assert.Equal(t, corev1.NodeSelectorOpIn, p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[1].MatchExpressions[0].Operator)
			assert.Equal(t, []string{"testvalue"}, p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[1].MatchExpressions[0].Values)

			require.NotNil(t, p.Spec.Affinity.PodAffinity)
			assert.Equal(t, "kubernetes.io/hostname", p.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].TopologyKey)
			require.NotNil(t, p.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].LabelSelector)
			assert.Equal(t, "testpodaffinitylabelvalue", p.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].LabelSelector.MatchLabels["testpodaffinitylabelkey"])

			require.NotNil(t, p.Spec.Affinity.PodAntiAffinity)
			assert.Equal(t, 2, len(p.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution))

			assert.Equal(t, int32(100), p.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].Weight)
			assert.Equal(t, "kubernetes.io/hostname", p.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].TopologyKey)
			require.NotNil(t, p.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm.LabelSelector)
			assert.Equal(t, manager.ManagerLabelValue, p.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm.LabelSelector.MatchLabels[manager.ManagerLabelKey])

			assert.Equal(t, int32(100), p.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[1].Weight)
			assert.Equal(t, "kubernetes.io/hostname", p.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[1].PodAffinityTerm.TopologyKey)
			require.NotNil(t, p.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[1].PodAffinityTerm.LabelSelector)
			assert.Equal(t, "testpodantiaffinitylabelvalue", p.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[1].PodAffinityTerm.LabelSelector.MatchLabels["testpodantiaffinitylabelkey"])
		},
		Cfg: &Config{
			NodeAffinityKey:   "foo",
			NodeAffinityValue: "bar",
		},
		Synth: &apiv1.Synthesizer{
			Spec: apiv1.SynthesizerSpec{
				PodOverrides: apiv1.PodOverrides{
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "testkey",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"testvalue"},
											},
										},
									},
								},
							},
						},
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									TopologyKey: "kubernetes.io/hostname",
									LabelSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"testpodaffinitylabelkey": "testpodaffinitylabelvalue",
										},
									},
								},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
								{
									Weight: 100,
									PodAffinityTerm: corev1.PodAffinityTerm{
										TopologyKey: "kubernetes.io/hostname",
										LabelSelector: &metav1.LabelSelector{
											MatchLabels: map[string]string{
												"testpodantiaffinitylabelkey": "testpodantiaffinitylabelvalue",
											},
										},
									},
								},
							},
						},
					},
				},
			},
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
			comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
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
