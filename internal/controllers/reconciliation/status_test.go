package reconciliation

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/aggregation"
	testv1 "github.com/Azure/eno/internal/controllers/reconciliation/fixtures/v1"
	"github.com/Azure/eno/internal/controllers/rollout"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// TestResourceReadiness proves that resources supporting readiness checks eventually have their state
// mirrored into the resource slice.
func TestResourceReadiness(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	// Register supporting controllers
	require.NoError(t, rollout.NewController(mgr.Manager, time.Millisecond))
	require.NoError(t, synthesis.NewStatusController(mgr.Manager))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, defaultConf))
	require.NoError(t, aggregation.NewSliceController(mgr.Manager))
	require.NoError(t, synthesis.NewExecController(mgr.Manager, defaultConf, &testutil.ExecConn{Hook: func(s *apiv1.Synthesizer) []client.Object {
		obj := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-obj",
				Namespace: "default",
				Annotations: map[string]string{
					"eno.azure.io/readiness": "self.data.foo == 'baz'",
				},
			},
			Data: map[string]string{"foo": s.Spec.Image},
		}

		gvks, _, err := scheme.ObjectKinds(obj)
		require.NoError(t, err)
		obj.GetObjectKind().SetGroupVersionKind(gvks[0])
		return []client.Object{obj}
	}}))

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)

	// Any syn/comp will do since we faked out the synthesizer pod
	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "bar"
	require.NoError(t, upstream.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, upstream.Create(ctx, comp))

	// Wait for resource to be created
	obj := &corev1.ConfigMap{}
	var err error
	testutil.Eventually(t, func() bool {
		obj.SetName("test-obj")
		obj.SetNamespace("default")
		err = downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		return err == nil
	})

	// Initially readiness should be false
	testutil.Eventually(t, func() bool {
		slices, err := mgr.GetCurrentResourceSlices(ctx)
		if err != nil {
			t.Log(err)
			return false
		}
		return len(slices[0].Status.Resources) > 0 && isNotReady(slices[0].Status.Resources[0])
	})

	testutil.Eventually(t, func() bool {
		err = upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready == nil
	})

	// Update resource to meet readiness criteria
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		err = upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn)
		syn.Spec.Image = "baz"
		return upstream.Update(ctx, syn)
	})
	require.NoError(t, err)

	// The composition should also be updated
	testutil.Eventually(t, func() bool {
		err = upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil
	})

	// Update resource to not meet readiness criteria
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		err = upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn)
		syn.Spec.Image = "bar"
		return upstream.Update(ctx, syn)
	})
	require.NoError(t, err)

	// The composition status should revert back to not ready when re-synthesized
	testutil.Eventually(t, func() bool {
		err = upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready == nil
	})
}

// TestReconcileStatus proves that reconciliation and deletion status are written to resource slices as expected.
func TestReconcileStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	setupTestSubject(t, mgr)
	mgr.Start(t)

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	require.NoError(t, upstream.Create(ctx, comp))
	comp.ResourceVersion = "1"

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice"
	slice.Namespace = comp.Namespace
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, upstream.Scheme()))
	slice.Spec.Resources = []apiv1.Manifest{
		{Manifest: `{ "kind": "ConfigMap", "apiVersion": "v1", "metadata": { "name": "test", "namespace": "default" } }`},
		{Deleted: true, Manifest: `{ "kind": "ConfigMap", "apiVersion": "v1", "metadata": { "name": "test-deleted", "namespace": "default" } }`},
	}
	require.NoError(t, upstream.Create(ctx, slice))

	now := metav1.Now()
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.CurrentSynthesis = &apiv1.Synthesis{
			UUID:           uuid.NewString(),
			Synthesized:    &now,
			ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
		}
		return upstream.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	// Status should eventually reflect the reconciliation state
	testutil.Eventually(t, func() bool {
		err = upstream.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		return err == nil && len(slice.Status.Resources) == 2 &&
			slice.Status.Resources[0].Reconciled && !slice.Status.Resources[0].Deleted &&
			slice.Status.Resources[1].Reconciled && slice.Status.Resources[1].Deleted
	})
}

func isNotReady(state apiv1.ResourceState) bool { return state.Ready == nil }

// TestStatusNegative covers a weird corner case where a resource fails a patch but is still marked as reconciled.
func TestStatusNegative(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	obj := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-obj",
			Namespace: "default",
			Annotations: map[string]string{
				"eno.azure.io/reconcile-interval": "1ms",
			},
		},
		Data: map[string]string{"foo": "bar"},
	}

	// Register supporting controllers
	require.NoError(t, rollout.NewController(mgr.Manager, time.Millisecond))
	require.NoError(t, synthesis.NewStatusController(mgr.Manager))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, defaultConf))
	require.NoError(t, aggregation.NewSliceController(mgr.Manager))
	require.NoError(t, synthesis.NewExecController(mgr.Manager, defaultConf, &testutil.ExecConn{Hook: func(s *apiv1.Synthesizer) []client.Object {
		obj := obj.DeepCopy()
		obj.Data = map[string]string{"foo": "baz"} // update: bar->baz
		gvks, _, err := scheme.ObjectKinds(obj)
		require.NoError(t, err)
		obj.GetObjectKind().SetGroupVersionKind(gvks[0])
		return []client.Object{obj}
	}}))

	// Test subject
	c := setupTestSubject(t, mgr)
	c.upstreamClient = testutil.NewReadOnlyClient(t, obj)
	mgr.Start(t)

	// Any syn/comp will do since we faked out the synthesizer pod
	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "create"
	require.NoError(t, upstream.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, upstream.Create(ctx, comp))

	// Prove the resource was not marked as reconciled
	// This would be better as a unit test where we can block on reconciliation,
	// consider refactoring later (it will be non-trivial)
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil
	})
	time.Sleep(time.Second)
	require.NoError(t, upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Nil(t, comp.Status.CurrentSynthesis.Reconciled)
}
