package scheduling

import (
	"strings"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	prometheustestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

var stuckTestNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func stuckTestTime(ago time.Duration) *metav1.Time {
	return &metav1.Time{Time: stuckTestNow.Add(-ago)}
}

func stuckTestSynth() *apiv1.Synthesizer {
	synth := &apiv1.Synthesizer{}
	synth.Name = "synth"
	synth.Annotations = map[string]string{"eno.azure.io/owner": "team"}
	synth.Spec.Refs = []apiv1.Ref{{Key: "a"}}
	return synth
}

func stuckTestComp() *apiv1.Composition {
	comp := &apiv1.Composition{}
	comp.Name = "comp"
	comp.Namespace = "ns"
	comp.CreationTimestamp = *stuckTestTime(time.Hour)
	comp.Spec.Synthesizer.Name = "synth"
	comp.Status.InputRevisions = []apiv1.InputRevisions{{Key: "a"}}
	return comp
}

func stuckTestReadySynthesis() *apiv1.Synthesis {
	return &apiv1.Synthesis{
		UUID:        "current",
		Initialized: stuckTestTime(50 * time.Minute),
		Synthesized: stuckTestTime(40 * time.Minute),
		Reconciled:  stuckTestTime(30 * time.Minute),
		Ready:       stuckTestTime(20 * time.Minute),
	}
}

func TestClassifyComposition(t *testing.T) {
	created := stuckTestNow.Add(-time.Hour)
	tests := []struct {
		name       string
		mutate     func(comp *apiv1.Composition, synth *apiv1.Synthesizer)
		noSynth    bool
		readySet   map[string]bool
		cyclicSet  map[string]bool
		wantReason string
		wantSince  time.Time
	}{
		{
			name: "deleting wins over every other state",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				comp.DeletionTimestamp = stuckTestTime(5 * time.Minute)
				comp.Status.InputRevisions = nil
			},
			cyclicSet:  map[string]bool{"ns/comp": true},
			wantReason: stuckReasonDeleting,
			wantSince:  stuckTestNow.Add(-5 * time.Minute),
		},
		{
			name:       "missing synthesizer",
			noSynth:    true,
			wantReason: stuckReasonMissingSynthesizer,
			wantSince:  created,
		},
		{
			name: "circular dependency even with a ready synthesis",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				comp.Status.CurrentSynthesis = stuckTestReadySynthesis()
			},
			cyclicSet:  map[string]bool{"ns/comp": true},
			wantReason: stuckReasonCircularDependency,
			wantSince:  created,
		},
		{
			name: "waiting on dependencies before first synthesis",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				comp.Spec.DependsOn = []apiv1.CompositionDependency{{Name: "dep", Namespace: "ns"}}
			},
			wantReason: stuckReasonWaitingOnDependencies,
			wantSince:  created,
		},
		{
			name: "ready dependencies fall through to pending synthesis",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				comp.Spec.DependsOn = []apiv1.CompositionDependency{{Name: "dep", Namespace: "ns"}}
			},
			readySet:   map[string]bool{"ns/dep": true},
			wantReason: stuckReasonPendingSynthesis,
			wantSince:  created,
		},
		{
			name: "unready dependencies are ignored once a synthesis exists",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				comp.Spec.DependsOn = []apiv1.CompositionDependency{{Name: "dep", Namespace: "ns"}}
				comp.Status.CurrentSynthesis = stuckTestReadySynthesis()
			},
			wantReason: "",
		},
		{
			name: "missing inputs before first synthesis",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				comp.Status.InputRevisions = nil
			},
			wantReason: stuckReasonMissingInputs,
			wantSince:  created,
		},
		{
			name: "missing inputs are ignored once a synthesis exists",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				comp.Status.InputRevisions = nil
				comp.Status.CurrentSynthesis = stuckTestReadySynthesis()
			},
			wantReason: "",
		},
		{
			name: "mismatched inputs before first synthesis",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				synth.Spec.Refs = []apiv1.Ref{{Key: "a"}, {Key: "b"}}
				comp.Status.InputRevisions = []apiv1.InputRevisions{
					{Key: "a", Revision: ptr.To(1)},
					{Key: "b", Revision: ptr.To(2)},
				}
			},
			wantReason: stuckReasonMismatchedInputs,
			wantSince:  created,
		},
		{
			name: "synthesis backoff on a ready composition",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				comp.Status.CurrentSynthesis = stuckTestReadySynthesis()
				comp.Status.InFlightSynthesis = &apiv1.Synthesis{
					Initialized: stuckTestTime(10 * time.Minute),
					Canceled:    stuckTestTime(4 * time.Minute),
				}
			},
			wantReason: stuckReasonSynthesisBackoff,
			wantSince:  stuckTestNow.Add(-4 * time.Minute),
		},
		{
			name: "resynthesis in flight on a ready composition",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				comp.Status.CurrentSynthesis = stuckTestReadySynthesis()
				comp.Status.InFlightSynthesis = &apiv1.Synthesis{Initialized: stuckTestTime(10 * time.Minute)}
			},
			wantReason: stuckReasonSynthesizing,
			wantSince:  stuckTestNow.Add(-10 * time.Minute),
		},
		{
			name: "pending resynthesis after a composition update",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				comp.Finalizers = []string{"eno.azure.io/cleanup"}
				comp.Generation = 2
				syn := stuckTestReadySynthesis()
				syn.ObservedCompositionGeneration = 1
				comp.Status.CurrentSynthesis = syn
			},
			wantReason: stuckReasonPendingSynthesis,
			wantSince:  time.Time{},
		},
		{
			name: "deferred synthesizer rollout is not pending",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				comp.Finalizers = []string{"eno.azure.io/cleanup"}
				synth.Generation = 2
				syn := stuckTestReadySynthesis()
				syn.ObservedSynthesizerGeneration = 1
				comp.Status.CurrentSynthesis = syn
			},
			wantReason: "",
		},
		{
			name:       "pending first synthesis",
			wantReason: stuckReasonPendingSynthesis,
			wantSince:  created,
		},
		{
			name: "waiting on tombstone recovery",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				comp.Annotations = map[string]string{"eno.azure.io/recovery-enabled": "true"}
				comp.Status.CurrentSynthesis = &apiv1.Synthesis{
					UUID:        "current",
					Initialized: stuckTestTime(10 * time.Minute),
					Synthesized: stuckTestTime(8 * time.Minute),
				}
			},
			wantReason: stuckReasonTombstoneRecovery,
			wantSince:  stuckTestNow.Add(-8 * time.Minute),
		},
		{
			name: "finished tombstone recovery falls through to reconciling",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				comp.Annotations = map[string]string{"eno.azure.io/recovery-enabled": "true"}
				comp.Status.CurrentSynthesis = &apiv1.Synthesis{
					UUID:                    "current",
					Synthesized:             stuckTestTime(8 * time.Minute),
					TombstoneRecoveryStatus: &apiv1.TombstoneRecoveryStatus{SynthesisUUID: "current", Status: true},
				}
			},
			wantReason: stuckReasonReconciling,
			wantSince:  stuckTestNow.Add(-8 * time.Minute),
		},
		{
			name: "reconciling without recovery opt-in",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				comp.Status.CurrentSynthesis = &apiv1.Synthesis{
					UUID:        "current",
					Synthesized: stuckTestTime(8 * time.Minute),
				}
			},
			wantReason: stuckReasonReconciling,
			wantSince:  stuckTestNow.Add(-8 * time.Minute),
		},
		{
			name: "not ready",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				syn := stuckTestReadySynthesis()
				syn.Ready = nil
				comp.Status.CurrentSynthesis = syn
			},
			wantReason: stuckReasonNotReady,
			wantSince:  stuckTestNow.Add(-30 * time.Minute),
		},
		{
			name: "ready",
			mutate: func(comp *apiv1.Composition, synth *apiv1.Synthesizer) {
				comp.Status.CurrentSynthesis = stuckTestReadySynthesis()
			},
			wantReason: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			comp, synth := stuckTestComp(), stuckTestSynth()
			if tc.mutate != nil {
				tc.mutate(comp, synth)
			}
			if tc.noSynth {
				synth = nil
			}
			reason, since := classifyComposition(comp, synth, tc.readySet, tc.cyclicSet)
			assert.Equal(t, tc.wantReason, reason)
			if tc.wantReason != "" {
				assert.True(t, tc.wantSince.Equal(since), "since: want %s, got %s", tc.wantSince, since)
			}
		})
	}
}

func TestPlanWaitingCompositions(t *testing.T) {
	synth := stuckTestSynth()
	synths := map[string]apiv1.Synthesizer{synth.Name: *synth}

	reconciling := stuckTestComp()
	reconciling.Name = "reconciling"
	reconciling.Status.CurrentSynthesis = &apiv1.Synthesis{UUID: "a", Synthesized: stuckTestTime(time.Minute)}

	ready := stuckTestComp()
	ready.Name = "ready"
	ready.Status.CurrentSynthesis = stuckTestReadySynthesis()

	pending := stuckTestComp()
	pending.Name = "pending"
	pending.UID = "pending-uid"
	pending.Finalizers = []string{"eno.azure.io/cleanup"}
	pending.Generation = 2
	pending.Status.CurrentSynthesis = stuckTestReadySynthesis()
	pending.Status.CurrentSynthesis.ObservedCompositionGeneration = 1

	comps := []apiv1.Composition{*reconciling, *ready, *pending}
	stale := map[types.UID]time.Time{"deleted-uid": stuckTestNow.Add(-time.Hour)}

	// First observation of the pending resynthesis starts its clock at now.
	waiting, pendingSince := planWaitingCompositions(comps, synths, nil, nil, stale, stuckTestNow)
	assert.Equal(t, []waitingComposition{
		{name: "reconciling", namespace: "ns", synthesizer: "synth", owner: "team", reason: stuckReasonReconciling, since: stuckTestNow.Add(-time.Minute)},
		{name: "pending", namespace: "ns", synthesizer: "synth", owner: "team", reason: stuckReasonPendingSynthesis, since: stuckTestNow},
	}, waiting)
	assert.Equal(t, map[types.UID]time.Time{"pending-uid": stuckTestNow}, pendingSince)

	// Later evaluations keep the first-observed time.
	later := stuckTestNow.Add(10 * time.Minute)
	waiting, pendingSince = planWaitingCompositions(comps, synths, nil, nil, pendingSince, later)
	assert.Equal(t, stuckTestNow, waiting[1].since)
	assert.Equal(t, map[types.UID]time.Time{"pending-uid": stuckTestNow}, pendingSince)

	// The entry is dropped once the resynthesis is dispatched.
	pending.Status.InFlightSynthesis = &apiv1.Synthesis{Initialized: &metav1.Time{Time: later}}
	_, pendingSince = planWaitingCompositions([]apiv1.Composition{*pending}, synths, nil, nil, pendingSince, later)
	assert.Empty(t, pendingSince)
}

func TestStuckCompositionsCollector(t *testing.T) {
	t.Cleanup(func() {
		stuckCompositionsSnapshot.Store(nil)
	})
	now := stuckTestNow
	collector := stuckCompositionsCollector{now: func() time.Time {
		return now
	}}
	threshold := 3 * time.Minute

	stuckCompositionsSnapshot.Store(nil)
	assert.Equal(t, 0, prometheustestutil.CollectAndCount(collector))

	publishWaitingCompositions(threshold, []waitingComposition{
		{name: "a", namespace: "ns1", synthesizer: "synth", owner: "team", reason: stuckReasonReconciling, since: now.Add(-10 * time.Minute)},
		{name: "b", namespace: "ns2", synthesizer: "synth", owner: "team", reason: stuckReasonReconciling, since: now.Add(-threshold - time.Second)},
		{name: "c", namespace: "ns1", synthesizer: "other", owner: "", reason: stuckReasonNotReady, since: now.Add(-time.Hour)},
		{name: "d", namespace: "ns1", synthesizer: "other", owner: "", reason: stuckReasonMissingSynthesizer, since: now.Add(-threshold)},
	})

	expected := `
# HELP eno_composition_stuck_reason Set to 1 for each composition stuck in one phase longer than the watchdog threshold, labeled with the reason
# TYPE eno_composition_stuck_reason gauge
eno_composition_stuck_reason{composition_name="a",composition_namespace="ns1",reason="Reconciling",synthesizer_name="synth"} 1
eno_composition_stuck_reason{composition_name="b",composition_namespace="ns2",reason="Reconciling",synthesizer_name="synth"} 1
eno_composition_stuck_reason{composition_name="c",composition_namespace="ns1",reason="NotReady",synthesizer_name="other"} 1
# HELP eno_compositions_stuck Number of compositions stuck in one phase longer than the watchdog threshold, partitioned by synthesizer, owner, and reason
# TYPE eno_compositions_stuck gauge
eno_compositions_stuck{owner="",reason="NotReady",synthesizer="other"} 1
eno_compositions_stuck{owner="team",reason="Reconciling",synthesizer="synth"} 2
`
	require.NoError(t, prometheustestutil.CollectAndCompare(collector, strings.NewReader(expected)))

	// Without a new snapshot, advancing time alone makes "d" cross the threshold.
	now = now.Add(time.Second)
	expected = `
# HELP eno_composition_stuck_reason Set to 1 for each composition stuck in one phase longer than the watchdog threshold, labeled with the reason
# TYPE eno_composition_stuck_reason gauge
eno_composition_stuck_reason{composition_name="a",composition_namespace="ns1",reason="Reconciling",synthesizer_name="synth"} 1
eno_composition_stuck_reason{composition_name="b",composition_namespace="ns2",reason="Reconciling",synthesizer_name="synth"} 1
eno_composition_stuck_reason{composition_name="c",composition_namespace="ns1",reason="NotReady",synthesizer_name="other"} 1
eno_composition_stuck_reason{composition_name="d",composition_namespace="ns1",reason="MissingSynthesizer",synthesizer_name="other"} 1
# HELP eno_compositions_stuck Number of compositions stuck in one phase longer than the watchdog threshold, partitioned by synthesizer, owner, and reason
# TYPE eno_compositions_stuck gauge
eno_compositions_stuck{owner="",reason="MissingSynthesizer",synthesizer="other"} 1
eno_compositions_stuck{owner="",reason="NotReady",synthesizer="other"} 1
eno_compositions_stuck{owner="team",reason="Reconciling",synthesizer="synth"} 2
`
	require.NoError(t, prometheustestutil.CollectAndCompare(collector, strings.NewReader(expected)))

	// A new snapshot fully replaces the previous one.
	publishWaitingCompositions(threshold, nil)
	assert.Equal(t, 0, prometheustestutil.CollectAndCount(collector))
}
