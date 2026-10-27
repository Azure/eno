package watch

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestIsInputBound(t *testing.T) {
	tests := []struct {
		name     string
		refs     []apiv1.Ref
		bindings []apiv1.Binding
		key      string
		expected bool
	}{
		{
			name:     "ref with explicit binding",
			refs:     []apiv1.Ref{{Key: "foo"}},
			bindings: []apiv1.Binding{{Key: "foo"}},
			key:      "foo",
			expected: true,
		},
		{
			name:     "ref with implicit binding",
			refs:     []apiv1.Ref{{Key: "foo", Resource: apiv1.ResourceRef{Name: "input"}}},
			key:      "foo",
			expected: true,
		},
		{
			name:     "ref without any binding",
			refs:     []apiv1.Ref{{Key: "foo"}},
			key:      "foo",
			expected: false,
		},
		{
			name:     "binding outlives removed ref",
			refs:     []apiv1.Ref{{Key: "bar"}},
			bindings: []apiv1.Binding{{Key: "foo"}, {Key: "bar"}},
			key:      "foo",
			expected: false,
		},
		{
			name:     "synthesizer without refs",
			bindings: []apiv1.Binding{{Key: "foo"}},
			key:      "foo",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comp := &apiv1.Composition{}
			comp.Spec.Bindings = tt.bindings
			synth := &apiv1.Synthesizer{}
			synth.Spec.Refs = tt.refs

			assert.Equal(t, tt.expected, isInputBound(comp, synth, tt.key))
		})
	}
}

func TestPruningControllerReconcile(t *testing.T) {
	newSynth := func() *apiv1.Synthesizer {
		synth := &apiv1.Synthesizer{}
		synth.Name = "test-synth"
		synth.Spec.Refs = []apiv1.Ref{{Key: "keep"}}
		return synth
	}
	newComp := func() *apiv1.Composition {
		comp := &apiv1.Composition{}
		comp.Name = "test-comp"
		comp.Namespace = "default"
		comp.Spec.Synthesizer.Name = "test-synth"
		comp.Spec.Bindings = []apiv1.Binding{{Key: "keep"}, {Key: "removed-1"}, {Key: "removed-2"}}
		comp.Status.InputRevisions = []apiv1.InputRevisions{
			{Key: "removed-1", ResourceVersion: "1"},
			{Key: "keep", ResourceVersion: "2"},
			{Key: "removed-2", ResourceVersion: "3"},
		}
		return comp
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-comp", Namespace: "default"}}

	t.Run("prunes all undeclared refs in one pass", func(t *testing.T) {
		ctx := testutil.NewContext(t)
		cli := testutil.NewClient(t, newSynth(), newComp())
		c := &pruningController{client: cli}

		_, err := c.Reconcile(ctx, req)
		require.NoError(t, err)

		comp := &apiv1.Composition{}
		require.NoError(t, cli.Get(ctx, req.NamespacedName, comp))
		assert.Equal(t, []apiv1.InputRevisions{{Key: "keep", ResourceVersion: "2"}}, comp.Status.InputRevisions)
	})

	t.Run("synthesizer missing", func(t *testing.T) {
		ctx := testutil.NewContext(t)
		cli := testutil.NewClient(t, newComp())
		c := &pruningController{client: cli}

		_, err := c.Reconcile(ctx, req)
		require.NoError(t, err)

		comp := &apiv1.Composition{}
		require.NoError(t, cli.Get(ctx, req.NamespacedName, comp))
		assert.Len(t, comp.Status.InputRevisions, 3)
	})

	t.Run("nothing to prune", func(t *testing.T) {
		ctx := testutil.NewContext(t)
		comp := newComp()
		comp.Status.InputRevisions = comp.Status.InputRevisions[1:2]
		cli := testutil.NewClient(t, newSynth(), comp)
		c := &pruningController{client: cli}

		before := &apiv1.Composition{}
		require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), before))

		_, err := c.Reconcile(ctx, req)
		require.NoError(t, err)

		after := &apiv1.Composition{}
		require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), after))
		assert.Equal(t, before.ResourceVersion, after.ResourceVersion)
	})
}
