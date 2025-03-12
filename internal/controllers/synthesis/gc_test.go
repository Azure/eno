package synthesis

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestPodGCMissingSynthesis(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewPodGC(mgr.Manager, 0))
	mgr.Start(t)

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-syn"
	synth.Spec.Image = "test-syn-image"
	require.NoError(t, cli.Create(ctx, synth))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = synth.Name
	require.NoError(t, cli.Create(ctx, comp))

	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "anything"}
	pod := newPod(minimalTestConfig, comp, synth)
	require.NoError(t, cli.Create(ctx, pod))

	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(pod), pod))
	})
}

func TestPodGCContainerCreationTimeout(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewPodGC(mgr.Manager, time.Millisecond*10))
	mgr.Start(t)

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-syn"
	synth.Spec.Image = "test-syn-image"
	require.NoError(t, cli.Create(ctx, synth))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = synth.Name
	require.NoError(t, cli.Create(ctx, comp))

	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "anything"}
	pod := newPod(minimalTestConfig, comp, synth)
	require.NoError(t, cli.Create(ctx, pod))

	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}}
	require.NoError(t, cli.Status().Update(ctx, pod))

	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(pod), pod))
	})
}

func TestTimeWaitingForKubelet(t *testing.T) {
	now := time.Now()
	tests := []struct {
		Name     string
		Pod      *corev1.Pod
		Now      time.Time
		Expected time.Duration
	}{
		{
			Name: "Pod with container statuses",
			Pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{},
					},
				},
			},
			Now:      now,
			Expected: 0,
		},
		{
			Name: "Pod not scheduled",
			Pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			Now:      now,
			Expected: 0,
		},
		{
			Name: "Pod scheduled",
			Pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:               corev1.PodScheduled,
							Status:             corev1.ConditionTrue,
							LastTransitionTime: metav1.Time{Time: now.Add(-5 * time.Minute)},
						},
					},
				},
			},
			Now:      now,
			Expected: 5 * time.Minute,
		},
		{
			Name: "Pod with no conditions",
			Pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{},
				},
			},
			Now:      now,
			Expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			result := timeWaitingForKubelet(tt.Pod, tt.Now)
			assert.Equal(t, tt.Expected, result)
		})
	}
}

func TestContainerExitedTime(t *testing.T) {
	ts := time.Now()
	tests := []struct {
		Name string
		Pod  *corev1.Pod
		Exp  *time.Time
	}{
		{
			Name: "zero pod",
			Pod:  &corev1.Pod{},
			Exp:  nil,
		},
		{
			Name: "one non-terminated container status",
			Pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{{}},
				},
			},
			Exp: nil,
		},
		{
			Name: "one terminated container status",
			Pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{{
						LastTerminationState: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								FinishedAt: metav1.NewTime(ts),
							},
						},
					}},
				},
			},
			Exp: &ts,
		},
		{
			Name: "one terminated container status, one non-terminated",
			Pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							LastTerminationState: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									FinishedAt: metav1.NewTime(ts),
								},
							},
						},
						{},
					},
				},
			},
			Exp: &ts,
		},
		{
			Name: "two terminated container status",
			Pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							LastTerminationState: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									FinishedAt: metav1.NewTime(ts),
								},
							},
						},
						{
							LastTerminationState: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									FinishedAt: metav1.NewTime(time.Date(1, 0, 0, 0, 0, 0, 0, time.UTC)),
								},
							},
						},
					},
				},
			},
			Exp: &ts,
		},
	}

	for _, test := range tests {
		ts := containerExitedTime(test.Pod)
		assert.Equal(t, test.Exp, ts)
	}
}
