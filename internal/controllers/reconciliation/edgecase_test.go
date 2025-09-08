package reconciliation

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	testv1 "github.com/Azure/eno/internal/controllers/reconciliation/fixtures/v1"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
)

// TestMissingNamespace proves that resynthesis is not blocked by resources that lack a namespace.
func TestMissingNamespace(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)
	namespace := atomic.Pointer[string]{}
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": namespace.Load(), // bad news
				},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, comp := writeGenericComposition(t, upstream)

	// Initial synthesis
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

	// Fixing the namespace should be possible
	namespace.Store(ptr.To("default"))
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn)
		syn.Spec.Image = "updated"
		return upstream.Update(ctx, syn)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})
}

// TestMissingNamespaceDeletion proves that composition deletion is not blocked by resources that lack a namespace.
func TestMissingNamespaceDeletion(t *testing.T) {
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
					"namespace": "", // bad news
				},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, comp := writeGenericComposition(t, upstream)

	// Initial synthesis
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

	// Deleting the composition should succeed eventually
	require.NoError(t, upstream.Delete(ctx, comp))
	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	})
}

func TestEmptySynthesis(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		return &krmv1.ResourceList{}, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, comp := writeGenericComposition(t, upstream)

	// Initial synthesis
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

	// Readiness
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil
	})

	// Deletion
	require.NoError(t, upstream.Delete(ctx, comp))
	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	})
}

// TestLargeNamespaceDeletion tests for race conditions between an external client (like namespace controller)
// when deleting a large number of resources.
func TestLargeNamespaceDeletion(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)
	ns := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": map[string]any{
				"name": "test",
			},
		},
	}
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{ns}

		for i := 0; i < 500; i++ {
			cm := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      fmt.Sprintf("test-%d", i),
						"namespace": ns.GetName(),
					},
				},
			}
			output.Items = append(output.Items, cm)
		}

		return output, nil
	})

	setupTestSubject(t, mgr)
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil
	})

	go func() {
		for i := 0; i < 500; i++ {
			cm := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      fmt.Sprintf("test-%d", i),
						"namespace": ns.GetName(),
					},
				},
			}
			t.Logf("deleting configmap %s", cm.GetName())
			mgr.DownstreamClient.Delete(ctx, cm)
			time.Sleep(time.Millisecond * 3)
		}
	}()

	require.NoError(t, upstream.Delete(ctx, comp))

	assert.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return errors.IsNotFound(err)
	}, time.Minute*3, time.Second)
}

// TestPatchStrategyReplace proves that resources which define a patch strategy of "replace" will eventually converge.
func TestPatchStrategyReplace(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	if mgr.DownstreamVersion < 21 {
		t.Skipf("skipping test for downstream version %d because it doesn't support policy/v1", mgr.DownstreamVersion)
	}

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "policy/v1",
				"kind":       "PodDisruptionBudget", // only PDBs set patch strategy == replace
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
				},
				"spec": map[string]any{
					"maxUnavailable": 1,
					"selector": map[string]any{
						"matchLabels": map[string]any{"app": "foobar"},
					},
				},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)

	// It should be able to become ready
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedCompositionGeneration == comp.Generation
	})
}

func TestDeploymentFieldRefs(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
				},
				"spec": map[string]any{
					"selector": map[string]any{
						"matchLabels": map[string]any{"app": "foobar"},
					},
					"template": map[string]any{
						"metadata": map[string]any{
							"labels": map[string]string{"app": "foobar"},
						},
						"spec": map[string]any{
							"containers": []map[string]any{{
								"name":  "nginx",
								"image": "nginx:latest",
								"env": []map[string]any{{
									"name": "FOO",
									"valueFrom": map[string]any{
										"fieldRef": map[string]string{"fieldPath": "metadata.namespace"},
									},
								}},
							}},
						},
					},
				},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)

	// It should be able to become ready
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedCompositionGeneration == comp.Generation
	})
}

// TestForceResynthesis proves that a composition can be forced to resynthesize.
func TestForceResynthesis(t *testing.T) {
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
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, comp := writeGenericComposition(t, upstream)

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})
	initialUUID := comp.Status.GetCurrentSynthesisUUID()

	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.ForceResynthesis()
		return upstream.Update(ctx, comp)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return comp.Status.GetCurrentSynthesisUUID() != initialUUID && !comp.ShouldForceResynthesis()
	})
}

func TestDeletedResourceSlice(t *testing.T) {
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
			},
		}}
		return output, nil
	})

	setupTestSubject(t, mgr)
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)

	// Wait for initial synthesis
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil
	})
	initialUUID := comp.Status.GetCurrentSynthesisUUID()

	for i := 0; i < 2; i++ {
		// Force delete the slices
		require.NoError(t, upstream.DeleteAllOf(ctx, &apiv1.ResourceSlice{}, client.InNamespace("default")))
		err := retry.RetryOnConflict(testutil.Backoff, func() error {
			list := &apiv1.ResourceSliceList{}
			require.NoError(t, upstream.List(ctx, list))

			for _, item := range list.Items {
				if len(item.Finalizers) == 0 {
					continue
				}
				item.Finalizers = []string{}
				err := upstream.Update(ctx, &item)
				if err != nil && !errors.IsNotFound(err) { // it's possible that some have been deleted before getting a finalizer
					return err
				}
			}

			return nil
		})
		require.NoError(t, err)

		// The composition should eventually be resynth'd
		testutil.Eventually(t, func() bool {
			err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
			return err == nil && comp.Status.GetCurrentSynthesisUUID() != initialUUID && comp.Status.CurrentSynthesis.Reconciled != nil
		})
		initialUUID = comp.Status.GetCurrentSynthesisUUID()
	}
}

func TestMissingInput(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)
	setupTestSubject(t, mgr)
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "create"
	syn.Spec.Refs = []apiv1.Ref{{Key: "test-ref"}}
	require.NoError(t, upstream.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	comp.Spec.Bindings = []apiv1.Binding{{Key: "test-ref"}}
	require.NoError(t, upstream.Create(ctx, comp))

	// Status should eventually be updated
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.Simplified != nil && comp.Status.Simplified.Status == "MissingInputs"
	})
}

func TestMissingInputBinding(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)
	setupTestSubject(t, mgr)
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "create"
	syn.Spec.Refs = []apiv1.Ref{{Key: "test-ref"}}
	require.NoError(t, upstream.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, upstream.Create(ctx, comp))

	// Status should eventually be updated
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.Simplified != nil && comp.Status.Simplified.Status == "MissingInputs"
	})
}

// TestDeletingUnknownKind proves that compositions can be deleted even if a CR was never successfully
// created due to its kind not being registered.
func TestDeletingUnknownKind(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "myapi.myapp.io/v1",
				"kind":       "DoesNotExist",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
				},
			},
		}}
		return output, nil
	})

	setupTestSubject(t, mgr)
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)

	// Wait for synthesis
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil
	})

	// It should eventually delete successfully
	require.NoError(t, upstream.Delete(ctx, comp))
	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	})
}

// TestSynthesisErrorResult proves that synthesizers that return error results while exiting 0 are retried.
func TestSynthesisErrorResult(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)

	var calls atomic.Int32
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		if calls.Add(1) < 2 {
			output.Results = []*krmv1.Result{{
				Message:  "test error",
				Severity: krmv1.ResultSeverityError,
			}}
		}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
				},
			},
		}}
		return output, nil
	})

	setupTestSubject(t, mgr)
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.InFlightSynthesis == nil
	})
}

func TestOptimisticMode_NoReadinessChecks(t *testing.T) {
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
				"data": "invalid!",
			},
		}}
		return output, nil
	})

	setupTestSubjectForOptions(t, mgr, Options{
		Manager:                    mgr.Manager,
		Timeout:                    time.Minute,
		ReadinessPollInterval:      time.Hour,
		DisableServerSideApply:     mgr.NoSsaSupport,
		DisableReconciliationCheck: true,
	})
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil && comp.Status.CurrentSynthesis.Ready != nil
	})
}

func TestOptimisticMode_WithReadinessChecks(t *testing.T) {
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
					"name":        "test-obj",
					"namespace":   "default",
					"annotations": map[string]any{"eno.azure.io/readiness": "true"},
				},
				"data": "invalid!",
			},
		}}
		return output, nil
	})

	setupTestSubjectForOptions(t, mgr, Options{
		Manager:                    mgr.Manager,
		Timeout:                    time.Minute,
		ReadinessPollInterval:      time.Hour,
		DisableServerSideApply:     mgr.NoSsaSupport,
		DisableReconciliationCheck: true,
	})
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil && comp.Status.CurrentSynthesis.Ready == nil
	})
}
