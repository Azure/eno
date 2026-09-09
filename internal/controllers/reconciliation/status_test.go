package reconciliation

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	testv1 "github.com/Azure/eno/internal/controllers/reconciliation/fixtures/v1"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
					"annotations": map[string]string{
						"eno.azure.io/readiness": "self.data.foo == 'baz'",
					},
				},
				"data": map[string]string{"foo": s.Spec.Image},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, comp := writeGenericComposition(t, upstream)

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
	waitForReadiness(t, mgr, comp, syn, nil)

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

func TestTombstonedServiceWithReadinessBecomesReadyAfterDeletion(t *testing.T) {
	const readinessExpression = "self.spec.type == 'ExternalName' || (self.spec.clusterIP != '' && (self.spec.type != 'LoadBalancer' || size(self.spec.externalIPs) > 0 || size(self.status.loadBalancer.ingress) > 0))"

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, synth *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		if synth.Spec.Image == "delete" {
			return output, nil
		}

		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "Service",
				"metadata": map[string]any{
					"name":      "test-service",
					"namespace": "default",
					"annotations": map[string]any{
						"eno.azure.io/readiness": readinessExpression,
					},
				},
				"spec": map[string]any{
					"type":         "ExternalName",
					"externalName": "example.com",
				},
			},
		}}
		return output, nil
	})

	setupTestSubjectForOptions(t, mgr, Options{
		Manager:                mgr.Manager,
		Timeout:                time.Minute,
		ReadinessPollInterval:  10 * time.Millisecond,
		DisableServerSideApply: mgr.NoSsaSupport,
	})
	mgr.Start(t)
	synth, comp := writeGenericComposition(t, upstream)
	waitForReadiness(t, mgr, comp, synth, nil)
	firstSynthesisUUID := comp.Status.CurrentSynthesis.UUID

	service := &corev1.Service{}
	service.Name = "test-service"
	service.Namespace = "default"
	require.NoError(t, downstream.Get(ctx, client.ObjectKeyFromObject(service), service))

	setImage(t, upstream, synth, "delete")

	testutil.Eventually(t, func() bool {
		service := &corev1.Service{}
		service.Name = "test-service"
		service.Namespace = "default"
		return apierrors.IsNotFound(downstream.Get(ctx, client.ObjectKeyFromObject(service), service))
	})

	testutil.Eventually(t, func() bool {
		slices, err := mgr.GetCurrentResourceSlices(ctx)
		if err != nil {
			t.Log(err)
			return false
		}
		for _, slice := range slices {
			for i, manifest := range slice.Spec.Resources {
				obj := &unstructured.Unstructured{}
				if err := obj.UnmarshalJSON([]byte(manifest.Manifest)); err != nil {
					t.Log(err)
					return false
				}
				if obj.GetKind() != "Service" || obj.GetName() != "test-service" {
					continue
				}
				if !manifest.Deleted || obj.GetAnnotations()["eno.azure.io/readiness"] != readinessExpression {
					return false
				}
				if len(slice.Status.Resources) <= i {
					return false
				}
				state := slice.Status.Resources[i]
				return state.Reconciled && state.Deleted && state.Ready != nil
			}
		}
		return false
	})

	waitForReadiness(t, mgr, comp, synth, &firstSynthesisUUID)
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
	_, comp := writeGenericComposition(t, upstream)

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
