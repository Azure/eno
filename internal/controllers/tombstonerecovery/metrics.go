package tombstonerecovery

import (
	"context"
	"sync/atomic"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const pendingCompositionsListTimeout = 5 * time.Second

var (
	// Persisted recovery decisions, partitioned by synthesizer and reason.
	// Counted only after the decision is written, so retries are not double-counted.
	// Anomaly example: a rising rate of reason="InventoryInvalid" points at corrupted inventory Secrets.
	recoveryDecisions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "eno_tombstone_recovery_decisions_total",
			Help: "Persisted tombstone recovery decisions partitioned by synthesizer and reason",
		},
		[]string{"synthesizer", "reason"},
	)

	// Tombstones written by one completed recovery, including runs that found nothing.
	// Anomaly example: observations in the 100+ buckets suggest a bad synthesis or the wrong inventory lineage.
	recoveryTombstones = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "eno_tombstone_recovery_tombstones",
			Help:    "Number of tombstones written by a completed tombstone recovery",
			Buckets: []float64{0, 1, 5, 10, 50, 100, 500},
		},
		[]string{"synthesizer"},
	)

	// Time from synthesis completion until the recovery decision is persisted.
	// Reconciliation of the synthesis is blocked for this whole interval.
	recoveryWait = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "eno_tombstone_recovery_wait_seconds",
			Help:    "Time from synthesis completion until the tombstone recovery decision is persisted",
			Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600},
		},
		[]string{"synthesizer"},
	)

	// Compositions whose reconciliation is currently blocked waiting on tombstone recovery.
	// Alert when this stays above zero; the threshold belongs in the alert rule.
	pendingCompositions = prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "eno_tombstone_recovery_pending_compositions",
			Help: "Compositions whose reconciliation is waiting on a tombstone recovery decision",
		},
		func() float64 {
			fn := pendingCompositionsFn.Load()
			if fn == nil {
				return 0
			}
			return float64((*fn)())
		},
	)
	// Installed by NewController; nil when tombstone recovery is disabled.
	// atomic.Pointer makes the swap safe against the scrape goroutine.
	pendingCompositionsFn atomic.Pointer[func() int]
)

func init() {
	metrics.Registry.MustRegister(recoveryDecisions, recoveryTombstones, recoveryWait, pendingCompositions)
}

func setPendingCompositionsSource(reader client.Reader) {
	fn := func() int {
		ctx, cancel := context.WithTimeout(context.Background(), pendingCompositionsListTimeout)
		defer cancel()
		list := &apiv1.CompositionList{}
		if err := reader.List(ctx, list); err != nil {
			return 0
		}
		count := 0
		for i := range list.Items {
			if isWaitingForRecovery(&list.Items[i]) {
				count++
			}
		}
		return count
	}
	pendingCompositionsFn.Store(&fn)
}

// isWaitingForRecovery mirrors the reconstitution gate that blocks reconciliation until recovery finishes.
func isWaitingForRecovery(comp *apiv1.Composition) bool {
	syn := comp.Status.CurrentSynthesis
	return comp.RecoveryEnabled() && comp.DeletionTimestamp == nil &&
		syn != nil && syn.Synthesized != nil && !syn.IsTombstoneRecoveryFinished()
}

func observeRecoveryDecision(comp *apiv1.Composition, reason string) {
	synthesizer := comp.Spec.Synthesizer.Name
	recoveryDecisions.WithLabelValues(synthesizer, reason).Inc()
	if syn := comp.Status.CurrentSynthesis; syn != nil && syn.Synthesized != nil {
		recoveryWait.WithLabelValues(synthesizer).Observe(time.Since(syn.Synthesized.Time).Seconds())
	}
}
