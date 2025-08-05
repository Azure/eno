package reconciliation

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"sync"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/inputs"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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

// TestBulkSynthesizerUpdates proves that the composition eventually converges on the latest synthesizer image when updated rapidly.
func TestBulkSynthesizerUpdates(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		time.Sleep(time.Duration(rand.IntN(500)) * time.Millisecond)

		output := &krmv1.ResourceList{}
		output.Results = []*krmv1.Result{{Message: s.Spec.Image}}
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

	setupTestSubject(t, mgr)
	mgr.Start(t)

	synth, comp := writeGenericComposition(t, upstream)

	for i := 0; i < 50; i++ {
		err := retry.RetryOnConflict(testutil.Backoff, func() error {
			upstream.Get(ctx, client.ObjectKeyFromObject(synth), synth)
			synth.Spec.Image = fmt.Sprintf("synth-%d", i)
			return upstream.Update(ctx, synth)
		})
		require.NoError(t, err)
	}

	testutil.Eventually(t, func() bool {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		syn := comp.Status.CurrentSynthesis
		return syn != nil && len(syn.Results) == 1 && syn.Results[0].Message == "synth-49"
	})
}

// TestBulkLockstepInputUpdates proves that syntheses are not committed when inputs declare conflicting revisions.
func TestBulkLockstepInputUpdates(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		time.Sleep(time.Duration(rand.IntN(500)) * time.Millisecond)
		return &krmv1.ResourceList{}, nil
	})

	input1 := &corev1.ConfigMap{}
	input1.Name = "input-1"
	input1.Namespace = "default"
	input1.Annotations = map[string]string{"eno.azure.io/revision": "1"}

	input2 := &corev1.ConfigMap{}
	input2.Name = "input-2"
	input2.Namespace = "default"
	input2.Annotations = map[string]string{"eno.azure.io/revision": "2"}

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-syn"
	synth.Spec.Image = "create"
	synth.Spec.Refs = []apiv1.Ref{
		{Key: "key-1", Resource: apiv1.ResourceRef{Version: "v1", Kind: "ConfigMap", Name: input1.Name, Namespace: input1.Namespace}},
		{Key: "key-2", Resource: apiv1.ResourceRef{Version: "v1", Kind: "ConfigMap", Name: input2.Name, Namespace: input2.Namespace}},
	}

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = synth.Name

	// Watch composition to make sure every committed synthesis has a valid set of inputs
	_, err := ctrl.NewControllerManagedBy(mgr.Manager).
		For(&apiv1.Composition{}).
		Build(reconcile.Func(func(ctx context.Context, r reconcile.Request) (reconcile.Result, error) {
			comp := &apiv1.Composition{}
			err := upstream.Get(ctx, r.NamespacedName, comp)
			if err != nil {
				return reconcile.Result{}, client.IgnoreNotFound(err)
			}
			syn := comp.Status.CurrentSynthesis
			if syn == nil {
				return reconcile.Result{}, nil
			}
			if inputs.OutOfLockstep(synth, comp, syn.InputRevisions) {
				t.Errorf("a synthesis with inconsistent inputs was committed!")
			}
			return reconcile.Result{}, nil
		}))
	require.NoError(t, err)

	setupTestSubject(t, mgr)
	mgr.Start(t)

	require.NoError(t, upstream.Create(ctx, input1))
	require.NoError(t, upstream.Create(ctx, input2))
	require.NoError(t, upstream.Create(ctx, synth))
	require.NoError(t, upstream.Create(ctx, comp))

	// Randomly modify both inputs concurrently
	var wg sync.WaitGroup
	randomize := func(cm *corev1.ConfigMap) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				time.Sleep(time.Duration(rand.IntN(50)) * time.Millisecond)
				upstream.Get(ctx, client.ObjectKeyFromObject(cm), cm)
				updated := cm.DeepCopy()
				updated.Annotations["eno.azure.io/revision"] = strconv.Itoa(rand.IntN(10))
				require.NoError(t, upstream.Patch(ctx, updated, client.MergeFrom(cm)))
			}
		}()
	}
	randomize(input1)
	randomize(input2)
	wg.Wait()
}

// TestSynthesisTimeout proves that synthesis pods will eventually time out and be recreated.
func TestSynthesisTimeout(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		time.Sleep(2 * time.Second) // long enough to never succeed
		return &krmv1.ResourceList{}, nil
	})

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

	// A few pods should be created eventually
	seenPods := map[string]struct{}{}
	pods := &corev1.PodList{}
	testutil.Eventually(t, func() bool {
		upstream.List(ctx, pods)
		for _, pod := range pods.Items {
			seenPods[pod.Name] = struct{}{}
		}
		return len(seenPods) > 1
	})

	// The synthesis should have timed out every time
	require.NoError(t, upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	require.Nil(t, comp.Status.CurrentSynthesis)

	// Pods are eventually cleaned up
	testutil.Eventually(t, func() bool {
		return upstream.List(ctx, pods) == nil && len(pods.Items) >= 1
	})
}
