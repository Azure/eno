package scheduling

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestNewOp(t *testing.T) {
	// TODO: Maybe cover deferred inputs here?
	tests := []struct {
		Name        string
		Expectation bool
		Composition apiv1.Composition
		Reason      string
	}{
		{
			Name:        "zero value",
			Expectation: true,
			Reason:      "InitialSynthesis",
		},
		{
			Name:        "missing input",
			Expectation: false,
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}},
				},
			},
			Reason: "",
		},
		{
			Name:        "matching input synthesis in progress",
			Expectation: false,
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						InputRevisions: []apiv1.InputRevisions{{
							Key: "foo",
						}},
					},
					InputRevisions: []apiv1.InputRevisions{{
						Key: "foo",
					}},
				},
			},
			Reason: "",
		},
		{
			Name:        "non-matching composition generation",
			Expectation: true,
			Composition: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 234,
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						ObservedCompositionGeneration: 123,
					},
				},
			},
			Reason: "CompositionModified",
		},
		{
			Name:        "matching input synthesis terminal",
			Expectation: false,
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						InputRevisions: []apiv1.InputRevisions{{
							Key: "foo",
						}},
						Synthesized: ptr.To(metav1.Now()),
					},
					InputRevisions: []apiv1.InputRevisions{{
						Key: "foo",
					}},
				},
			},
			Reason: "",
		},
		{
			Name:        "non-matching input synthesis terminal",
			Expectation: true,
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						InputRevisions: []apiv1.InputRevisions{{
							Key: "foo",
						}},
						Synthesized: ptr.To(metav1.Now()),
					},
					InputRevisions: []apiv1.InputRevisions{{
						Key:             "foo",
						ResourceVersion: "new",
					}},
				},
			},
			Reason: "InputModified",
		},
		{
			Name:        "non-matching input synthesis terminal ignore side effects",
			Expectation: false,
			Composition: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"eno.azure.io/ignore-side-effects": "true",
					},
				},
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						InputRevisions: []apiv1.InputRevisions{{
							Key: "foo",
						}},
						Synthesized: ptr.To(metav1.Now()),
					},
					InputRevisions: []apiv1.InputRevisions{{
						Key:             "foo",
						ResourceVersion: "new",
					}},
				},
			},
			Reason: "",
		},
		{
			Name:        "non-matching input synthesis non-terminal",
			Expectation: false,
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						InputRevisions: []apiv1.InputRevisions{{
							Key: "foo",
						}},
						// Synthesized: ptr.To(metav1.Now()),
					},
					InputRevisions: []apiv1.InputRevisions{{
						Key:             "foo",
						ResourceVersion: "new",
					}},
				},
			},
			Reason: "",
		},
		{
			Name:        "non-matching input synthesis deleting",
			Expectation: true,
			Composition: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: ptr.To(metav1.Now()),
					Generation:        2,
				},
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						InputRevisions: []apiv1.InputRevisions{{
							Key: "foo",
						}},
					},
					InputRevisions: []apiv1.InputRevisions{{
						Key:             "foo",
						ResourceVersion: "new",
					}},
				},
			},
			Reason: "CompositionModified",
		},
		{
			Name:        "missing input synthesis deleting",
			Expectation: true,
			Composition: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: ptr.To(metav1.Now()),
					Generation:        2,
				},
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}},
				},
			},
			Reason: "InitialSynthesis",
		},
		{
			Name:        "revision mismatch",
			Expectation: false,
			Composition: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 1,
				},
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}, {Key: "bar"}},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{},
					InputRevisions: []apiv1.InputRevisions{{
						Key:             "foo",
						ResourceVersion: "new",
						Revision:        ptr.To(123),
					}, {
						Key:             "bar",
						ResourceVersion: "another",
						Revision:        ptr.To(234),
					}},
				},
			},
			Reason: "",
		},
		{
			Name:        "revision match",
			Expectation: true,
			Composition: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 1,
				},
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}, {Key: "bar"}},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{},
					InputRevisions: []apiv1.InputRevisions{{
						Key:             "foo",
						ResourceVersion: "new",
						Revision:        ptr.To(123),
					}, {
						Key:             "bar",
						ResourceVersion: "another",
						Revision:        ptr.To(123),
					}},
				},
			},
			Reason: "CompositionModified",
		},
	}

	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			syn := &apiv1.Synthesizer{}
			syn.Spec.Refs = []apiv1.Ref{{Key: "foo"}}

			op := newOp(syn, &tc.Composition, 0, time.Time{})
			assert.Equal(t, tc.Expectation, op != nil)

			if tc.Reason != "" {
				require.NotNil(t, op)
				assert.Equal(t, tc.Reason, op.Reason)
			}
		})
	}
}

func TestInputRevisionsEqual(t *testing.T) {
	synth := &apiv1.Synthesizer{}
	synth.Spec.Refs = []apiv1.Ref{{Key: "foo"}, {Key: "bar", Defer: true}, {Key: "baz"}}

	tcs := []struct {
		name                 string
		a, b                 []apiv1.InputRevisions
		equal, deferredEqual bool
	}{
		{
			name:          "just keys",
			a:             []apiv1.InputRevisions{{Key: "foo"}, {Key: "baz"}},
			b:             []apiv1.InputRevisions{{Key: "foo"}, {Key: "baz"}},
			equal:         true,
			deferredEqual: true,
		},
		{
			name:          "resource version mismatch",
			a:             []apiv1.InputRevisions{{Key: "foo"}, {Key: "baz"}},
			b:             []apiv1.InputRevisions{{Key: "foo", ResourceVersion: "not-zero"}, {Key: "baz"}},
			deferredEqual: true,
		},
		{
			name:          "revision missong",
			a:             []apiv1.InputRevisions{{Key: "foo"}, {Key: "baz"}},
			b:             []apiv1.InputRevisions{{Key: "foo", Revision: ptr.To(123)}, {Key: "baz"}},
			deferredEqual: true,
		},
		{
			name:          "revision mismatch",
			a:             []apiv1.InputRevisions{{Key: "foo", Revision: ptr.To(234)}, {Key: "baz"}},
			b:             []apiv1.InputRevisions{{Key: "foo", Revision: ptr.To(123)}, {Key: "baz"}},
			deferredEqual: true,
		},
		{
			name:          "revision match",
			a:             []apiv1.InputRevisions{{Key: "foo", Revision: ptr.To(123)}, {Key: "baz"}},
			b:             []apiv1.InputRevisions{{Key: "foo", Revision: ptr.To(123)}, {Key: "baz"}},
			equal:         true,
			deferredEqual: true,
		},
		{
			name:          "resource version match",
			a:             []apiv1.InputRevisions{{Key: "foo", ResourceVersion: "not-zero"}, {Key: "baz"}},
			b:             []apiv1.InputRevisions{{Key: "foo", ResourceVersion: "not-zero"}, {Key: "baz"}},
			equal:         true,
			deferredEqual: true,
		},
		{
			name:          "mixed resource version and revision",
			a:             []apiv1.InputRevisions{{Key: "foo", ResourceVersion: "not-zero"}, {Key: "baz"}},
			b:             []apiv1.InputRevisions{{Key: "foo", Revision: ptr.To(123)}, {Key: "baz"}},
			deferredEqual: true,
		},
		{
			name:          "deferred",
			a:             []apiv1.InputRevisions{{Key: "foo"}, {Key: "bar"}, {Key: "baz"}},
			b:             []apiv1.InputRevisions{{Key: "foo"}, {Key: "bar", ResourceVersion: "not-zero"}, {Key: "baz"}},
			equal:         true,
			deferredEqual: false,
		},
		{
			name:          "deferred equal",
			a:             []apiv1.InputRevisions{{Key: "foo"}, {Key: "bar", ResourceVersion: "not-zero"}, {Key: "baz"}},
			b:             []apiv1.InputRevisions{{Key: "foo"}, {Key: "bar", ResourceVersion: "not-zero"}, {Key: "baz"}},
			equal:         true,
			deferredEqual: true,
		},
		{
			name:          "mismatched items with deferred",
			a:             []apiv1.InputRevisions{{Key: "foo"}, {Key: "baz"}},
			b:             []apiv1.InputRevisions{{Key: "bar"}, {Key: "baz"}},
			deferredEqual: true,
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			eq, deferred := inputRevisionsEqual(synth, tc.a, tc.b)
			assert.Equal(t, tc.equal, eq)
			assert.Equal(t, tc.deferredEqual, deferred)
		})
	}

}

func TestInputRevisionsEqualOrdering(t *testing.T) {
	synth := &apiv1.Synthesizer{}
	synth.Spec.Refs = []apiv1.Ref{{Key: "foo"}, {Key: "bar"}}

	eq, _ := inputRevisionsEqual(synth, []apiv1.InputRevisions{
		{Key: "bar"}, {Key: "foo"},
	}, []apiv1.InputRevisions{
		{Key: "foo"}, {Key: "bar"},
	})

	assert.True(t, eq)
}
