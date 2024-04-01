package replication

import (
	"reflect"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

	// Update the bindings and prove the new bindings are replicated to the compositions
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(sym), sym)
		sym.Spec.Bindings = []apiv1.Binding{{Key: "new-binding", Resource: apiv1.ResourceBinding{Name: "foo"}}}
		return cli.Update(ctx, sym)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		comps := &apiv1.CompositionList{}
		err := cli.List(ctx, comps)
		if err != nil && len(comps.Items) < 2 {
			return false
		}
		for _, comp := range comps.Items {
			if !reflect.DeepEqual(sym.Spec.Bindings, comp.Spec.Bindings) {
				t.Logf("composition %q has incorrect bindings", comp.Name)
				return false
			}
		}
		return true
	})

	// Test deletion
	require.NoError(t, cli.Delete(ctx, sym))
	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(sym), sym))
	})

	comps := &apiv1.CompositionList{}
	err = cli.List(ctx, comps)
	require.NoError(t, err)
	assert.Len(t, comps.Items, 0)
}

// TestSymphonyDuplicateCleanup proves that the newest compositions are deleted if multiple exist for a given synthesizer.
func TestSymphonyDuplicateCleanup(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	s := &symphonyController{client: cli}

	sym := &apiv1.Symphony{}
	sym.Name = "test"
	sym.Namespace = "default"
	sym.Spec.Synthesizers = []apiv1.SynthesizerRef{{Name: "foo"}}
	require.NoError(t, cli.Create(ctx, sym))

	comp := apiv1.Composition{}
	now := metav1.Now()
	comp.CreationTimestamp = metav1.NewTime(now.Add(time.Second))
	comp.Name = "foo"

	comp2 := apiv1.Composition{}
	comp2.CreationTimestamp = now
	comp2.Name = "bar"

	comps := &apiv1.CompositionList{Items: []apiv1.Composition{comp, comp2}}
	_, _, err := s.reconcileReverse(ctx, sym, comps)
	require.EqualError(t, err, `deleting duplicate composition: compositions.eno.azure.io "bar" not found`)
}
