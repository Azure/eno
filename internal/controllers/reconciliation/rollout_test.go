package reconciliation

import (
	"context"
	"fmt"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBulkRollout(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
				},
				"data": map[string]string{"image": s.Spec.Image},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "create"
	require.NoError(t, upstream.Create(ctx, syn))

	// Create a bunch of compositions
	const n = 25
	for i := 0; i < n; i++ {
		comp := &apiv1.Composition{}
		comp.Name = fmt.Sprintf("test-comp-%d", i)
		comp.Namespace = "default"
		comp.Spec.Synthesizer.Name = syn.Name
		require.NoError(t, upstream.Create(ctx, comp))
	}

	testutil.Eventually(t, func() bool {
		for i := 0; i < n; i++ {
			comp := &apiv1.Composition{}
			comp.Name = fmt.Sprintf("test-comp-%d", i)
			comp.Namespace = "default"
			err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
			inSync := err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
			if !inSync {
				return false
			}
		}
		return true
	})

	// Update the synthesizer, prove all compositions converge
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		syn.Spec.Image = "updated"
		return upstream.Update(ctx, syn)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		for i := 0; i < n; i++ {
			comp := &apiv1.Composition{}
			comp.Name = fmt.Sprintf("test-comp-%d", i)
			comp.Namespace = "default"
			err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
			inSync := err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
			if !inSync {
				return false
			}
		}
		return true
	})
}
