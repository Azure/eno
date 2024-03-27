package synthesis

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/require"
)

// TODO: Add an integration test that verifies input usage end to end.
func TestBuildPodInput(t *testing.T) {
	tcs := []struct {
		name        string
		comp        apiv1.Composition
		synth       apiv1.Synthesizer
		expected    string
		expectedErr string
	}{
		{
			name:     "no inputs",
			expected: "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[]}",
		},
		{
			name: "unbound ref",
			synth: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "in", Resource: apiv1.ResourceRef{Kind: "ConfigMap"}},
					},
				},
			},
			expectedErr: "referenced, but not bound",
		},
		{
			name: "valid",
			synth: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "in", Resource: apiv1.ResourceRef{Kind: "ConfigMap", Group: ""}},
					},
				},
			},
			comp: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{
						{Key: "in", Resource: apiv1.ResourceBinding{Name: "some-cm", Namespace: "default"}},
					},
				},
			},
			expected: "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"apiVersion\":\"eno.azure.io/v1\",\"key\":\"in\",\"kind\":\"Input\",\"resource\":{\"group\":\"\",\"kind\":\"ConfigMap\",\"name\":\"some-cm\",\"namespace\":\"default\"}}]}",
		},
		{
			name: "multiple",
			synth: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "cm", Resource: apiv1.ResourceRef{Kind: "ConfigMap", Group: ""}},
						{Key: "deploy", Resource: apiv1.ResourceRef{Kind: "Deployment", Group: "apps"}},
					},
				},
			},
			comp: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{
						{Key: "deploy", Resource: apiv1.ResourceBinding{Name: "some-deploy", Namespace: "default"}},
						{Key: "cm", Resource: apiv1.ResourceBinding{Name: "some-cm", Namespace: "some-ns"}},
					},
				},
			},
			expected: "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"apiVersion\":\"eno.azure.io/v1\",\"key\":\"cm\",\"kind\":\"Input\",\"resource\":{\"group\":\"\",\"kind\":\"ConfigMap\",\"name\":\"some-cm\",\"namespace\":\"some-ns\"}},{\"apiVersion\":\"eno.azure.io/v1\",\"key\":\"deploy\",\"kind\":\"Input\",\"resource\":{\"group\":\"apps\",\"kind\":\"Deployment\",\"name\":\"some-deploy\",\"namespace\":\"default\"}}]}",
		},
		{
			name: "non-referenced binding",
			synth: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "in", Resource: apiv1.ResourceRef{Kind: "ConfigMap", Group: "core"}},
					},
				},
			},
			comp: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{
						{Key: "in", Resource: apiv1.ResourceBinding{Name: "some-cm", Namespace: "default"}},
						{Key: "in2", Resource: apiv1.ResourceBinding{Name: "some-other-cm", Namespace: "other-ns"}}, // Safe to specify it, but won't be passed to the the Syhtesis Pod.
					},
				},
			},
			expected: "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"apiVersion\":\"eno.azure.io/v1\",\"key\":\"in\",\"kind\":\"Input\",\"resource\":{\"group\":\"core\",\"kind\":\"ConfigMap\",\"name\":\"some-cm\",\"namespace\":\"default\"}}]}",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			res, err := buildPodInput(&tc.comp, &tc.synth)
			if tc.expectedErr != "" {
				require.ErrorContains(t, err, tc.expectedErr)
				return
			}
			require.Equal(t, tc.expected, string(res))
		})
	}
}
