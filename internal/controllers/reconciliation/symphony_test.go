package reconciliation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/kubectl/pkg/scheme"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
)

// TestSymphonyIntegration proves that a basic symphony creation/deletion workflow works.
func TestSymphonyIntegration(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t, testutil.WithCompositionNamespace(ctrlcache.AllNamespaces))
	upstream := mgr.GetClient()

	// Create test namespace.
	require.NoError(t, upstream.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test"}}))

	registerControllers(t, mgr)
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

// TestOrphanedNamespaceSymphony covers a special behavior of symphonies: resilience against missing namespaces.
// Ripping the namespace out from under a symphony and its associated compositions/resource slices shouldn't prevent them from being deleted.
func TestOrphanedNamespaceSymphony(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t, testutil.WithCompositionNamespace(ctrlcache.AllNamespaces))
	upstream := mgr.GetClient()

	// Create test namespace.
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test"}}
	require.NoError(t, upstream.Create(ctx, ns))

	registerControllers(t, mgr)
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

	symph := &apiv1.Symphony{}
	symph.Name = "test-comp"
	symph.Namespace = ns.Name
	symph.Spec.Variations = []apiv1.Variation{{Synthesizer: apiv1.SynthesizerRef{Name: syn.Name}}}
	require.NoError(t, upstream.Create(ctx, symph))

	testutil.Eventually(t, func() bool {
		upstream.Get(ctx, client.ObjectKeyFromObject(symph), symph)
		return symph.Status.Reconciled != nil && symph.Status.ObservedGeneration == symph.Generation
	})

	// Establish that resource slices did at one point exist
	slices := &apiv1.ResourceSliceList{}
	require.NoError(t, upstream.List(ctx, slices))
	require.True(t, len(slices.Items) > 0)

	// Force delete the namespace
	require.NoError(t, upstream.Delete(ctx, ns))

	conf := rest.CopyConfig(mgr.RestConfig)
	conf.GroupVersion = &schema.GroupVersion{Version: "v1"}
	conf.NegotiatedSerializer = serializer.WithoutConversionCodecFactory{CodecFactory: scheme.Codecs}
	rc, err := rest.RESTClientFor(conf)
	require.NoError(t, err)

	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(ns), ns)
		ns.Spec.Finalizers = nil

		_, err = rc.Put().
			AbsPath("/api/v1/namespaces", ns.Name, "/finalize").
			Body(ns).
			Do(ctx).Raw()
		return err
	})
	require.NoError(t, err)

	// The symphony and its associated resources should eventually be cleaned up
	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(upstream.Get(ctx, client.ObjectKeyFromObject(symph), symph))
	})
	require.NoError(t, upstream.List(ctx, slices))
	require.Empty(t, slices.Items)
}
