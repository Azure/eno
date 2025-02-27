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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

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
				PendingSynthesis: &apiv1.Synthesis{
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
				PendingSynthesis: &apiv1.Synthesis{
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
				PendingSynthesis: &apiv1.Synthesis{
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
				PendingSynthesis: &apiv1.Synthesis{},
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
				PendingSynthesis: &apiv1.Synthesis{},
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
				CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute * 2)),
				Labels:            map[string]string{},
			},
			Status: corev1.PodStatus{},
		}},
		Composition: &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				PendingSynthesis: &apiv1.Synthesis{},
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
				CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute * 2)),
				Labels:            map[string]string{},
			},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{}}},
		}},
		Composition: &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				PendingSynthesis: &apiv1.Synthesis{},
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
		Name: "container-timeout-another-pod-deleting",
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
				PendingSynthesis: &apiv1.Synthesis{},
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
		Name: "container-timeout-too-many-retries",
		Pods: []corev1.Pod{{
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
				PendingSynthesis: &apiv1.Synthesis{Attempts: 4},
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
				PendingSynthesis: &apiv1.Synthesis{},
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
