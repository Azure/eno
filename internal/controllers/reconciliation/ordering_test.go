package reconciliation

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	testv1 "github.com/Azure/eno/internal/controllers/reconciliation/fixtures/v1"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestReadinessGroups(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{
			{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      "test-obj-0",
						"namespace": "default",
						"annotations": map[string]string{
							"eno.azure.io/readiness-group": "-2",
						},
					},
					"data": map[string]any{"image": s.Spec.Image},
				},
			},
			{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      "test-obj-1",
						"namespace": "default",
					},
					"data": map[string]any{"image": s.Spec.Image},
				},
			},
			{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      "test-obj-2",
						"namespace": "default",
						"annotations": map[string]string{
							"eno.azure.io/readiness-group": "2",
						},
					},
					"data": map[string]any{"image": s.Spec.Image},
				},
			},
			{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      "test-obj-3",
						"namespace": "default",
						"annotations": map[string]string{
							"eno.azure.io/readiness-group": "4",
						},
					},
					"data": map[string]any{"image": s.Spec.Image},
				},
			},
		}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "create"
	require.NoError(t, upstream.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, upstream.Create(ctx, comp))

	// Wait for reconciliation
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

	// Prove resources were created in the expected order
	// Technically resource version is an opaque string, realistically it won't be changing
	// any time soon so it's safe to use here and less flaky than the creation timestamp
	assertOrder := func() {
		resourceVersions := []int{}
		for i := 0; i < 4; i++ {
			cm := &corev1.ConfigMap{}
			cm.Name = fmt.Sprintf("test-obj-%d", i)
			cm.Namespace = "default"
			err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
			require.NoError(t, err)

			rv, _ := strconv.Atoi(cm.ResourceVersion)
			resourceVersions = append(resourceVersions, rv)
		}
		if !slices.IsSorted(resourceVersions) { // ascending
			t.Errorf("expected resource versions to be sorted: %+d", resourceVersions)
		}
	}
	assertOrder()

	// Updates should also be ordered
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
	assertOrder()

	// Deletes should not be ordered
	require.NoError(t, upstream.Delete(ctx, comp))
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return errors.IsNotFound(err)
	})
}

func TestCRDOrdering(t *testing.T) {
	if !testutil.AtLeastVersion(t, 16) {
		t.Skipf("test does not support the old v1beta1 crd api")
		return
	}
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	// Register supporting controllers
	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		crdFixture := "fixtures/crd-runtimetest.yaml"
		if s.Spec.Image == "updated" {
			crdFixture = "fixtures/crd-runtimetest-extra-property.yaml"
		}

		crd := &unstructured.Unstructured{}
		crdBytes, err := os.ReadFile(crdFixture)
		require.NoError(t, err)
		require.NoError(t, yaml.Unmarshal(crdBytes, &crd.Object))

		cr := &unstructured.Unstructured{}
		cr.SetName("test-obj")
		cr.SetNamespace("default")
		cr.SetKind("RuntimeTest")
		cr.SetAPIVersion("enotest.azure.io/v1")
		cr.Object["spec"] = map[string]any{"values": []map[string]any{{"int": 123}}}

		if s.Spec.Image == "updated" {
			cr.Object["spec"].(map[string]any)["addedValue"] = 234
		}

		return &krmv1.ResourceList{Items: []*unstructured.Unstructured{cr, crd}}, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, comp := writeGenericComposition(t, upstream)

	// Wait for the initial creation
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

	// Update the CR and CRD to add a new property - it should exist after the next reconciliation
	// If we didn't order the writes correctly the CR update would succeed with a warning without populating the new (not yet existing) property.
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

	cr := &unstructured.Unstructured{}
	cr.SetName("test-obj")
	cr.SetNamespace("default")
	cr.SetKind("RuntimeTest")
	cr.SetAPIVersion("enotest.azure.io/v1")
	err = mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cr), cr)
	require.NoError(t, err)

	val, _, _ := unstructured.NestedInt64(cr.Object, "spec", "addedValue")
	assert.Equal(t, int64(234), val)
}

// TestInputMismatch proves that synthesis is not blocked by inputs with matching or missing revisions.
func TestInputMismatch(t *testing.T) {
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

	cm1 := &corev1.ConfigMap{}
	cm1.Name = "input1"
	cm1.Namespace = "default"
	cm1.Annotations = map[string]string{"eno.azure.io/revision": "123"}
	require.NoError(t, upstream.Create(ctx, cm1))

	cm2 := &corev1.ConfigMap{}
	cm2.Name = "input2"
	cm2.Namespace = "default"
	cm2.Annotations = map[string]string{"eno.azure.io/revision": "123"}
	require.NoError(t, upstream.Create(ctx, cm2))

	cm3 := &corev1.ConfigMap{}
	cm3.Name = "input3"
	cm3.Namespace = "default"
	cm3.Annotations = map[string]string{"eno.azure.io/not-revision": "the revision annotation is not set - this should be ignored"}
	require.NoError(t, upstream.Create(ctx, cm3))

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "create"
	syn.Spec.Refs = []apiv1.Ref{{
		Key: "foo",
		Resource: apiv1.ResourceRef{
			Version: "v1",
			Kind:    "ConfigMap",
		},
	}, {
		Key: "bar",
		Resource: apiv1.ResourceRef{
			Version: "v1",
			Kind:    "ConfigMap",
		},
	}, {
		Key: "baz",
		Resource: apiv1.ResourceRef{
			Version: "v1",
			Kind:    "ConfigMap",
		},
	}}
	require.NoError(t, upstream.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	comp.Spec.Bindings = []apiv1.Binding{{
		Key: "foo",
		Resource: apiv1.ResourceBinding{
			Name:      "input1",
			Namespace: "default",
		},
	}, {
		Key: "bar",
		Resource: apiv1.ResourceBinding{
			Name:      "input2",
			Namespace: "default",
		},
	}, {
		Key: "baz",
		Resource: apiv1.ResourceBinding{
			Name:      "input3",
			Namespace: "default",
		},
	}}
	require.NoError(t, upstream.Create(ctx, comp))

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil
	})
}

func TestInputSynthesizerOrdering(t *testing.T) {
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

	input := &corev1.ConfigMap{}
	input.Name = "input1"
	input.Namespace = "default"
	// NOTE(jordan): generation 0 _should_ work here but causes the test to flake ~once per 100 runs.
	//               not sure what's going on but fairly confident its an issue with the test not the controller.
	input.Annotations = map[string]string{"eno.azure.io/synthesizer-generation": "-1"} // too old
	require.NoError(t, upstream.Create(ctx, input))

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "create"
	syn.Spec.Refs = []apiv1.Ref{{
		Key: "foo",
		Resource: apiv1.ResourceRef{
			Version: "v1",
			Kind:    "ConfigMap",
		},
	}}
	require.NoError(t, upstream.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	comp.Spec.Bindings = []apiv1.Binding{{
		Key: "foo",
		Resource: apiv1.ResourceBinding{
			Name:      "input1",
			Namespace: "default",
		},
	}}
	require.NoError(t, upstream.Create(ctx, comp))

	// Synthesis should not have been dispatched with the first state of the input
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && (comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil)
	})

	// Synthesis can happen once the input catches up
	input.Annotations = map[string]string{"eno.azure.io/synthesizer-generation": fmt.Sprintf("%d", syn.Generation)}
	require.NoError(t, upstream.Update(ctx, input))

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil
	})
	assert.Equal(t, input.ResourceVersion, comp.Status.InputRevisions[0].ResourceVersion)
}

func TestInputCompositionGenerationOrdering(t *testing.T) {
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

	input := &corev1.ConfigMap{}
	input.Name = "input1"
	input.Namespace = "default"
	input.Annotations = map[string]string{"eno.azure.io/composition-generation": "-1"} // too old
	require.NoError(t, upstream.Create(ctx, input))

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "create"
	syn.Spec.Refs = []apiv1.Ref{{
		Key: "foo",
		Resource: apiv1.ResourceRef{
			Version: "v1",
			Kind:    "ConfigMap",
		},
	}}
	require.NoError(t, upstream.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	comp.Spec.Bindings = []apiv1.Binding{{
		Key: "foo",
		Resource: apiv1.ResourceBinding{
			Name:      "input1",
			Namespace: "default",
		},
	}}
	require.NoError(t, upstream.Create(ctx, comp))

	// Adding some delay to ensure informers catch up.
	time.Sleep(50 * time.Millisecond)
	// Synthesis should not have been dispatched with the first state of the input.
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && (comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil)
	})

	// Synthesis can happen once the input catches up.
	input.Annotations = map[string]string{"eno.azure.io/composition-generation": fmt.Sprintf("%d", comp.Generation)}
	require.NoError(t, upstream.Update(ctx, input))

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil
	})
	assert.Equal(t, input.ResourceVersion, comp.Status.InputRevisions[0].ResourceVersion)
}

func TestDeletionGroups(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{
			{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":       "default-group",
						"namespace":  "default",
						"finalizers": []any{"eno.azure.io/test"}, // this resource will never delete successfully
						"annotations": map[string]string{
							"eno.azure.io/deletion-strategy": "Foreground",
						},
					},
				},
			},
			{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      "deleted-after-default",
						"namespace": "default",
						"annotations": map[string]string{
							"eno.azure.io/readiness-group":  "-1",
							"eno.azure.io/ordered-deletion": "true",
						},
					},
				},
			},
			{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      "deleted-before-default",
						"namespace": "default",
						"annotations": map[string]string{
							"eno.azure.io/readiness-group":  "1",
							"eno.azure.io/ordered-deletion": "true",
						},
					},
				},
			},
			{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      "non-ordered",
						"namespace": "default",
						"annotations": map[string]string{
							"eno.azure.io/readiness-group": "-1",
						},
					},
				},
			},
		}
		return output, nil
	})

	setupTestSubject(t, mgr)
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "deletion-test-syn"
	syn.Spec.Image = "create"
	require.NoError(t, upstream.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "deletion-test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, upstream.Create(ctx, comp))

	// Wait for initial reconciliation
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil
	})

	// Delete the composition
	require.NoError(t, upstream.Delete(ctx, comp))

	// Wait for the default deletion group (0) to be reached
	testutil.Eventually(t, func() bool {
		res := &corev1.ConfigMap{}
		res.Name = "default-group"
		res.Namespace = "default"
		return mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(res), res) == nil && res.DeletionTimestamp != nil
	})

	// The earlier deletion group should be deleted
	testutil.Eventually(t, func() bool {
		res := &corev1.ConfigMap{}
		res.Name = "deleted-before-default"
		res.Namespace = "default"
		return errors.IsNotFound(mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(res), res))
	})

	// The non-ordered resource should be deleted
	testutil.Eventually(t, func() bool {
		res := &corev1.ConfigMap{}
		res.Name = "non-ordered"
		res.Namespace = "default"
		return errors.IsNotFound(mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(res), res))
	})

	// The later deletion group is blocked by the default group's finalizer
	time.Sleep(time.Millisecond * 200)
	res := &corev1.ConfigMap{}
	res.Name = "deleted-after-default"
	res.Namespace = "default"
	assert.Nil(t, mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(res), res))
}
