package reconciliation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/aggregation"
	"github.com/Azure/eno/internal/controllers/replication"
	"github.com/Azure/eno/internal/controllers/rollout"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
)

// TestSymphonyIntegration proves that a basic symphony creation/deletion workflow works.
func TestSymphonyIntegration(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t, testutil.WithCompositionNamespace(ctrlcache.AllNamespaces))
	upstream := mgr.GetClient()

	// Create test namespace.
	require.NoError(t, upstream.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test"}}))

	// Register supporting controllers
	require.NoError(t, rollout.NewSynthesizerController(mgr.Manager))
	require.NoError(t, rollout.NewController(mgr.Manager, time.Millisecond))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, defaultConf))
	require.NoError(t, replication.NewSymphonyController(mgr.Manager))
	require.NoError(t, aggregation.NewSymphonyController(mgr.Manager))
	require.NoError(t, aggregation.NewSliceController(mgr.Manager))
	require.NoError(t, synthesis.NewSliceCleanupController(mgr.Manager))
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]string{
					"name":      "test-obj",
					"namespace": "default",
				},
				"data": map[string]string{"foo": "bar"},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "anything"
	require.NoError(t, upstream.Create(ctx, syn))

	syn2 := &apiv1.Synthesizer{}
	syn2.Name = "test-syn-2"
	syn2.Spec.Image = "anything"
	require.NoError(t, upstream.Create(ctx, syn2))

	// Creation
	symph := &apiv1.Symphony{}
	symph.Name = "test-comp"
	symph.Namespace = "default"
	symph.Spec.Variations = []apiv1.Variation{{Synthesizer: apiv1.SynthesizerRef{Name: syn.Name}}}
	require.NoError(t, upstream.Create(ctx, symph))

	// TODO: Extract the boilerplate for this test and create a dedicated
	// scenario for ns isolation.
	// Creating a second symphony with the same name in a separate namespace
	// to ensure we handle namespace isolation correctly.
	symph2 := &apiv1.Symphony{}
	symph2.Name = "test-comp"
	symph2.Namespace = "test"
	symph2.Spec.Variations = []apiv1.Variation{{Synthesizer: apiv1.SynthesizerRef{Name: syn.Name}}}
	require.NoError(t, upstream.Create(ctx, symph2))

	testutil.Eventually(t, func() bool {
		upstream.Get(ctx, client.ObjectKeyFromObject(symph), symph)
		if symph.Status.Reconciled == nil || symph.Status.ObservedGeneration != symph.Generation {
			return false
		}

		comps := &apiv1.CompositionList{}
		upstream.List(ctx, comps, client.InNamespace(symph.Namespace))
		return len(comps.Items) == 1
	})

	testutil.Eventually(t, func() bool {
		upstream.Get(ctx, client.ObjectKeyFromObject(symph2), symph2)
		if symph.Status.Reconciled == nil || symph.Status.ObservedGeneration != symph.Generation {
			return false
		}

		comps := &apiv1.CompositionList{}
		upstream.List(ctx, comps, client.InNamespace(symph2.Namespace))
		return len(comps.Items) == 1
	})

	// Delete one of the symphonies
	require.NoError(t, upstream.Delete(ctx, symph2))
	testutil.Eventually(t, func() bool {
		comps := &apiv1.CompositionList{}
		upstream.List(ctx, comps)
		return len(comps.Items) == 1
	})

	// Add another variation
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(symph), symph)
		symph.Spec.Variations = append(symph.Spec.Variations, apiv1.Variation{
			Synthesizer: apiv1.SynthesizerRef{Name: syn2.Name},
		})
		return upstream.Update(ctx, symph)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		upstream.Get(ctx, client.ObjectKeyFromObject(symph), symph)
		return symph.Status.Reconciled != nil && symph.Status.ObservedGeneration == symph.Generation
	})

	// Remove a variation
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(symph), symph)
		symph.Spec.Variations = symph.Spec.Variations[:1]
		return upstream.Update(ctx, symph)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		current := &apiv1.Symphony{} // invalidate cache
		upstream.Get(ctx, client.ObjectKeyFromObject(symph), current)
		return current.Status.Reconciled != nil && current.Status.ObservedGeneration == current.Generation && len(current.Status.Synthesizers) == 1
	})

	comps := &apiv1.CompositionList{}
	err = upstream.List(ctx, comps)
	require.NoError(t, err)
	assert.Len(t, comps.Items, 1)

	// Deletion
	require.NoError(t, upstream.Delete(ctx, symph))
	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(upstream.Get(ctx, client.ObjectKeyFromObject(symph), symph))
	})

	comps = &apiv1.CompositionList{}
	err = upstream.List(ctx, comps)
	require.NoError(t, err)
	assert.Len(t, comps.Items, 0)
}
