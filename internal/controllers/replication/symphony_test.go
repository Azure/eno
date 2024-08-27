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
	err := NewSymphonyController(mgr.Manager)
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
	sym.Spec.Variations = []apiv1.Variation{
		{
			Synthesizer: apiv1.SynthesizerRef{Name: "foosynth"},
			Labels:      map[string]string{"foo": "bar"},
			Annotations: map[string]string{"foo": "bar"},
		},
		{
			Synthesizer: apiv1.SynthesizerRef{Name: "barsynth"},
			Labels:      map[string]string{"foo": "bar"},
			Annotations: map[string]string{"foo": "bar"},
		},
	}
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
			if !reflect.DeepEqual(sym.Spec.Bindings, comp.Spec.Bindings) ||
				!reflect.DeepEqual(comp.Annotations, map[string]string{"foo": "bar"}) ||
				!reflect.DeepEqual(comp.Labels, map[string]string{"foo": "bar"}) {
				t.Logf("composition %q has incorrect bindings/labels/annotations", comp.Name)
				return false
			}
			synthsSeen[comp.Spec.Synthesizer.Name] = struct{}{}
		}
		if len(synthsSeen) > 2 {
			t.Logf("wrong number of synths seen: %d", len(synthsSeen))
			return false
		}
		for _, v := range sym.Spec.Variations {
			if _, ok := synthsSeen[v.Synthesizer.Name]; !ok {
				t.Logf("didn't see composition for synth %q", v.Synthesizer.Name)
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

	// Update the labels and annotations and prove they're replicated to the compositions
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(sym), sym)
		for i := range sym.Spec.Variations {
			sym.Spec.Variations[i].Labels = map[string]string{"foo": "baz"}
			sym.Spec.Variations[i].Annotations = map[string]string{"foo": "baz"}
		}
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
			if comp.Labels == nil ||
				comp.Labels["foo"] != "baz" ||
				comp.Annotations == nil ||
				comp.Annotations["foo"] != "baz" {
				t.Logf("composition %q doesn't have the expected labels and annotations", comp.Name)
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
	sym.Spec.Variations = []apiv1.Variation{{Synthesizer: apiv1.SynthesizerRef{Name: "foo"}}}
	require.NoError(t, cli.Create(ctx, sym))

	comp := apiv1.Composition{}
	now := metav1.Now()
	comp.CreationTimestamp = metav1.NewTime(now.Add(time.Second))
	comp.Name = "foo"
	comp.Spec.Synthesizer.Name = "foo"

	comp2 := apiv1.Composition{}
	comp2.CreationTimestamp = now
	comp2.Name = "bar"
	comp2.Spec.Synthesizer.Name = "foo"

	comps := &apiv1.CompositionList{Items: []apiv1.Composition{comp, comp2}}
	_, _, err := s.reconcileReverse(ctx, sym, comps)
	require.EqualError(t, err, `deleting duplicate composition: compositions.eno.azure.io "bar" not found`)
}

func TestGetBindings(t *testing.T) {
	tcs := []struct {
		name             string
		symph            apiv1.Symphony
		variation        apiv1.Variation
		expectedBindings []apiv1.Binding
	}{
		{
			name: "just symphony bindings",
			symph: apiv1.Symphony{
				Spec: apiv1.SymphonySpec{
					Bindings: []apiv1.Binding{
						{Key: "bnd-1"},
					},
				},
			},
			expectedBindings: []apiv1.Binding{
				{Key: "bnd-1"},
			},
		},
		{
			name: "just variation bindings",
			variation: apiv1.Variation{
				Bindings: []apiv1.Binding{
					{Key: "bnd-1"},
				},
			},
			expectedBindings: []apiv1.Binding{
				{Key: "bnd-1"},
			},
		},
		{
			name: "symphony and variation bindings",
			variation: apiv1.Variation{
				Bindings: []apiv1.Binding{
					{Key: "bnd-1"},
				},
			},
			symph: apiv1.Symphony{
				Spec: apiv1.SymphonySpec{
					Bindings: []apiv1.Binding{
						{Key: "bnd-2"},
					},
				},
			},
			expectedBindings: []apiv1.Binding{
				{Key: "bnd-1"},
				{Key: "bnd-2"},
			},
		},
		{
			name: "symphony and variation bindings with dups",
			variation: apiv1.Variation{
				Bindings: []apiv1.Binding{
					{Key: "bnd-1"},
					{Key: "bnd-1"},
				},
			},
			symph: apiv1.Symphony{
				Spec: apiv1.SymphonySpec{
					Bindings: []apiv1.Binding{
						{Key: "bnd-2"},
						{Key: "bnd-2"},
					},
				},
			},
			expectedBindings: []apiv1.Binding{
				{Key: "bnd-1"},
				{Key: "bnd-2"},
			},
		},
		{
			name: "variation takes precedence over symphony",
			variation: apiv1.Variation{
				Bindings: []apiv1.Binding{
					{Key: "bnd-1", Resource: apiv1.ResourceBinding{Name: "from-variation"}},
				},
			},
			symph: apiv1.Symphony{
				Spec: apiv1.SymphonySpec{
					Bindings: []apiv1.Binding{
						{Key: "bnd-1", Resource: apiv1.ResourceBinding{Name: "from-symphony"}},
					},
				},
			},
			expectedBindings: []apiv1.Binding{
				{Key: "bnd-1", Resource: apiv1.ResourceBinding{Name: "from-variation"}},
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			actualBindings := getBindings(&tc.symph, &tc.variation)
			require.ElementsMatch(t, tc.expectedBindings, actualBindings)
		})
	}
}
