package watch

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"
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
