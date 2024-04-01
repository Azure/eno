package replication

import (
	"reflect"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSymphonyCRUD(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()
	err := NewCompositionSetController(mgr.Manager)
	require.NoError(t, err)
	mgr.Start(t)

	// Create the symphony
	sym := &apiv1.Symphony{}
	sym.Name = "test-symphony"
	sym.Namespace = "default"
	sym.Spec.Bindings = []apiv1.Binding{
		{
			Key:      "foo",
			Resource: apiv1.ResourceBinding{Name: "test-resource-1"},
		},
		{
			Key:      "bar",
			Resource: apiv1.ResourceBinding{Name: "test-resource-2"},
		},
	}
	sym.Spec.Synthesizers = []apiv1.SynthesizerRef{{Name: "foosynth"}, {Name: "barsynth"}}
	err = cli.Create(ctx, sym)
	require.NoError(t, err)

	// Exactly one composition should eventually be created for each synth
	testutil.Eventually(t, func() bool {
		comps := &apiv1.CompositionList{}
		err := cli.List(ctx, comps)
		if err != nil && len(comps.Items) < 2 {
			return false
		}
		synthsSeen := map[string]struct{}{}
		for _, comp := range comps.Items {
			comp := comp
			if !reflect.DeepEqual(sym.Spec.Bindings, comp.Spec.Bindings) {
				t.Logf("composition %q has incorrect bindings", comp.Name)
				return false
			}
			synthsSeen[comp.Spec.Synthesizer.Name] = struct{}{}
		}
		if len(synthsSeen) > 2 {
			t.Logf("wrong number of synths seen: %d", len(synthsSeen))
			return false
		}
		for _, syn := range sym.Spec.Synthesizers {
			if _, ok := synthsSeen[syn.Name]; !ok {
				t.Logf("didn't see composition for synth %q", syn.Name)
				return false
			}
		}
		return true
	})
}
