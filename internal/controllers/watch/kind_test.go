package watch

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestBuildRequests(t *testing.T) {
	tests := []struct {
		name     string
		synth    *apiv1.Synthesizer
		comps    []apiv1.Composition
		expected []reconcile.Request
	}{
		{
			name: "no refs or bindings",
			synth: &apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{},
				},
			},
			comps:    []apiv1.Composition{},
			expected: []reconcile.Request{},
		},
		{
			name: "refs with no resource name",
			synth: &apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "key1", Resource: apiv1.ResourceRef{}},
					},
				},
			},
			comps: []apiv1.Composition{
				{
					Spec: apiv1.CompositionSpec{
						Bindings: []apiv1.Binding{
							{Key: "key1", Resource: apiv1.ResourceBinding{}},
						},
					},
				},
			},
			expected: []reconcile.Request{{}},
		},
		{
			name: "refs with resource name and no duplicate requests",
			synth: &apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{
							Key: "key1",
							Resource: apiv1.ResourceRef{
								Name:      "resource1",
								Namespace: "namespace1",
							},
						},
					},
				},
			},
			comps: []apiv1.Composition{
				{
					Spec: apiv1.CompositionSpec{
						Bindings: []apiv1.Binding{
							{
								Key: "key1",
								Resource: apiv1.ResourceBinding{
									Name:      "resource1",
									Namespace: "namespace1",
								},
							},
						},
					},
				},
			},
			expected: []reconcile.Request{
				{NamespacedName: types.NamespacedName{Namespace: "namespace1", Name: "resource1"}},
			},
		},
		{
			name: "multiple refs and bindings with duplicates",
			synth: &apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{
							Key: "key1",
							Resource: apiv1.ResourceRef{
								Name:      "resource1",
								Namespace: "namespace1",
							},
						},
						{
							Key: "key2",
							Resource: apiv1.ResourceRef{
								Name:      "resource2",
								Namespace: "namespace2",
							},
						},
					},
				},
			},
			comps: []apiv1.Composition{
				{
					Spec: apiv1.CompositionSpec{
						Bindings: []apiv1.Binding{
							{
								Key: "key1",
								Resource: apiv1.ResourceBinding{
									Name:      "resource1",
									Namespace: "namespace1",
								},
							},
							{
								Key: "key2",
								Resource: apiv1.ResourceBinding{
									Name:      "resource2",
									Namespace: "namespace2",
								},
							},
						},
					},
				},
				{
					Spec: apiv1.CompositionSpec{
						Bindings: []apiv1.Binding{
							{
								Key: "key1",
								Resource: apiv1.ResourceBinding{
									Name:      "resource3",
									Namespace: "namespace1",
								},
							},
						},
					},
				},
			},
			expected: []reconcile.Request{
				{NamespacedName: types.NamespacedName{Namespace: "namespace1", Name: "resource1"}},
				{NamespacedName: types.NamespacedName{Namespace: "namespace2", Name: "resource2"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := &KindWatchController{}
			result := k.buildRequests(tt.synth, tt.comps...)
			assert.ElementsMatch(t, tt.expected, result)
		})
	}
}

func TestIsOptionalRef(t *testing.T) {
	tests := []struct {
		name     string
		synth    *apiv1.Synthesizer
		key      string
		expected bool
	}{
		{
			name: "optional ref returns true",
			synth: &apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "required", Optional: false},
						{Key: "optional", Optional: true},
					},
				},
			},
			key:      "optional",
			expected: true,
		},
		{
			name: "required ref returns false",
			synth: &apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "required", Optional: false},
						{Key: "optional", Optional: true},
					},
				},
			},
			key:      "required",
			expected: false,
		},
		{
			name: "non-existent key returns false",
			synth: &apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "required", Optional: false},
					},
				},
			},
			key:      "nonexistent",
			expected: false,
		},
		{
			name: "ref without optional field defaults to false",
			synth: &apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "default"},
					},
				},
			},
			key:      "default",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isOptionalRef(tt.synth, tt.key)
			assert.Equal(t, tt.expected, result)
		})
	}
}
