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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestPodGCTerminatingCompositionNamespace(t *testing.T) {
	ctx := testutil.NewContext(t)
	now := metav1.Now()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:              "terminating",
		DeletionTimestamp: &now,
		Finalizers:        []string{"test-finalizer"},
	}}
	synth := &apiv1.Synthesizer{ObjectMeta: metav1.ObjectMeta{Name: "test-synth"}}
	comp := &apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{Name: "test-comp", Namespace: ns.Name},
		Spec:       apiv1.CompositionSpec{Synthesizer: apiv1.SynthesizerRef{Name: synth.Name}},
		Status:     apiv1.CompositionStatus{InFlightSynthesis: &apiv1.Synthesis{UUID: "test-synthesis"}},
	}
	pod := newPod(minimalTestConfig, comp, synth)
	pod.GenerateName = ""
	pod.Name = "test-pod"
	cli := testutil.NewClient(t, ns, synth, comp, pod)
	p := &podGarbageCollector{client: cli}

	_, err := p.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)})
	require.NoError(t, err)
	assert.True(t, errors.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(pod), pod)))
}

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
