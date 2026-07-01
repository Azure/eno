package composition

import (
	"context"
	"fmt"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const testPodNamespace = "eno-system"

// newInFlightComp builds a composition that is mid-synthesis: it has the cleanup
// finalizer, a synthesizer reference, an in-flight synthesis with the given UUID
// and Initialized time, and a pre-seeded "Synthesizing" simplified status so that
// reconcileSimplifiedStatus is a no-op and Reconcile reaches the timeout block.
func newInFlightComp(uuid string, initialized time.Time) *apiv1.Composition {
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{EnoCleanupFinalizer}
	comp.Spec.Synthesizer.Name = "test-syn"
	comp.Status.Simplified = &apiv1.SimplifiedStatus{Status: "Synthesizing"}
	comp.Status.InFlightSynthesis = &apiv1.Synthesis{
		UUID:        uuid,
		Initialized: ptr.To(metav1.NewTime(initialized)),
	}
	return comp
}

func newTestSynth() *apiv1.Synthesizer {
	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	return syn
}

// newSynthPod builds a synthesizer pod labeled with the synthesis UUID. A zero
// "created" time leaves CreationTimestamp unset (which reads as far in the past,
// i.e. the grace period has elapsed).
func newSynthPod(name, uuid string, phase corev1.PodPhase, created time.Time) *corev1.Pod {
	pod := &corev1.Pod{}
	pod.Name = name
	pod.Namespace = testPodNamespace
	pod.Labels = map[string]string{synthesisIDLabelKey: uuid}
	pod.Status.Phase = phase
	if !created.IsZero() {
		pod.CreationTimestamp = metav1.NewTime(created)
	}
	return pod
}

// newTerminatedSynthPod builds a Succeeded synthesizer pod whose container
// terminated at finishedAt, so tests can exercise the termination-time grace
// anchor independently of CreationTimestamp.
func newTerminatedSynthPod(name, uuid string, created, finishedAt time.Time) *corev1.Pod {
	pod := newSynthPod(name, uuid, corev1.PodSucceeded, created)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "synth",
		State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				FinishedAt: metav1.NewTime(finishedAt),
			},
		},
	}}
	return pod
}

func newFastCancelController(cli client.Client) *compositionController {
	return &compositionController{
		client:                cli,
		podTimeout:            time.Minute,
		synthesisPodNamespace: testPodNamespace,
	}
}

// pinFastCancelTimers sets the package-level fast-path timers for the duration of
// a test and restores them afterwards. Tests that assert grace/poll behavior use
// this so they don't depend on the ambient (possibly integration-mutated) value.
func pinFastCancelTimers(t *testing.T, grace, poll time.Duration) {
	origGrace, origPoll := podCompletionGracePeriod, inFlightPollInterval
	podCompletionGracePeriod = grace
	inFlightPollInterval = poll
	t.Cleanup(func() {
		podCompletionGracePeriod = origGrace
		inFlightPollInterval = origPoll
	})
}

// --- terminalInFlightPod unit matrix ---

func TestTerminalInFlightPod(t *testing.T) {
	const uuid = "uuid-1"

	tests := []struct {
		name         string
		inFlight     *apiv1.Synthesis
		pods         []client.Object
		wantTerminal bool
	}{
		{
			name:         "no in-flight synthesis",
			inFlight:     nil,
			wantTerminal: false,
		},
		{
			name:         "empty uuid",
			inFlight:     &apiv1.Synthesis{UUID: ""},
			wantTerminal: false,
		},
		{
			name:         "pod pending",
			inFlight:     &apiv1.Synthesis{UUID: uuid},
			pods:         []client.Object{newSynthPod("p", uuid, corev1.PodPending, time.Time{})},
			wantTerminal: false,
		},
		{
			name:         "pod running",
			inFlight:     &apiv1.Synthesis{UUID: uuid},
			pods:         []client.Object{newSynthPod("p", uuid, corev1.PodRunning, time.Time{})},
			wantTerminal: false,
		},
		{
			name:         "pod succeeded",
			inFlight:     &apiv1.Synthesis{UUID: uuid},
			pods:         []client.Object{newSynthPod("p", uuid, corev1.PodSucceeded, time.Time{})},
			wantTerminal: true,
		},
		{
			name:         "pod failed",
			inFlight:     &apiv1.Synthesis{UUID: uuid},
			pods:         []client.Object{newSynthPod("p", uuid, corev1.PodFailed, time.Time{})},
			wantTerminal: true,
		},
		{
			name:     "terminal but being deleted",
			inFlight: &apiv1.Synthesis{UUID: uuid},
			pods: []client.Object{func() client.Object {
				p := newSynthPod("p", uuid, corev1.PodSucceeded, time.Time{})
				now := metav1.Now()
				p.DeletionTimestamp = &now
				p.Finalizers = []string{"kubernetes"} // fake client requires a finalizer to retain a deleting object
				return p
			}()},
			wantTerminal: false,
		},
		{
			name:         "uuid mismatch",
			inFlight:     &apiv1.Synthesis{UUID: uuid},
			pods:         []client.Object{newSynthPod("p", "other-uuid", corev1.PodSucceeded, time.Time{})},
			wantTerminal: false,
		},
		{
			name:     "wrong namespace",
			inFlight: &apiv1.Synthesis{UUID: uuid},
			pods: []client.Object{func() client.Object {
				p := newSynthPod("p", uuid, corev1.PodSucceeded, time.Time{})
				p.Namespace = "somewhere-else"
				return p
			}()},
			wantTerminal: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := testutil.NewContext(t)
			cli := testutil.NewClient(t, tc.pods...)
			c := newFastCancelController(cli)

			comp := &apiv1.Composition{}
			comp.Status.InFlightSynthesis = tc.inFlight

			pod, terminal, err := c.terminalInFlightPod(ctx, comp)
			require.NoError(t, err)
			assert.Equal(t, tc.wantTerminal, terminal)
			if tc.wantTerminal {
				assert.NotNil(t, pod)
			} else {
				assert.Nil(t, pod)
			}
		})
	}
}

// --- Reconcile fast-cancel decision matrix ---

// Case 1: abandoned synthesis (terminal pod, status never advanced, grace elapsed)
// is cancelled immediately rather than waiting for podTimeout.
func TestFastCancelAbandonedSynthesis(t *testing.T) {
	ctx := testutil.NewContext(t)
	comp := newInFlightComp("uuid-1", time.Now()) // initialized just now: well within podTimeout
	pod := newSynthPod("synthesis-1", "uuid-1", corev1.PodSucceeded, time.Time{})

	cli := testutil.NewClient(t, comp, newTestSynth(), pod)
	c := newFastCancelController(cli)

	res, err := c.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)})
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
	assert.False(t, res.Requeue)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	require.NotNil(t, comp.Status.InFlightSynthesis)
	assert.NotNil(t, comp.Status.InFlightSynthesis.Canceled, "abandoned synthesis should be cancelled immediately")
}

// Case 2: a terminal pod still within the grace period requeues instead of
// cancelling, giving a late successful status write time to propagate.
func TestFastCancelWithinGraceRequeues(t *testing.T) {
	pinFastCancelTimers(t, time.Minute, inFlightPollInterval) // grace must be long enough that a now-created pod is within it
	ctx := testutil.NewContext(t)
	comp := newInFlightComp("uuid-1", time.Now())
	pod := newSynthPod("synthesis-1", "uuid-1", corev1.PodSucceeded, time.Now()) // created now -> within grace

	cli := testutil.NewClient(t, comp, newTestSynth(), pod)
	c := newFastCancelController(cli)

	res, err := c.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)})
	require.NoError(t, err)
	assert.Greater(t, res.RequeueAfter, time.Duration(0))
	assert.LessOrEqual(t, res.RequeueAfter, podCompletionGracePeriod)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Nil(t, comp.Status.InFlightSynthesis.Canceled, "should not cancel while within grace period")
}

// Case 2b: the grace window is measured from when the pod *terminated*, not when
// it was created. A pod created long ago but finished just now must still be
// within grace (the old creation-time anchor would have cancelled immediately).
func TestFastCancelGraceUsesTerminationTime(t *testing.T) {
	pinFastCancelTimers(t, time.Minute, inFlightPollInterval)
	ctx := testutil.NewContext(t)
	comp := newInFlightComp("uuid-1", time.Now())
	pod := newTerminatedSynthPod("synthesis-1", "uuid-1", time.Now().Add(-time.Hour), time.Now()) // created long ago, finished now

	cli := testutil.NewClient(t, comp, newTestSynth(), pod)
	c := newFastCancelController(cli)

	res, err := c.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)})
	require.NoError(t, err)
	assert.Greater(t, res.RequeueAfter, time.Duration(0), "grace must be measured from termination, not creation")

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Nil(t, comp.Status.InFlightSynthesis.Canceled, "should not cancel while within the post-termination grace window")
}

// TestPodTerminationTime covers the grace anchor helper directly.
func TestPodTerminationTime(t *testing.T) {
	created := time.Now().Add(-time.Hour)
	finished := time.Now().Add(-time.Minute)

	// No terminated state -> falls back to CreationTimestamp.
	assert.WithinDuration(t, created,
		podTerminationTime(newSynthPod("p", "u", corev1.PodSucceeded, created)), time.Second)

	// Single terminated container -> uses its FinishedAt.
	assert.WithinDuration(t, finished,
		podTerminationTime(newTerminatedSynthPod("p", "u", created, finished)), time.Second)

	// Multiple terminated containers -> uses the latest FinishedAt.
	multi := newTerminatedSynthPod("p", "u", created, finished)
	multi.Status.ContainerStatuses = append(multi.Status.ContainerStatuses, corev1.ContainerStatus{
		Name: "c2",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			FinishedAt: metav1.NewTime(finished.Add(30 * time.Second)),
		}},
	})
	assert.WithinDuration(t, finished.Add(30*time.Second), podTerminationTime(multi), time.Second)
}

// Case 3: when the synthesis has already advanced (InFlightSynthesis cleared by a
// successful executor swap), Reconcile must not cancel anything even though the
// pod is terminal.
func TestFastCancelSkippedWhenAdvanced(t *testing.T) {
	ctx := testutil.NewContext(t)

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{EnoCleanupFinalizer}
	comp.Spec.Synthesizer.Name = "test-syn"
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		UUID:       "uuid-1",
		Reconciled: ptr.To(metav1.Now()),
		Ready:      ptr.To(metav1.Now()),
	}
	pod := newSynthPod("synthesis-1", "uuid-1", corev1.PodSucceeded, time.Time{})

	cli := testutil.NewClient(t, comp, newTestSynth(), pod)
	c := newFastCancelController(cli)

	res, err := c.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)})
	require.NoError(t, err)
	assert.False(t, res.Requeue)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Nil(t, comp.Status.InFlightSynthesis, "no in-flight synthesis should be created")
	require.NotNil(t, comp.Status.CurrentSynthesis)
	assert.Equal(t, "uuid-1", comp.Status.CurrentSynthesis.UUID, "completed synthesis must be untouched")
	assert.Nil(t, comp.Status.CurrentSynthesis.Canceled)
}

// Case 4: a genuinely hung pod (never terminal) is still cancelled by the existing
// time-based timeout once podTimeout elapses.
func TestSynthesisTimeoutStillFires(t *testing.T) {
	ctx := testutil.NewContext(t)
	comp := newInFlightComp("uuid-1", time.Now().Add(-2*time.Minute)) // older than podTimeout (1m)
	pod := newSynthPod("synthesis-1", "uuid-1", corev1.PodRunning, time.Now().Add(-2*time.Minute))

	cli := testutil.NewClient(t, comp, newTestSynth(), pod)
	c := newFastCancelController(cli)

	res, err := c.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)})
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	require.NotNil(t, comp.Status.InFlightSynthesis)
	assert.NotNil(t, comp.Status.InFlightSynthesis.Canceled, "hung pod should still time out")
}

// Case 5: while the pod is running and within podTimeout, Reconcile polls on the
// short interval instead of sleeping the full timeout, and does not cancel.
func TestInFlightPollsWhilePodRunning(t *testing.T) {
	ctx := testutil.NewContext(t)
	comp := newInFlightComp("uuid-1", time.Now())
	pod := newSynthPod("synthesis-1", "uuid-1", corev1.PodRunning, time.Now())

	cli := testutil.NewClient(t, comp, newTestSynth(), pod)
	c := newFastCancelController(cli)

	res, err := c.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)})
	require.NoError(t, err)
	assert.Equal(t, inFlightPollInterval, res.RequeueAfter)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Nil(t, comp.Status.InFlightSynthesis.Canceled, "running pod within timeout must not be cancelled")
}

// Case 5b: with no pod at all, behavior is identical to a non-terminal pod: poll,
// do not cancel (absence is ambiguous and must not be treated as terminal).
func TestInFlightPollsWhenPodMissing(t *testing.T) {
	ctx := testutil.NewContext(t)
	comp := newInFlightComp("uuid-1", time.Now())

	cli := testutil.NewClient(t, comp, newTestSynth())
	c := newFastCancelController(cli)

	res, err := c.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)})
	require.NoError(t, err)
	assert.Equal(t, inFlightPollInterval, res.RequeueAfter)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Nil(t, comp.Status.InFlightSynthesis.Canceled, "missing pod must not be treated as terminal")
}

// Case 6: if the cancel write conflicts (a concurrent successful executor write
// bumped resourceVersion), Reconcile requeues instead of erroring or clobbering.
func TestFastCancelConflictRequeues(t *testing.T) {
	ctx := testutil.NewContext(t)
	comp := newInFlightComp("uuid-1", time.Now())
	pod := newSynthPod("synthesis-1", "uuid-1", corev1.PodSucceeded, time.Time{}) // grace elapsed

	ict := &interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			return apierrors.NewConflict(
				schema.GroupResource{Group: "eno.azure.io", Resource: "compositions"},
				obj.GetName(), fmt.Errorf("simulated conflict"))
		},
	}
	cli := testutil.NewClientWithInterceptors(t, ict, comp, newTestSynth(), pod)
	c := newFastCancelController(cli)

	res, err := c.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)})
	require.NoError(t, err, "conflict must not surface as an error")
	assert.True(t, res.Requeue, "conflict should requeue")

	// The conflicting write must not have landed: the (concurrently successful)
	// synthesis is left intact rather than clobbered with a cancellation.
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Nil(t, comp.Status.InFlightSynthesis.Canceled, "conflicted cancel must not be persisted")
}

// --- Functional progression: grace gate releases into a cancel ---

func TestFastCancelGraceThenCancel(t *testing.T) {
	pinFastCancelTimers(t, time.Minute, inFlightPollInterval) // grace must be long enough that a now-created pod is within it
	ctx := testutil.NewContext(t)
	comp := newInFlightComp("uuid-1", time.Now())
	pod := newSynthPod("synthesis-1", "uuid-1", corev1.PodSucceeded, time.Now()) // within grace

	cli := testutil.NewClient(t, comp, newTestSynth(), pod)
	c := newFastCancelController(cli)

	// First reconcile: within grace -> requeue, no cancel.
	res, err := c.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)})
	require.NoError(t, err)
	assert.Greater(t, res.RequeueAfter, time.Duration(0))
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	require.Nil(t, comp.Status.InFlightSynthesis.Canceled)

	// Simulate the grace period elapsing by replacing the pod with a backdated one.
	require.NoError(t, cli.Delete(ctx, pod))
	agedPod := newSynthPod("synthesis-1", "uuid-1", corev1.PodSucceeded, time.Now().Add(-time.Hour))
	require.NoError(t, cli.Create(ctx, agedPod))

	// Second reconcile: grace elapsed -> cancel.
	_, err = c.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)})
	require.NoError(t, err)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.NotNil(t, comp.Status.InFlightSynthesis.Canceled, "should cancel once grace has elapsed")
}
