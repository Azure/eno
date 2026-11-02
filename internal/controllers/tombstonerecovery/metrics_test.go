package tombstonerecovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	prometheustestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	pending := listPendingCompositions(reader, threshold, metricsTestNow)

	var names []string
	for _, comp := range pending {
		names = append(names, comp.Name)
	}
	assert.ElementsMatch(t, []string{"past-threshold", "stale-decision"}, names)
	assert.Equal(t, 2, countPendingCompositions(reader, threshold, metricsTestNow))
}

func TestListPendingCompositionsListError(t *testing.T) {
	reader := interceptor.NewClient(pendingTestReader(t, pendingTestComposition("pending", time.Hour)), interceptor.Funcs{
		List: func(ctx context.Context, cli client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			return errors.New("cache not synced")
		},
	})
	assert.Nil(t, listPendingCompositions(reader, time.Minute, metricsTestNow))
	assert.Equal(t, 0, countPendingCompositions(reader, time.Minute, metricsTestNow))
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
		return countPendingCompositions(reader, time.Minute, metricsTestNow)
	})
	setPendingCompositionDetailsSource(func() []apiv1.Composition {
		return listPendingCompositions(reader, time.Minute, metricsTestNow)
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

// tombstoneHistogramText renders the expected histogram for observations that are all 0 or 1.
func tombstoneHistogramText(zeros, ones int) string {
	count := zeros + ones
	var b strings.Builder
	b.WriteString(`
# HELP eno_tombstone_recovery_tombstones Number of tombstones written by a completed tombstone recovery
# TYPE eno_tombstone_recovery_tombstones histogram
`)
	fmt.Fprintf(&b, "eno_tombstone_recovery_tombstones_bucket{synthesizer=\"synthesizer\",le=\"0\"} %d\n", zeros)
	for _, le := range []string{"1", "2", "5", "10", "20", "50", "+Inf"} {
		fmt.Fprintf(&b, "eno_tombstone_recovery_tombstones_bucket{synthesizer=\"synthesizer\",le=\"%s\"} %d\n", le, count)
	}
	fmt.Fprintf(&b, "eno_tombstone_recovery_tombstones_sum{synthesizer=\"synthesizer\"} %d\n", ones)
	fmt.Fprintf(&b, "eno_tombstone_recovery_tombstones_count{synthesizer=\"synthesizer\"} %d\n", count)
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
			wantReason: reasonFinished, wantHistogram: tombstoneHistogramText(0, 1),
		},
		{
			name: "recovery that finds nothing is observed as zero", required: true, history: []string{"desired"},
			wantReason: reasonFinished, wantHistogram: tombstoneHistogramText(1, 0),
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
	// The first attempt appended the tombstone; the retry writes nothing new but still reports the full total.
	require.NoError(t, prometheustestutil.CollectAndCompare(recoveryTombstones, strings.NewReader(tombstoneHistogramText(0, 1))))
	assert.Empty(t, f.controller.progress, "progress is cleared once the decision is saved")
}

func TestWrittenTombstonesAccumulateAcrossPartialWrites(t *testing.T) {
	f := newControllerTestFixture(t, true)
	history := []inventoryResource{controllerTestResource("removed-a"), controllerTestResource("removed-b")}
	tombstones, err := missingTombstones(nil, history)
	require.NoError(t, err)
	// Each existing slice has room for exactly one tombstone, so the batch needs two patches.
	room := resource.MaxSliceJSONBytes - len(tombstones[0].Manifest)
	slices := sliceTestExisting(f, [][]apiv1.Manifest{
		{sliceTestManifest(t, "existing-0", room, false)},
		{sliceTestManifest(t, "existing-1", room, false)},
	})
	comp := f.composition()

	rejection := apierrors.NewServiceUnavailable("injected slice write failure")
	patches := 0
	f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
		Patch: func(ctx context.Context, cli client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			patches++
			if patches == 2 {
				return rejection
			}
			return cli.Patch(ctx, obj, patch, opts...)
		},
	})
	_, err = f.controller.writeTombstones(t.Context(), comp, slices, tombstones)
	require.ErrorIs(t, err, rejection)

	reloaded, err := f.controller.loadCurrentSynthesisResourceSlices(t.Context(), f.composition())
	require.NoError(t, err)
	missing, err := missingTombstones(reloaded, history)
	require.NoError(t, err)
	require.Len(t, missing, 1, "only the unwritten tombstone remains")
	_, err = f.controller.writeTombstones(t.Context(), f.composition(), reloaded, missing)
	require.NoError(t, err)

	assert.Equal(t, 2, f.controller.takeWrittenTombstones(f.composition()), "both attempts' writes are counted")
	assert.Equal(t, 0, f.controller.takeWrittenTombstones(f.composition()), "taking clears the progress")
}

func TestWrittenTombstonesIgnoreRewrittenOverflow(t *testing.T) {
	f := newControllerTestFixture(t, true)
	slices := sliceTestExisting(f, nil)
	comp := f.composition()
	history := []inventoryResource{controllerTestResource("removed")}
	tombstones, err := missingTombstones(slices, history)
	require.NoError(t, err)

	_, err = f.controller.writeTombstones(t.Context(), comp, slices, tombstones)
	require.NoError(t, err)
	// The overflow reference was never published, so the retry rewrites the same slice and hits AlreadyExists.
	reloaded, err := f.controller.loadCurrentSynthesisResourceSlices(t.Context(), f.composition())
	require.NoError(t, err)
	missing, err := missingTombstones(reloaded, history)
	require.NoError(t, err)
	_, err = f.controller.writeTombstones(t.Context(), f.composition(), reloaded, missing)
	require.NoError(t, err)

	assert.Equal(t, 1, f.controller.takeWrittenTombstones(f.composition()), "a matching existing overflow slice is not counted again")
}

func TestWrittenTombstonesResetForNewSynthesis(t *testing.T) {
	c := &tombstoneRecoveryController{}
	comp := pendingTestComposition("composition", 0)
	comp.UID = "uid"
	c.addWrittenTombstones(comp, 3)

	next := comp.DeepCopy()
	next.Status.CurrentSynthesis.UUID = "next"
	assert.Equal(t, 0, c.takeWrittenTombstones(next), "progress from an older synthesis is not reported")

	c.addWrittenTombstones(comp, 3)
	recreated := comp.DeepCopy()
	recreated.UID = "recreated-uid"
	c.addWrittenTombstones(recreated, 1)
	assert.Equal(t, 1, c.takeWrittenTombstones(recreated), "a recreated Composition starts from zero")
}
