package composition

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// newSynthPodForCache builds a synthesizer pod the way the pod lifecycle
// controller does, including the manager label. That label is required for the
// pod to be admitted into the controller's namespace-and-label-scoped cache, so
// succeededInFlightPod (a cached List) can see it.
func newSynthPodForCache(name, uuid string) *corev1.Pod {
	pod := &corev1.Pod{}
	pod.Name = name
	pod.Namespace = "default"
	pod.Labels = map[string]string{
		manager.SynthesisIDLabelKey: uuid,
		manager.ManagerLabelKey:     manager.ManagerLabelValue,
	}
	pod.Spec.Containers = []corev1.Container{{Name: "synth", Image: "test-image"}}
	return pod
}

// TestIntegrationFastCancelAbandonedSynthesis proves, against a real manager and
// informer cache, that an in-flight synthesis whose pod has terminated without
// advancing the status (the skipSynthesis early-return case) is cancelled
// promptly by the fast path rather than waiting for podTimeout.
//
// It wires only the composition controller and drives the "terminal pod, status
// never advanced" state directly, so the assertion isolates the fast-cancel
// behavior. podTimeout is set to an hour, so the only thing that can set Canceled
// within the test window is the fast path.
func TestIntegrationFastCancelAbandonedSynthesis(t *testing.T) {
	// Shrink the fast-path timers so the cancel is observable well within
	// testutil.Eventually's window. Production defaults are unchanged.
	origGrace, origPoll := podCompletionGracePeriod, inFlightPollInterval
	podCompletionGracePeriod = 100 * time.Millisecond
	inFlightPollInterval = 100 * time.Millisecond
	t.Cleanup(func() {
		podCompletionGracePeriod = origGrace
		inFlightPollInterval = origPoll
	})

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t, testutil.WithPodNamespace("default"))
	cli := mgr.GetClient()
	apiReader := mgr.GetAPIReader() // uncached: avoids read-after-write cache lag

	require.NoError(t, NewController(mgr.Manager, time.Hour, "default"))
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "test-image"
	require.NoError(t, cli.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{EnoCleanupFinalizer}
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, cli.Create(ctx, comp))

	const uuid = "abandoned-uuid"

	// Drive the composition into the dangling in-flight state.
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := apiReader.Get(ctx, client.ObjectKeyFromObject(comp), comp); err != nil {
			return err
		}
		comp.Status.InFlightSynthesis = &apiv1.Synthesis{
			UUID:        uuid,
			Initialized: ptr.To(metav1.Now()),
		}
		return cli.Status().Update(ctx, comp)
	}))

	// Create a terminal pod that never advanced the status (simulates the
	// executor's skipSynthesis early return: exit 0, no status write).
	pod := newSynthPodForCache("synthesis-abandoned", uuid)
	require.NoError(t, cli.Create(ctx, pod))
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := apiReader.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
			return err
		}
		pod.Status.Phase = corev1.PodSucceeded
		return cli.Status().Update(ctx, pod)
	}))

	// The fast path must cancel the abandoned synthesis far sooner than the 1h
	// podTimeout.
	testutil.Eventually(t, func() bool {
		if err := apiReader.Get(ctx, client.ObjectKeyFromObject(comp), comp); err != nil {
			return false
		}
		return comp.Status.InFlightSynthesis != nil && comp.Status.InFlightSynthesis.Canceled != nil
	})
}

// TestIntegrationFastCancelLeavesSuccessAlone proves the inverse: when the
// executor advanced the status (success swap to CurrentSynthesis) the fast path
// must not cancel anything, even though the pod is terminal.
func TestIntegrationFastCancelLeavesSuccessAlone(t *testing.T) {
	origGrace, origPoll := podCompletionGracePeriod, inFlightPollInterval
	podCompletionGracePeriod = 100 * time.Millisecond
	inFlightPollInterval = 100 * time.Millisecond
	t.Cleanup(func() {
		podCompletionGracePeriod = origGrace
		inFlightPollInterval = origPoll
	})

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t, testutil.WithPodNamespace("default"))
	cli := mgr.GetClient()
	apiReader := mgr.GetAPIReader()

	require.NoError(t, NewController(mgr.Manager, time.Hour, "default"))
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "test-image"
	require.NoError(t, cli.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{EnoCleanupFinalizer}
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, cli.Create(ctx, comp))

	const uuid = "succeeded-uuid"

	// Advanced/successful state: no in-flight synthesis, current synthesis matches
	// the pod's UUID.
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := apiReader.Get(ctx, client.ObjectKeyFromObject(comp), comp); err != nil {
			return err
		}
		comp.Status.CurrentSynthesis = &apiv1.Synthesis{
			UUID:        uuid,
			Initialized: ptr.To(metav1.Now()),
			Synthesized: ptr.To(metav1.Now()),
		}
		comp.Status.InFlightSynthesis = nil
		return cli.Status().Update(ctx, comp)
	}))

	pod := newSynthPodForCache("synthesis-succeeded", uuid)
	require.NoError(t, cli.Create(ctx, pod))
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := apiReader.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
			return err
		}
		pod.Status.Phase = corev1.PodSucceeded
		return cli.Status().Update(ctx, pod)
	}))

	// Give the controller time to reconcile a few poll intervals, then assert the
	// completed synthesis was never disturbed.
	time.Sleep(time.Second)
	require.NoError(t, apiReader.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	require.Nil(t, comp.Status.InFlightSynthesis, "no in-flight synthesis should be created")
	require.NotNil(t, comp.Status.CurrentSynthesis)
	require.Equal(t, uuid, comp.Status.CurrentSynthesis.UUID)
	require.Nil(t, comp.Status.CurrentSynthesis.Canceled)
}
