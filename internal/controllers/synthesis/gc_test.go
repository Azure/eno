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
	"k8s.io/client-go/util/retry"
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
	synth.Spec.PodTimeout = &metav1.Duration{Duration: time.Hour}
	require.NoError(t, cli.Create(ctx, synth))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = synth.Name
	require.NoError(t, cli.Create(ctx, comp))

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "anything"}
		return cli.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	pod := newPod(minimalTestConfig, comp, synth)
	require.NoError(t, cli.Create(ctx, pod))

	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(pod), pod)
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}}
		return cli.Status().Update(ctx, pod)
	})
	require.NoError(t, err)

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

func TestCheckImagePullError(t *testing.T) {
	tests := []struct {
		name           string
		pod            *corev1.Pod
		expectedError  string
		expectedResult bool
	}{
		{
			name: "no image pull error",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "container1",
							State: corev1.ContainerState{
								Running: &corev1.ContainerStateRunning{},
							},
						},
					},
				},
			},
			expectedError:  "",
			expectedResult: false,
		},
		{
			name: "with image pull error",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "container1",
							State: corev1.ContainerState{
								Waiting: &corev1.ContainerStateWaiting{
									Reason:  "ErrImagePull",
									Message: "failed to pull image: rpc error: code = Unknown desc = error pulling image",
								},
							},
						},
					},
				},
			},
			expectedError:  "failed to pull image: rpc error: code = Unknown desc = error pulling image",
			expectedResult: true,
		},
		{
			name: "with image pull backoff error",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "container1",
							State: corev1.ContainerState{
								Waiting: &corev1.ContainerStateWaiting{
									Reason: "ImagePullBackOff",
								},
							},
						},
					},
				},
			},
			expectedError:  "Container container1 failed to pull image: ImagePullBackOff",
			expectedResult: true,
		},
		{
			name: "with init container image pull error",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					InitContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "init-container",
							State: corev1.ContainerState{
								Waiting: &corev1.ContainerStateWaiting{
									Reason:  "ErrImagePull",
									Message: "failed to pull init container image",
								},
							},
						},
					},
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "container1",
							State: corev1.ContainerState{
								Waiting: &corev1.ContainerStateWaiting{
									Reason: "PodInitializing",
								},
							},
						},
					},
				},
			},
			expectedError:  "failed to pull init container image",
			expectedResult: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errorMsg, hasError := checkImagePullError(tt.pod)
			assert.Equal(t, tt.expectedResult, hasError)
			assert.Equal(t, tt.expectedError, errorMsg)
		})
	}
}
