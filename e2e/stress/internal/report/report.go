// Machine-readable and human-readable stress latency reports.
package report

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	stressstate "github.com/Azure/eno/e2e/stress/internal/state"
)

type Report struct {
	SchemaVersion   string                 `json:"schemaVersion"`
	RunID           string                 `json:"runID"`
	PlanName        string                 `json:"planName"`
	PlanDigest      string                 `json:"planDigest"`
	CompletionStage string                 `json:"completionStage"`
	StartedAt       *time.Time             `json:"startedAt,omitempty"`
	CompletedAt     *time.Time             `json:"completedAt,omitempty"`
	Compositions    map[string]Composition `json:"compositions"`
	Aggregate       map[string]Statistics  `json:"aggregate"`
	Failures        []string               `json:"failures,omitempty"`
}

type Composition struct {
	Namespace       string             `json:"namespace"`
	CompositionName string             `json:"compositionName"`
	Variation       string             `json:"variation"`
	SynthesisUUID   string             `json:"synthesisUUID,omitempty"`
	LastStatus      string             `json:"lastStatus,omitempty"`
	OutputValid     bool               `json:"outputValid"`
	Failure         string             `json:"failure,omitempty"`
	LatencyMS       map[string]float64 `json:"latencyMs,omitempty"`
}

type Statistics struct {
	Count int     `json:"count"`
	P50MS float64 `json:"p50Ms"`
	P95MS float64 `json:"p95Ms"`
	P99MS float64 `json:"p99Ms"`
	MaxMS float64 `json:"maxMs"`
}

func Build(data *stressstate.State) *Report {
	result := &Report{
		SchemaVersion:   "v1alpha2",
		RunID:           data.RunID,
		PlanName:        data.PlanName,
		PlanDigest:      data.PlanDigest,
		CompletionStage: data.CompletionStage,
		StartedAt:       data.WorkloadStart,
		CompletedAt:     data.CompletedAt,
		Compositions:    map[string]Composition{},
		Aggregate:       map[string]Statistics{},
	}
	values := map[string][]float64{}
	for key, state := range data.CompositionState {
		entry := Composition{LatencyMS: map[string]float64{}}
		if state == nil {
			entry.Failure = "no composition result"
			result.Failures = append(result.Failures, key+": "+entry.Failure)
			result.Compositions[key] = entry
			continue
		}
		entry.Namespace = state.Namespace
		entry.CompositionName = state.CompositionName
		entry.Variation = state.Variation
		entry.SynthesisUUID = state.SynthesisUUID
		entry.LastStatus = state.LastStatus
		entry.OutputValid = state.OutputValid
		entry.Failure = state.Failure
		inputStart, inputAck := inputBounds(data.NamespaceInputs[state.Namespace])
		addLatency(entry.LatencyMS, values, "inputCreateToAPIAck", inputStart, inputAck)
		addLatency(entry.LatencyMS, values, "apiAckToInputRevision", inputAck, state.InputRevisionObservedAt)
		addLatency(entry.LatencyMS, values, "apiAckToExitMissingInputs", inputAck, state.ExitedMissingInputsAt)
		if data.CompletionStage == "PostSynthesis" || data.CompletionStage == "Reconcile" {
			addLatency(entry.LatencyMS, values, "exitMissingInputsToSynthesisStart", state.ExitedMissingInputsAt, state.SynthesisInitializedObservedAt)
			addLatency(entry.LatencyMS, values, "synthesisStartToSynthesisComplete", state.SynthesisInitializedObservedAt, state.SynthesisCompletedObservedAt)
			addLatency(entry.LatencyMS, values, "inputCreateToSynthesisComplete", inputStart, state.SynthesisCompletedObservedAt)
		}
		if data.CompletionStage == "Reconcile" {
			addLatency(entry.LatencyMS, values, "synthesisCompleteToReconciled", state.SynthesisCompletedObservedAt, state.ReconciledObservedAt)
			addLatency(entry.LatencyMS, values, "inputCreateToReconciled", inputStart, state.ReconciledObservedAt)
		}
		if entry.Failure != "" {
			result.Failures = append(result.Failures, key+": "+entry.Failure)
		}
		result.Compositions[key] = entry
	}
	for metric, samples := range values {
		result.Aggregate[metric] = statistics(samples)
	}
	return result
}

func Write(path string, result *Report) error {
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func Print(result *Report, output func(string, ...any)) {
	output("run %s: stage=%s, %d compositions, %d failures\n", result.RunID, result.CompletionStage, len(result.Compositions), len(result.Failures))
	metrics := make([]string, 0, len(result.Aggregate))
	for metric := range result.Aggregate {
		metrics = append(metrics, metric)
	}
	sort.Strings(metrics)
	output("%-38s %8s %8s %8s %8s\n", "latency", "p50", "p95", "p99", "max")
	for _, metric := range metrics {
		stats := result.Aggregate[metric]
		output("%-38s %7.1fms %7.1fms %7.1fms %7.1fms\n", metric, stats.P50MS, stats.P95MS, stats.P99MS, stats.MaxMS)
	}
	for _, failure := range result.Failures {
		output("failure: %s\n", failure)
	}
}

func inputBounds(inputs map[string]*stressstate.InputTiming) (*time.Time, *time.Time) {
	var earliest, latest time.Time
	for _, input := range inputs {
		if earliest.IsZero() || input.RequestStartedAt.Before(earliest) {
			earliest = input.RequestStartedAt
		}
		if latest.IsZero() || input.APIAcknowledgedAt.After(latest) {
			latest = input.APIAcknowledgedAt
		}
	}
	if earliest.IsZero() || latest.IsZero() {
		return nil, nil
	}
	return &earliest, &latest
}

func addLatency(target map[string]float64, aggregate map[string][]float64, name string, start, end *time.Time) {
	if start == nil || end == nil {
		return
	}
	value := float64(end.Sub(*start).Microseconds()) / 1000
	target[name] = value
	aggregate[name] = append(aggregate[name], value)
}

func statistics(values []float64) Statistics {
	sort.Float64s(values)
	return Statistics{
		Count: len(values),
		P50MS: percentile(values, 0.50),
		P95MS: percentile(values, 0.95),
		P99MS: percentile(values, 0.99),
		MaxMS: values[len(values)-1],
	}
}

func percentile(values []float64, quantile float64) float64 {
	index := int(math.Ceil(quantile*float64(len(values)))) - 1
	if index < 0 {
		return 0
	}
	return values[index]
}

func ResolvePath(baseDir, configured, runID string) string {
	if configured == "" {
		configured = filepath.Join("results", runID+".json")
	}
	configured = filepath.Clean(configured)
	configured = stringReplace(configured, "${runID}", runID)
	if !filepath.IsAbs(configured) {
		configured = filepath.Join(baseDir, configured)
	}
	return configured
}

func stringReplace(value, old, replacement string) string {
	for {
		index := -1
		for position := 0; position+len(old) <= len(value); position++ {
			if value[position:position+len(old)] == old {
				index = position
				break
			}
		}
		if index < 0 {
			return value
		}
		value = fmt.Sprintf("%s%s%s", value[:index], replacement, value[index+len(old):])
	}
}
