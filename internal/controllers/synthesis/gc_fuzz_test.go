package synthesis

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/Azure/eno/internal/testutil/statespace"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type podGCState struct {
	Pod   *corev1.Pod
	Comp  *apiv1.Composition
	Synth *apiv1.Synthesizer
}

func TestPodGCDoesNotPanic(t *testing.T) {
	ctx := context.Background() // no logger on purpose
	const creationTimeout = time.Second

	statespace.Test(func(state *podGCState) bool {
		objs := []client.Object{state.Pod}
		if state.Comp != nil {
			objs = append(objs, state.Comp)
		}
		if state.Synth != nil {
			objs = append(objs, state.Synth)
		}
		cli := testutil.NewClient(t, objs...)

		p := &podGarbageCollector{client: cli, creationTimeout: creationTimeout}
		_, err := p.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: state.Pod.Name, Namespace: state.Pod.Namespace}})
		require.NoError(t, err)
		return true
	}).
		WithInitialState(func() *podGCState {
			state := &podGCState{
				Pod:   &corev1.Pod{},
				Comp:  &apiv1.Composition{},
				Synth: &apiv1.Synthesizer{},
			}

			state.Synth.Name = "test-synth"
			state.Synth.Namespace = "test-ns"

			state.Comp.Name = "test-comp"
			state.Comp.Namespace = state.Synth.Namespace
			state.Comp.Spec.Synthesizer.Name = state.Synth.Name
			state.Comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "in-flight-uuid"}

			// Pod is running by default
			state.Pod = newPod(minimalTestConfig, state.Comp, state.Synth)
			state.Pod.Name = "test-pod"
			state.Pod.Namespace = state.Synth.Namespace
			state.Pod.Status.ContainerStatuses = []corev1.ContainerStatus{{}}
			state.Pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()}}

			// For whatever reason, fake clients require a finalizer when deletion timestamp is set
			state.Pod.Finalizers = []string{"test.azure.io/finalizer"}
			state.Comp.Finalizers = []string{"test.azure.io/finalizer"}
			state.Synth.Finalizers = []string{"test.azure.io/finalizer"}

			return state
		}).
		WithMutation("pod succeeded", func(pg *podGCState) *podGCState {
			pg.Pod.Status.Phase = corev1.PodSucceeded
			return pg
		}).
		WithMutation("not seen by kubelet", func(pg *podGCState) *podGCState {
			pg.Pod.Status.ContainerStatuses = nil
			return pg
		}).
		WithMutation("not scheduled", func(pg *podGCState) *podGCState {
			pg.Pod.Status.Conditions = nil
			return pg
		}).
		WithMutation("scheduled 1hr ago", func(pg *podGCState) *podGCState {
			if len(pg.Pod.Status.Conditions) > 0 {
				pg.Pod.Status.Conditions[0].LastTransitionTime = metav1.NewTime(time.Now().Add(-time.Hour))
			}
			return pg
		}).
		WithMutation("nil synthesis", func(pg *podGCState) *podGCState {
			if pg.Comp != nil {
				pg.Comp.Status.InFlightSynthesis = nil
			}
			return pg
		}).
		WithMutation("composition deleted", func(pg *podGCState) *podGCState {
			pg.Comp = nil
			return pg
		}).
		WithMutation("synth deleted", func(pg *podGCState) *podGCState {
			pg.Synth = nil
			return pg
		}).
		WithMutation("no labels", func(pg *podGCState) *podGCState {
			pg.Pod.Labels = nil
			return pg
		}).
		WithMutation("missing name label", func(pg *podGCState) *podGCState {
			if pg.Pod.Labels != nil {
				delete(pg.Pod.Labels, compositionNameLabelKey)
			}
			return pg
		}).
		WithMutation("no synthesizer", func(pg *podGCState) *podGCState {
			if pg.Comp != nil {
				pg.Comp.Spec.Synthesizer.Name = ""
			}
			return pg
		}).
		WithMutation("pod deleting", func(pg *podGCState) *podGCState {
			pg.Pod.DeletionTimestamp = &metav1.Time{}
			return pg
		}).
		WithMutation("no synthesis uuid", func(pg *podGCState) *podGCState {
			if pg.Comp != nil && pg.Comp.Status.InFlightSynthesis != nil {
				pg.Comp.Status.InFlightSynthesis.UUID = ""
			}
			return pg
		}).
		WithInvariant("doesn't panic", func(pg *podGCState, res bool) bool {
			// Given the structure of the logic, it doesn't make sense to test the behavior here.
			// But fuzz tests are still useful for uncovering panics.
			return res
		}).
		Evaluate(t)
}
