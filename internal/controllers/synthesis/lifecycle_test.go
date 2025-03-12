package synthesis

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/scheduling"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
)

// TestCompositionDeletion proves that a composition's status is eventually updated to reflect its deletion.
// This is necessary to unblock finalizer removal, since we don't synthesize deleted compositions.
func TestCompositionDeletion(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]string{
					"name":      "test",
					"namespace": "default",
				},
			},
		}}
		return output, nil
	})

	require.NoError(t, NewPodLifecycleController(mgr.Manager, minimalTestConfig))
	require.NoError(t, NewSliceCleanupController(mgr.Manager))
	require.NoError(t, scheduling.NewController(mgr.Manager, 10, 2*time.Second, time.Second))
	require.NoError(t, NewPodGC(mgr.Manager, 0))
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn-1"
	syn.Spec.Image = "initial-image"
	require.NoError(t, cli.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, cli.Create(ctx, comp))

	// Create the composition's resource slice
	testutil.Eventually(t, func() bool {
		require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
		return comp.Status.CurrentSynthesis != nil && len(comp.Status.CurrentSynthesis.ResourceSlices) > 0
	})
	assert.NotNil(t, comp.Status.CurrentSynthesis.Initialized, "initialized timestamp is set")

	// Wait for the resource slice to be created
	testutil.Eventually(t, func() bool {
		require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
		return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.ResourceSlices != nil
	})

	// Delete the composition
	require.NoError(t, cli.Delete(ctx, comp))
	deleteGen := comp.Generation

	// The generation should be updated
	testutil.Eventually(t, func() bool {
		require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
		return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.ObservedCompositionGeneration >= deleteGen
	})

	// The composition should still exist after a bit
	// Yeahyeahyeah a fake clock would be better but this is more obvious and not meaningfully slower
	time.Sleep(time.Millisecond * 100)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))

	// Mark the composition as reconciled
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.CurrentSynthesis.Reconciled = ptr.To(metav1.Now())
		return cli.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	// The composition should eventually be released
	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	})
}

func TestNonExistentComposition(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewPodLifecycleController(mgr.Manager, minimalTestConfig))
	mgr.Start(t)

	pod := &corev1.Pod{}
	pod.Name = "some-synthesis-pod"
	pod.Namespace = "default"
	pod.Labels = map[string]string{
		"eno.azure.io/composition-name":      "some-comp",
		"eno.azure.io/composition-namespace": "default",
	}
	pod.Spec.Containers = []corev1.Container{{
		Name:  "executor",
		Image: "some-image-tag",
	}}
	pnn := client.ObjectKeyFromObject(pod)

	require.NoError(t, cli.Create(ctx, pod))
	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, pnn, pod)
		return errors.IsNotFound(err)
	})
}

func TestCheckExistingPods(t *testing.T) {
	ctx := testutil.NewContext(t)

	t.Run("no pods", func(t *testing.T) {
		cli := testutil.NewClient(t)
		c := &podLifecycleController{client: cli}

		pods := &corev1.PodList{}
		comp := &apiv1.Composition{}
		ok, err := c.safeToCreatePod(ctx, pods, comp)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("one active pod", func(t *testing.T) {
		cli := testutil.NewClient(t)
		c := &podLifecycleController{client: cli}

		comp := &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				InFlightSynthesis: &apiv1.Synthesis{UUID: "some-uuid"},
			},
		}
		pods := &corev1.PodList{
			Items: []corev1.Pod{{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"eno.azure.io/synthesis-uuid": comp.Status.InFlightSynthesis.UUID},
				},
			}},
		}
		ok, err := c.safeToCreatePod(ctx, pods, comp)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("one terminating pod", func(t *testing.T) {
		cli := testutil.NewClient(t)
		c := &podLifecycleController{client: cli}

		comp := &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				InFlightSynthesis: &apiv1.Synthesis{UUID: "some-uuid"},
			},
		}
		pods := &corev1.PodList{
			Items: []corev1.Pod{{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{},
					Labels:            map[string]string{"eno.azure.io/synthesis-uuid": comp.Status.InFlightSynthesis.UUID},
				},
			}},
		}
		ok, err := c.safeToCreatePod(ctx, pods, comp)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("two terminating pods", func(t *testing.T) {
		cli := testutil.NewClient(t)
		c := &podLifecycleController{client: cli}

		comp := &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				InFlightSynthesis: &apiv1.Synthesis{UUID: "some-uuid"},
			},
		}
		pods := &corev1.PodList{
			Items: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: &metav1.Time{},
						Labels:            map[string]string{"eno.azure.io/synthesis-uuid": comp.Status.InFlightSynthesis.UUID},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: &metav1.Time{},
						Labels:            map[string]string{"eno.azure.io/synthesis-uuid": comp.Status.InFlightSynthesis.UUID},
					},
				},
			},
		}
		ok, err := c.safeToCreatePod(ctx, pods, comp)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("two active pods", func(t *testing.T) {
		comp := &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				InFlightSynthesis: &apiv1.Synthesis{UUID: "some-uuid"},
			},
		}
		pods := &corev1.PodList{
			Items: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "foo",
						CreationTimestamp: metav1.NewTime(time.Date(1965, 1, 1, 0, 0, 0, 0, time.UTC)),
						Labels:            map[string]string{"eno.azure.io/synthesis-uuid": comp.Status.InFlightSynthesis.UUID},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "bar",
						CreationTimestamp: metav1.NewTime(time.Date(1964, 1, 1, 0, 0, 0, 0, time.UTC)),
						Labels:            map[string]string{"eno.azure.io/synthesis-uuid": comp.Status.InFlightSynthesis.UUID},
					},
				},
			},
		}
		rand.Shuffle(len(pods.Items), func(i, j int) {
			pods.Items[i], pods.Items[j] = pods.Items[j], pods.Items[i]
		})

		cli := testutil.NewClient(t, &pods.Items[0], &pods.Items[1])
		c := &podLifecycleController{client: cli}

		ok, err := c.safeToCreatePod(ctx, pods, comp)
		require.NoError(t, err)
		assert.False(t, ok)

		// The newest dup is deleted
		assert.Error(t, cli.Get(ctx, types.NamespacedName{Name: "foo"}, &corev1.Pod{}))
		assert.NoError(t, cli.Get(ctx, types.NamespacedName{Name: "bar"}, &corev1.Pod{}))
	})
}
