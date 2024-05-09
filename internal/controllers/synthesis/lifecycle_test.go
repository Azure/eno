package synthesis

import (
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

// TestCompositionDeletion proves that a composition's status is eventually updated to reflect its deletion.
// This is necessary to unblock finalizer removal, since we don't synthesize deleted compositions.
func TestCompositionDeletion(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewExecController(mgr.Manager, minimalTestConfig, &testutil.ExecConn{
		Hook: func(s *apiv1.Synthesizer) []client.Object {
			cm := &corev1.ConfigMap{}
			cm.APIVersion = "v1"
			cm.Kind = "ConfigMap"
			cm.Name = "test"
			cm.Namespace = "default"
			return []client.Object{cm}
		},
	}))

	require.NoError(t, NewPodLifecycleController(mgr.Manager, minimalTestConfig))
	require.NoError(t, NewStatusController(mgr.Manager))
	require.NoError(t, NewSliceCleanupController(mgr.Manager))
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

// TestPodConcurrencyLimit proves that Eno will not create more than two concurrent pods per composition.
func TestPodConcurrencyLimit(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewPodLifecycleController(mgr.Manager, minimalTestConfig))
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

	// Wait for the first pod to be created
	testutil.Eventually(t, func() bool {
		pods := &corev1.PodList{}
		cli.List(ctx, pods)
		return len(pods.Items) > 0
	})

	// Change something on the composition
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Spec.Bindings = []apiv1.Binding{{Key: "new-binding", Resource: apiv1.ResourceBinding{Name: "test"}}}
		return cli.Update(ctx, comp)
	})
	require.NoError(t, err)

	// Wait for the second pod to be created
	testutil.Eventually(t, func() bool {
		pods := &corev1.PodList{}
		cli.List(ctx, pods)
		return len(pods.Items) > 1
	})

	// Change something on the composition again
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Spec.Bindings = []apiv1.Binding{{Key: "new-binding", Resource: apiv1.ResourceBinding{Name: "test"}}}
		return cli.Update(ctx, comp)
	})
	require.NoError(t, err)

	// A third pod shouldn't be created
	time.Sleep(time.Millisecond * 100)
	pods := &corev1.PodList{}
	err = cli.List(ctx, pods)
	require.NoError(t, err)
	assert.Len(t, pods.Items, 2)
}

var shouldDeletePodTests = []struct {
	Name               string
	Pods               []corev1.Pod
	Composition        *apiv1.Composition
	Synth              *apiv1.Synthesizer
	PodShouldExist     bool
	PodShouldBeDeleted bool
}{
	{
		Name:               "no-pods",
		Pods:               []corev1.Pod{},
		Composition:        &apiv1.Composition{},
		Synth:              &apiv1.Synthesizer{},
		PodShouldExist:     false,
		PodShouldBeDeleted: false,
	},
	{
		Name: "still-in-use",
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.Now(),
				Annotations: map[string]string{
					"eno.azure.io/composition-generation": "2",
				},
			},
		}},
		Composition: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				Generation: 2,
			},
		},
		Synth: &apiv1.Synthesizer{
			Spec: apiv1.SynthesizerSpec{
				PodTimeout: ptr.To(metav1.Duration{Duration: time.Hour}),
			},
		},
		PodShouldExist:     true,
		PodShouldBeDeleted: false,
	},
	{
		Name: "success",
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.Now(),
				Annotations: map[string]string{
					"eno.azure.io/composition-generation": "2",
				},
			},
		}},
		Composition: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				Generation: 2,
			},
			Status: apiv1.CompositionStatus{
				CurrentSynthesis: &apiv1.Synthesis{
					Synthesized: ptr.To(metav1.Now()),
				},
			},
		},
		Synth: &apiv1.Synthesizer{
			Spec: apiv1.SynthesizerSpec{
				PodTimeout: ptr.To(metav1.Duration{Duration: time.Hour}),
			},
		},
		PodShouldExist:     true,
		PodShouldBeDeleted: true,
	},
	{
		Name: "success-and-wrong-gen",
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.Now(),
				Annotations: map[string]string{
					"eno.azure.io/composition-generation": "1",
				},
			},
		}},
		Composition: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				Generation: 2,
			},
			Status: apiv1.CompositionStatus{
				CurrentSynthesis: &apiv1.Synthesis{
					Synthesized: ptr.To(metav1.Now()),
				},
			},
		},
		Synth: &apiv1.Synthesizer{
			Spec: apiv1.SynthesizerSpec{
				PodTimeout: ptr.To(metav1.Duration{Duration: time.Hour}),
			},
		},
		PodShouldExist:     true,
		PodShouldBeDeleted: true,
	},
	{
		Name: "pod-timeout",
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute * 2)),
				Annotations: map[string]string{
					"eno.azure.io/composition-generation": "2",
				},
			},
		}},
		Composition: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				Generation: 2,
			},
		},
		Synth: &apiv1.Synthesizer{
			Spec: apiv1.SynthesizerSpec{
				PodTimeout: ptr.To(metav1.Duration{Duration: time.Minute}),
			},
		},
		PodShouldExist:     true,
		PodShouldBeDeleted: true,
	},
	{
		Name: "composition-deleted",
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.Now(),
				Annotations: map[string]string{
					"eno.azure.io/composition-generation": "2",
				},
			},
		}},
		Composition: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				DeletionTimestamp: &metav1.Time{Time: time.Now()},
				Generation:        2,
			},
		},
		Synth: &apiv1.Synthesizer{
			Spec: apiv1.SynthesizerSpec{
				PodTimeout: ptr.To(metav1.Duration{Duration: time.Hour}),
			},
		},
		PodShouldExist:     true,
		PodShouldBeDeleted: true,
	},
	{
		Name: "synth-deleted",
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.Now(),
				Annotations: map[string]string{
					"eno.azure.io/composition-generation": "2",
				},
			},
		}},
		Composition: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				Generation: 2,
			},
		},
		Synth:              nil,
		PodShouldExist:     true,
		PodShouldBeDeleted: true,
	},
	{
		Name: "composition-and-pod-deleted",
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.Now(),
				DeletionTimestamp: ptr.To(metav1.Now()),
				Annotations: map[string]string{
					"eno.azure.io/composition-generation": "2",
				},
			},
		}},
		Composition: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				DeletionTimestamp: &metav1.Time{Time: time.Now()},
				Generation:        2,
			},
		},
		Synth: &apiv1.Synthesizer{
			Spec: apiv1.SynthesizerSpec{
				PodTimeout: ptr.To(metav1.Duration{Duration: time.Hour}),
			},
		},
		PodShouldExist:     false,
		PodShouldBeDeleted: false,
	},
	{
		Name: "one-pod-deleting",
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.Now(),
				DeletionTimestamp: &metav1.Time{Time: time.Now()},
				Annotations: map[string]string{
					"eno.azure.io/composition-generation": "2",
				},
			},
		}},
		Composition: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				Generation: 2,
			},
		},
		Synth: &apiv1.Synthesizer{
			Spec: apiv1.SynthesizerSpec{
				PodTimeout: ptr.To(metav1.Duration{Duration: time.Hour}),
			},
		},
		PodShouldExist:     false,
		PodShouldBeDeleted: false,
	},
	{
		Name: "two-pods-deleting",
		Pods: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Now(),
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
					Annotations: map[string]string{
						"eno.azure.io/composition-generation": "2",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Now(),
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
					Annotations: map[string]string{
						"eno.azure.io/composition-generation": "2",
					},
				},
			},
		},
		Composition: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				Generation: 2,
			},
		},
		Synth: &apiv1.Synthesizer{
			Spec: apiv1.SynthesizerSpec{
				PodTimeout: ptr.To(metav1.Duration{Duration: time.Hour}),
			},
		},
		PodShouldExist:     true,
		PodShouldBeDeleted: false,
	},
	{
		Name: "three-pods-deleting",
		Pods: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Now(),
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
					Annotations: map[string]string{
						"eno.azure.io/composition-generation": "2",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Now(),
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
					Annotations: map[string]string{
						"eno.azure.io/composition-generation": "2",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Now(),
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
					Annotations: map[string]string{
						"eno.azure.io/composition-generation": "2",
					},
				},
			},
		},
		Composition: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				Generation: 2,
			},
		},
		Synth: &apiv1.Synthesizer{
			Spec: apiv1.SynthesizerSpec{
				PodTimeout: ptr.To(metav1.Duration{Duration: time.Hour}),
			},
		},
		PodShouldExist:     true,
		PodShouldBeDeleted: false,
	},
}

func TestShouldDeletePod(t *testing.T) {
	logger := testr.New(t)

	for _, tc := range shouldDeletePodTests {
		t.Run(tc.Name, func(t *testing.T) {
			logger, pod, exists := shouldDeletePod(logger, tc.Composition, tc.Synth, &corev1.PodList{Items: tc.Pods})
			assert.Equal(t, tc.PodShouldExist, exists)
			assert.Equal(t, tc.PodShouldBeDeleted, pod != nil)
			logger.V(0).Info("logging to see the appended fields for debugging purposes")
		})
	}
}
