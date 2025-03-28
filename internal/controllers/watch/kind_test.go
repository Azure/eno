package watch

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestSetInputRevisions(t *testing.T) {
	tests := []struct {
		name      string
		comp      *apiv1.Composition
		revs      *apiv1.InputRevisions
		expected  bool
		finalRevs []apiv1.InputRevisions
	}{
		{
			name: "add new revision when key is not found",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "rev1", Revision: ptr.To(1)},
					},
				},
			},
			revs: &apiv1.InputRevisions{
				Key:      "rev2",
				Revision: ptr.To(2),
			},
			expected: true,
			finalRevs: []apiv1.InputRevisions{
				{Key: "rev1", Revision: ptr.To(1)},
				{Key: "rev2", Revision: ptr.To(2)},
			},
		},
		{
			name: "update existing revision",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "rev1", Revision: ptr.To(1)},
					},
				},
			},
			revs: &apiv1.InputRevisions{
				Key:      "rev1",
				Revision: ptr.To(2),
			},
			expected: true,
			finalRevs: []apiv1.InputRevisions{
				{Key: "rev1", Revision: ptr.To(2)},
			},
		},
		{
			name: "no update if revision is identical",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "rev1", Revision: ptr.To(1)},
					},
				},
			},
			revs: &apiv1.InputRevisions{
				Key:      "rev1",
				Revision: ptr.To(1),
			},
			expected: false,
			finalRevs: []apiv1.InputRevisions{
				{Key: "rev1", Revision: ptr.To(1)},
			},
		},
		{
			name: "no update if revision is identical and synth generation is set",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "rev1", Revision: ptr.To(1), SynthesizerGeneration: ptr.To(int64(3))},
					},
				},
			},
			revs: &apiv1.InputRevisions{
				Key:                   "rev1",
				Revision:              ptr.To(1),
				SynthesizerGeneration: ptr.To(int64(3)),
			},
			expected: false,
			finalRevs: []apiv1.InputRevisions{
				{Key: "rev1", Revision: ptr.To(1), SynthesizerGeneration: ptr.To(int64(3))},
			},
		},
		{
			name: "update if revision is identical but synth generation is not",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "rev1", Revision: ptr.To(1), SynthesizerGeneration: ptr.To(int64(3))},
					},
				},
			},
			revs: &apiv1.InputRevisions{
				Key:                   "rev1",
				Revision:              ptr.To(1),
				SynthesizerGeneration: ptr.To(int64(5)),
			},
			expected: true,
			finalRevs: []apiv1.InputRevisions{
				{Key: "rev1", Revision: ptr.To(1), SynthesizerGeneration: ptr.To(int64(5))},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := setInputRevisions(tt.comp, tt.revs)
			assert.Equal(t, tt.expected, result)
			assert.Equal(t, tt.finalRevs, tt.comp.Status.InputRevisions)
		})
	}
}

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
