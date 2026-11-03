package tombstonerecovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	prometheustestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var metricsTestNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func resetRecoveryMetrics(t *testing.T) {
	t.Helper()
	reset := func() {
		recoveryDecisions.Reset()
		recoveryTombstones.Reset()
		recoveryWait.Reset()
		pendingCompositionsFn.Store(nil)
		pendingCompositionDetailsFn.Store(nil)
	}
	reset()
	t.Cleanup(reset)
}

func pendingTestComposition(name string, waited time.Duration) *apiv1.Composition {
	synthesized := metav1.NewTime(metricsTestNow.Add(-waited))
	return &apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "control",
			Annotations: map[string]string{"eno.azure.io/recovery-enabled": "true"},
		},
		Spec: apiv1.CompositionSpec{Synthesizer: apiv1.SynthesizerRef{Name: "synthesizer"}},
		Status: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{
			UUID: "current", Synthesized: &synthesized,
		}},
	}
}

func pendingTestReader(t *testing.T, comps ...*apiv1.Composition) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.SchemeBuilder.AddToScheme(scheme))
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, comp := range comps {
		builder = builder.WithObjects(comp)
	}
	return builder.Build()
}

func TestListPendingCompositions(t *testing.T) {
	threshold := time.Minute

	pastThreshold := pendingTestComposition("past-threshold", threshold+time.Second)
	atThreshold := pendingTestComposition("at-threshold", threshold)
	notOptedIn := pendingTestComposition("not-opted-in", time.Hour)
	notOptedIn.Annotations = nil
	deleting := pendingTestComposition("deleting", time.Hour)
	deleting.Finalizers = []string{"eno.azure.io/cleanup"}
	deleting.DeletionTimestamp = &metav1.Time{Time: metricsTestNow}
	finished := pendingTestComposition("finished", time.Hour)
	finished.Status.CurrentSynthesis.TombstoneRecoveryStatus = &apiv1.TombstoneRecoveryStatus{SynthesisUUID: "current", Status: true}
	staleDecision := pendingTestComposition("stale-decision", time.Hour)
	staleDecision.Status.CurrentSynthesis.TombstoneRecoveryStatus = &apiv1.TombstoneRecoveryStatus{SynthesisUUID: "previous", Status: true}
	notSynthesized := pendingTestComposition("not-synthesized", time.Hour)
	notSynthesized.Status.CurrentSynthesis.Synthesized = nil

	reader := pendingTestReader(t, pastThreshold, atThreshold, notOptedIn, deleting, finished, staleDecision, notSynthesized)
	pending := listPendingCompositions(logr.Discard(), reader, threshold, metricsTestNow)

	var names []string
	for _, comp := range pending {
		names = append(names, comp.Name)
	}
	assert.ElementsMatch(t, []string{"past-threshold", "stale-decision"}, names)
	assert.Equal(t, 2, countPendingCompositions(logr.Discard(), reader, threshold, metricsTestNow))
}

func TestListPendingCompositionsListError(t *testing.T) {
	reader := interceptor.NewClient(pendingTestReader(t, pendingTestComposition("pending", time.Hour)), interceptor.Funcs{
		List: func(ctx context.Context, cli client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			return errors.New("cache not synced")
		},
	})
	var logged []string
	logger := funcr.New(func(prefix, args string) {
		logged = append(logged, args)
	}, funcr.Options{})
	assert.Nil(t, listPendingCompositions(logger, reader, time.Minute, metricsTestNow))
	assert.Equal(t, 0, countPendingCompositions(logger, reader, time.Minute, metricsTestNow))
	require.Len(t, logged, 2, "each failed list is logged")
	assert.Contains(t, logged[0], "cache not synced")
}

func TestPendingCompositionMetrics(t *testing.T) {
	resetRecoveryMetrics(t)

	// Disabled recovery installs no sources.
	assert.Equal(t, 0.0, prometheustestutil.ToFloat64(pendingCompositions))
	assert.Equal(t, 0, prometheustestutil.CollectAndCount(pendingCompositionCollector{}))

	reader := pendingTestReader(t,
		pendingTestComposition("stuck-a", time.Hour),
		pendingTestComposition("stuck-b", 2*time.Minute),
		pendingTestComposition("recent", time.Second),
	)
	setPendingCompositionsSource(func() int {
		return countPendingCompositions(logr.Discard(), reader, time.Minute, metricsTestNow)
	})
	setPendingCompositionDetailsSource(func() []apiv1.Composition {
		return listPendingCompositions(logr.Discard(), reader, time.Minute, metricsTestNow)
	})

	assert.Equal(t, 2.0, prometheustestutil.ToFloat64(pendingCompositions))
	expected := `
# HELP eno_tombstone_recovery_pending_composition Set to 1 for each Composition whose reconciliation has waited on a tombstone recovery decision longer than the pending threshold
# TYPE eno_tombstone_recovery_pending_composition gauge
eno_tombstone_recovery_pending_composition{composition_name="stuck-a",composition_namespace="control",synthesizer="synthesizer"} 1
eno_tombstone_recovery_pending_composition{composition_name="stuck-b",composition_namespace="control",synthesizer="synthesizer"} 1
`
	require.NoError(t, prometheustestutil.CollectAndCompare(pendingCompositionCollector{}, strings.NewReader(expected)))
}

// tombstoneHistogramText renders the expected histogram after a single observation of value.
func tombstoneHistogramText(value int) string {
	var b strings.Builder
	b.WriteString(`
# HELP eno_tombstone_recovery_tombstones Number of tombstones written by a completed tombstone recovery
# TYPE eno_tombstone_recovery_tombstones histogram
`)
	for _, le := range []int{0, 1, 2, 5, 10, 20, 50} {
		inBucket := 0
		if value <= le {
			inBucket = 1
		}
		fmt.Fprintf(&b, "eno_tombstone_recovery_tombstones_bucket{synthesizer=\"synthesizer\",le=\"%d\"} %d\n", le, inBucket)
	}
	fmt.Fprintf(&b, "eno_tombstone_recovery_tombstones_bucket{synthesizer=\"synthesizer\",le=\"+Inf\"} 1\n")
	fmt.Fprintf(&b, "eno_tombstone_recovery_tombstones_sum{synthesizer=\"synthesizer\"} %d\n", value)
	fmt.Fprintf(&b, "eno_tombstone_recovery_tombstones_count{synthesizer=\"synthesizer\"} 1\n")
	return b.String()
}

func TestRecoveryDecisionMetrics(t *testing.T) {
	tests := []struct {
		name          string
		required      bool
		history       []string
		wantReason    string
		wantHistogram string
	}{
		{
			name: "recovery writes one tombstone", required: true, history: []string{"desired", "removed"},
			wantReason: reasonFinished, wantHistogram: tombstoneHistogramText(1),
		},
		{
			name: "recovery that finds nothing is observed as zero", required: true, history: []string{"desired"},
			wantReason: reasonFinished, wantHistogram: tombstoneHistogramText(0),
		},
		{
			name: "missing inventory records no tombstone observation", required: true,
			wantReason: reasonInventoryNotFound,
		},
		{
			name: "recovery not needed records no tombstone observation", required: false,
			wantReason: reasonNotNeeded,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetRecoveryMetrics(t)
			f := newControllerTestFixture(t, tc.required, "desired")
			if tc.history != nil {
				f.history(tc.history...)
			}
			f.finish(tc.wantReason)

			assert.Equal(t, 1.0, prometheustestutil.ToFloat64(recoveryDecisions.WithLabelValues("synthesizer", tc.wantReason)))
			assert.Equal(t, 1, prometheustestutil.CollectAndCount(recoveryDecisions))
			assert.Equal(t, 1, prometheustestutil.CollectAndCount(recoveryWait))
			if tc.wantHistogram == "" {
				assert.Equal(t, 0, prometheustestutil.CollectAndCount(recoveryTombstones))
				return
			}
			require.NoError(t, prometheustestutil.CollectAndCompare(recoveryTombstones, strings.NewReader(tc.wantHistogram)))
		})
	}
}

func TestRecoveryDecisionMetricsCountRetriedRunOnce(t *testing.T) {
	resetRecoveryMetrics(t)
	f := newControllerTestFixture(t, true, "desired")
	f.history("desired", "removed")

	errPatch := errors.New("status patch failed")
	failed := false
	f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, cli client.Client, name string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if !failed {
				failed = true
				return errPatch
			}
			return cli.SubResource(name).Patch(ctx, obj, patch, opts...)
		},
	})

	f.expectError(errPatch)
	assert.Equal(t, 0, prometheustestutil.CollectAndCount(recoveryDecisions), "an unsaved decision must not be counted")
	assert.Equal(t, 0, prometheustestutil.CollectAndCount(recoveryTombstones), "an unsaved decision must not observe tombstones")

	f.finish(reasonFinished)
	assert.Equal(t, 1.0, prometheustestutil.ToFloat64(recoveryDecisions.WithLabelValues("synthesizer", reasonFinished)))
	// Known limitation: the first attempt already appended the tombstone, so the retry finds nothing missing
	// and observes 0 instead of 1. The run is still counted once.
	require.NoError(t, prometheustestutil.CollectAndCompare(recoveryTombstones, strings.NewReader(tombstoneHistogramText(0))))
}
