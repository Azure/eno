package watch

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestShouldCauseResynthesis(t *testing.T) {
	tests := []struct {
		Name      string
		LastKnown []apiv1.InputRevisions
		Current   apiv1.InputRevisions
		Expected  bool
	}{
		{
			Name: "matching rv",
			LastKnown: []apiv1.InputRevisions{{
				Key:             "foo",
				ResourceVersion: "1",
			}},
			Current: apiv1.InputRevisions{
				Key:             "foo",
				ResourceVersion: "1",
			},
			Expected: false,
		},
		{
			Name: "mismatch in another input",
			LastKnown: []apiv1.InputRevisions{{
				Key:             "bar",
				ResourceVersion: "2",
			}, {
				Key:             "foo",
				ResourceVersion: "1",
			}},
			Current: apiv1.InputRevisions{
				Key:             "foo",
				ResourceVersion: "1",
			},
			Expected: false,
		},
		{
			Name: "match in another input",
			LastKnown: []apiv1.InputRevisions{{
				Key:             "bar",
				ResourceVersion: "1",
			}, {
				Key:             "foo",
				ResourceVersion: "2",
			}},
			Current: apiv1.InputRevisions{
				Key:             "foo",
				ResourceVersion: "1",
			},
			Expected: true,
		},
		{
			Name: "mismatched rv",
			LastKnown: []apiv1.InputRevisions{{
				Key:             "foo",
				ResourceVersion: "1",
			}},
			Current: apiv1.InputRevisions{
				Key:             "foo",
				ResourceVersion: "2",
			},
			Expected: true,
		},
		{
			Name: "only previous has revision",
			LastKnown: []apiv1.InputRevisions{{
				Key:             "foo",
				ResourceVersion: "1",
				Revision:        ptr.To(123),
			}},
			Current: apiv1.InputRevisions{
				Key:             "foo",
				ResourceVersion: "1",
			},
			Expected: false,
		},
		{
			Name: "only current has revision",
			LastKnown: []apiv1.InputRevisions{{
				Key:             "foo",
				ResourceVersion: "1",
			}},
			Current: apiv1.InputRevisions{
				Key:             "foo",
				ResourceVersion: "1",
				Revision:        ptr.To(123),
			},
			Expected: true,
		},
		{
			Name: "matching revisions",
			LastKnown: []apiv1.InputRevisions{{
				Key:             "foo",
				ResourceVersion: "1",
				Revision:        ptr.To(123),
			}},
			Current: apiv1.InputRevisions{
				Key:             "foo",
				ResourceVersion: "1",
				Revision:        ptr.To(123),
			},
			Expected: false,
		},
		{
			Name: "matching revisions, non matching rvs",
			LastKnown: []apiv1.InputRevisions{{
				Key:             "foo",
				ResourceVersion: "1",
				Revision:        ptr.To(123),
			}},
			Current: apiv1.InputRevisions{
				Key:             "foo",
				ResourceVersion: "2",
				Revision:        ptr.To(123),
			},
			Expected: false,
		},
		{
			Name: "non-matching revisions",
			LastKnown: []apiv1.InputRevisions{{
				Key:             "foo",
				ResourceVersion: "1",
				Revision:        ptr.To(123),
			}},
			Current: apiv1.InputRevisions{
				Key:             "foo",
				ResourceVersion: "1",
				Revision:        ptr.To(234),
			},
			Expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			comp := &apiv1.Composition{}
			comp.Status.CurrentSynthesis = &apiv1.Synthesis{InputRevisions: tc.LastKnown, Synthesized: ptr.To(metav1.Now())}
			assert.Equal(t, tc.Expected, shouldCauseResynthesis(comp, &tc.Current))
		})
	}
}
