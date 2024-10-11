package synthesis

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/flowcontrol"
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
	require.NoError(t, flowcontrol.NewSynthesisConcurrencyLimiter(mgr.Manager, 10, 0))
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
				Labels: map[string]string{
					"eno.azure.io/synthesis-uuid": "test-uuid",
				},
			},
		}},
		Composition: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{},
			Status: apiv1.CompositionStatus{
				CurrentSynthesis: &apiv1.Synthesis{
					UUID: "test-uuid",
				},
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
		Name: "container-timeout",
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.Now(),
				DeletionTimestamp: ptr.To(metav1.Now()),
			},
		}, {
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute * 2)),
				Labels:            map[string]string{},
			},
			Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
				Type:               corev1.PodScheduled,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Minute * 2)),
			}}},
		}},
		Composition: &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				CurrentSynthesis: &apiv1.Synthesis{},
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
		Name: "container-timeout-negative",
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.Now(),
				DeletionTimestamp: ptr.To(metav1.Now()),
			},
		}, {
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute * 2)),
				Labels:            map[string]string{},
			},
			Spec: corev1.PodSpec{NodeName: "anything"},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{{}},
				Conditions: []corev1.PodCondition{{
					Type:               corev1.PodScheduled,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Minute * 2)),
				}},
			},
		}},
		Composition: &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				CurrentSynthesis: &apiv1.Synthesis{},
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
		Name: "container-timeout-not-scheduled",
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.Now(),
				DeletionTimestamp: ptr.To(metav1.Now()),
			},
		}, {
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute * 2)),
				Labels:            map[string]string{},
			},
			Status: corev1.PodStatus{},
		}},
		Composition: &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				CurrentSynthesis: &apiv1.Synthesis{},
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
		Name: "container-timeout-not-scheduled-but-somehow-created",
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.Now(),
				DeletionTimestamp: ptr.To(metav1.Now()),
			},
		}, {
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute * 2)),
				Labels:            map[string]string{},
			},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{}}},
		}},
		Composition: &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				CurrentSynthesis: &apiv1.Synthesis{},
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
		Name: "pod-timeout",
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Second * 2)),
				Labels:            map[string]string{},
			},
		}},
		Composition: &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				CurrentSynthesis: &apiv1.Synthesis{},
			},
		},
		Synth: &apiv1.Synthesizer{
			Spec: apiv1.SynthesizerSpec{
				PodTimeout: ptr.To(metav1.Duration{Duration: time.Second}),
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
			logger, pod, exists := shouldDeletePod(logger, tc.Composition, tc.Synth, &corev1.PodList{Items: tc.Pods}, time.Minute)
			assert.Equal(t, tc.PodShouldExist, exists)
			assert.Equal(t, tc.PodShouldBeDeleted, pod != nil)
			logger.V(0).Info("logging to see the appended fields for debugging purposes")
		})
	}
}

func TestShouldSwapStates(t *testing.T) {
	tests := []struct {
		Name        string
		Expectation bool
		Composition apiv1.Composition
	}{
		{
			Name:        "zero value",
			Expectation: true,
		},
		{
			Name:        "missing input",
			Expectation: false,
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}},
				},
			},
		},
		{
			Name:        "matching input synthesis in progress",
			Expectation: false,
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						InputRevisions: []apiv1.InputRevisions{{
							Key: "foo",
						}},
					},
					InputRevisions: []apiv1.InputRevisions{{
						Key: "foo",
					}},
				},
			},
		},
		{
			Name:        "non-matching composition generation",
			Expectation: true,
			Composition: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 234,
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						ObservedCompositionGeneration: 123,
					},
				},
			},
		},
		{
			Name:        "matching input synthesis terminal",
			Expectation: false,
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						InputRevisions: []apiv1.InputRevisions{{
							Key: "foo",
						}},
						Synthesized: ptr.To(metav1.Now()),
					},
					InputRevisions: []apiv1.InputRevisions{{
						Key: "foo",
					}},
				},
			},
		},
		{
			Name:        "non-matching input synthesis terminal",
			Expectation: true,
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						InputRevisions: []apiv1.InputRevisions{{
							Key: "foo",
						}},
						Synthesized: ptr.To(metav1.Now()),
					},
					InputRevisions: []apiv1.InputRevisions{{
						Key:             "foo",
						ResourceVersion: "new",
					}},
				},
			},
		},
		{
			Name:        "non-matching input synthesis terminal ignore side effects",
			Expectation: false,
			Composition: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"eno.azure.io/ignore-side-effects": "true",
					},
				},
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						InputRevisions: []apiv1.InputRevisions{{
							Key: "foo",
						}},
						Synthesized: ptr.To(metav1.Now()),
					},
					InputRevisions: []apiv1.InputRevisions{{
						Key:             "foo",
						ResourceVersion: "new",
					}},
				},
			},
		},
		{
			Name:        "non-matching input synthesis non-terminal",
			Expectation: false,
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						InputRevisions: []apiv1.InputRevisions{{
							Key: "foo",
						}},
						// Synthesized: ptr.To(metav1.Now()),
					},
					InputRevisions: []apiv1.InputRevisions{{
						Key:             "foo",
						ResourceVersion: "new",
					}},
				},
			},
		},
		{
			Name:        "non-matching input synthesis deleting",
			Expectation: true,
			Composition: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: ptr.To(metav1.Now()),
					Generation:        2,
				},
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						InputRevisions: []apiv1.InputRevisions{{
							Key: "foo",
						}},
					},
					InputRevisions: []apiv1.InputRevisions{{
						Key:             "foo",
						ResourceVersion: "new",
					}},
				},
			},
		},
		{
			Name:        "missing input synthesis deleting",
			Expectation: true,
			Composition: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: ptr.To(metav1.Now()),
					Generation:        2,
				},
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}},
				},
				Status: apiv1.CompositionStatus{},
			},
		},
		{
			Name:        "revision mismatch",
			Expectation: false,
			Composition: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 1,
				},
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}, {Key: "bar"}},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{},
					InputRevisions: []apiv1.InputRevisions{{
						Key:             "foo",
						ResourceVersion: "new",
						Revision:        ptr.To(123),
					}, {
						Key:             "bar",
						ResourceVersion: "another",
						Revision:        ptr.To(234),
					}},
				},
			},
		},
		{
			Name:        "revision match",
			Expectation: true,
			Composition: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 1,
				},
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{{Key: "foo"}, {Key: "bar"}},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{},
					InputRevisions: []apiv1.InputRevisions{{
						Key:             "foo",
						ResourceVersion: "new",
						Revision:        ptr.To(123),
					}, {
						Key:             "bar",
						ResourceVersion: "another",
						Revision:        ptr.To(123),
					}},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			syn := &apiv1.Synthesizer{}
			syn.Spec.Refs = []apiv1.Ref{{Key: "foo"}}
			assert.Equal(t, tc.Expectation, shouldSwapStates(syn, &tc.Composition))
		})
	}
}

func TestInputRevisionsEqual(t *testing.T) {
	synth := &apiv1.Synthesizer{}
	synth.Spec.Refs = []apiv1.Ref{{Key: "foo"}, {Key: "bar", Defer: true}, {Key: "baz"}}

	tcs := []struct {
		name  string
		a, b  []apiv1.InputRevisions
		equal bool
	}{
		{
			name:  "just keys",
			a:     []apiv1.InputRevisions{{Key: "foo"}, {Key: "baz"}},
			b:     []apiv1.InputRevisions{{Key: "foo"}, {Key: "baz"}},
			equal: true,
		},
		{
			name:  "resource version mismatch",
			a:     []apiv1.InputRevisions{{Key: "foo"}, {Key: "baz"}},
			b:     []apiv1.InputRevisions{{Key: "foo", ResourceVersion: "not-zero"}, {Key: "baz"}},
			equal: false,
		},
		{
			name:  "revision missong",
			a:     []apiv1.InputRevisions{{Key: "foo"}, {Key: "baz"}},
			b:     []apiv1.InputRevisions{{Key: "foo", Revision: ptr.To(123)}, {Key: "baz"}},
			equal: false,
		},
		{
			name:  "revision mismatch",
			a:     []apiv1.InputRevisions{{Key: "foo", Revision: ptr.To(234)}, {Key: "baz"}},
			b:     []apiv1.InputRevisions{{Key: "foo", Revision: ptr.To(123)}, {Key: "baz"}},
			equal: false,
		},
		{
			name:  "revision match",
			a:     []apiv1.InputRevisions{{Key: "foo", Revision: ptr.To(123)}, {Key: "baz"}},
			b:     []apiv1.InputRevisions{{Key: "foo", Revision: ptr.To(123)}, {Key: "baz"}},
			equal: true,
		},
		{
			name:  "resource version match",
			a:     []apiv1.InputRevisions{{Key: "foo", ResourceVersion: "not-zero"}, {Key: "baz"}},
			b:     []apiv1.InputRevisions{{Key: "foo", ResourceVersion: "not-zero"}, {Key: "baz"}},
			equal: true,
		},
		{
			name:  "mixed resource version and revision",
			a:     []apiv1.InputRevisions{{Key: "foo", ResourceVersion: "not-zero"}, {Key: "baz"}},
			b:     []apiv1.InputRevisions{{Key: "foo", Revision: ptr.To(123)}, {Key: "baz"}},
			equal: false,
		},
		{
			name:  "ignore deferred",
			a:     []apiv1.InputRevisions{{Key: "foo"}, {Key: "bar"}, {Key: "baz"}},
			b:     []apiv1.InputRevisions{{Key: "foo"}, {Key: "bar", ResourceVersion: "not-zero"}, {Key: "baz"}},
			equal: true,
		},
		{
			name:  "mismatched items with deferred",
			a:     []apiv1.InputRevisions{{Key: "foo"}, {Key: "baz"}},
			b:     []apiv1.InputRevisions{{Key: "bar"}, {Key: "baz"}},
			equal: false,
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.equal, inputRevisionsEqual(synth, tc.a, tc.b))
		})
	}

}

func TestInputRevisionsEqualOrdering(t *testing.T) {
	synth := &apiv1.Synthesizer{}
	synth.Spec.Refs = []apiv1.Ref{{Key: "foo"}, {Key: "bar"}}

	assert.True(t, inputRevisionsEqual(synth, []apiv1.InputRevisions{
		{Key: "bar"}, {Key: "foo"},
	}, []apiv1.InputRevisions{
		{Key: "foo"}, {Key: "bar"},
	}))
}
