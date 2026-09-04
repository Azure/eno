// Workload-only execution for prepared stress runs.
package runner

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Azure/eno/e2e/stress/internal/render"
	"github.com/Azure/eno/e2e/stress/internal/report"
	stressstate "github.com/Azure/eno/e2e/stress/internal/state"
)

func Run(ctx context.Context, options Options) error {
	r, err := loadRunRuntime(options)
	if err != nil {
		return err
	}
	timeout, _ := r.plan.Timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	r.output("verifying prepared Compositions\n")
	compositionRecords, expectedInputs, err := r.verifyPrepared(ctx)
	if err != nil {
		return err
	}
	r.output("verified %d prepared Compositions; establishing watches\n", len(compositionRecords))
	monitor, err := newRunMonitor(r, gvrFor(compositionRecords[0]), expectedInputs)
	if err != nil {
		return err
	}
	defer monitor.Stop()
	if err := monitor.Start(ctx); err != nil {
		return fmt.Errorf("establishing workload watches: %w", err)
	}
	start := time.Now()
	if err := r.store.Update(func(data *stressstate.State) error {
		data.Phase = "running"
		data.WorkloadStart = &start
		return nil
	}); err != nil {
		return err
	}
	monitor.Arm()
	r.output("workload started at %s; watches established\n", start.Format(time.RFC3339Nano))
	if err := r.executePhases(ctx, monitor); err != nil {
		monitor.markIncomplete(err.Error())
		return err
	}
	if err := monitor.Wait(ctx); err != nil {
		return fmt.Errorf("waiting for %s completion: %w", r.plan.Spec.Run.CompletionStage, err)
	}
	completed := time.Now()
	if err := r.store.Update(func(data *stressstate.State) error {
		data.Phase = "completed"
		data.CompletedAt = &completed
		return nil
	}); err != nil {
		return err
	}
	return r.writeReport()
}

func (r *runtime) verifyPrepared(ctx context.Context) ([]stressstate.Resource, []string, error) {
	var records []stressstate.Resource
	var phase, runID string
	r.store.Read(func(data *stressstate.State) error {
		phase = data.Phase
		runID = data.RunID
		for _, resource := range data.Resources {
			if resource.Phase == "setup" && resource.Kind == "Composition" {
				records = append(records, resource)
			}
		}
		return nil
	})
	if phase != "prepared" && phase != "running" {
		return nil, nil, fmt.Errorf("state phase must be prepared or running, got %q", phase)
	}
	if len(records) == 0 {
		return nil, nil, fmt.Errorf("no prepared Compositions found in state")
	}
	expectedCount := expandedResourceCount(r.plan, records[0].LogicalName)
	if len(records) != expectedCount {
		return nil, nil, fmt.Errorf("expected %d prepared Compositions, found %d", expectedCount, len(records))
	}
	gvr := gvrFor(records[0])
	recordsByKey := make(map[string]stressstate.Resource, len(records))
	for _, record := range records {
		if gvrFor(record) != gvr {
			return nil, nil, fmt.Errorf("prepared Compositions span multiple resource types")
		}
		recordsByKey[compositionKey(record.Namespace, record.Name)] = record
	}

	results := map[string]*stressstate.CompositionResult{}
	selector := render.RunIDLabel + "=" + runID + "," + render.ResourceIDLabel + "=composition"
	resource := r.client.Dynamic.Resource(gvr).Namespace(metav1.NamespaceAll)
	continueToken := ""
	for {
		list, err := resource.List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 500, Continue: continueToken})
		if err != nil {
			return nil, nil, fmt.Errorf("listing prepared Compositions: %w", err)
		}
		for index := range list.Items {
			object := &list.Items[index]
			key := compositionKey(object.GetNamespace(), object.GetName())
			record, found := recordsByKey[key]
			if !found {
				return nil, nil, fmt.Errorf("found unrecorded Composition %s during preflight", key)
			}
			if string(object.GetUID()) != record.UID {
				return nil, nil, fmt.Errorf("Composition %s UID changed after prepare", key)
			}
			if simplifiedStatus(object) != "MissingInputs" || synthesisExists(object) {
				return nil, nil, fmt.Errorf("Composition %s is no longer pristine MissingInputs (status=%s)", key, simplifiedStatus(object))
			}
			results[key] = &stressstate.CompositionResult{
				Namespace:       object.GetNamespace(),
				CompositionName: object.GetName(),
				Variation:       object.GetLabels()[render.VariationLabel],
			}
		}
		continueToken = list.GetContinue()
		if continueToken == "" {
			break
		}
	}
	if len(results) != len(records) {
		return nil, nil, fmt.Errorf("expected %d live Compositions, found %d", len(records), len(results))
	}
	expected := []string{}
	for _, readiness := range r.plan.Spec.Setup.Readiness {
		if readiness.Condition.Status == "MissingInputs" {
			expected = append(expected, readiness.Condition.ExpectedMissingInputs...)
			break
		}
	}
	if len(expected) == 0 {
		for _, resource := range r.plan.Spec.Test.Phases[0].Resources {
			expected = append(expected, resource.Name)
		}
	}
	if err := r.store.Update(func(data *stressstate.State) error {
		for key, result := range results {
			if existing := data.CompositionState[key]; existing != nil {
				result.InputRevisionObservedAt = existing.InputRevisionObservedAt
				result.ExitedMissingInputsAt = existing.ExitedMissingInputsAt
				result.SynthesisInitializedAt = existing.SynthesisInitializedAt
				result.SynthesisInitializedObservedAt = existing.SynthesisInitializedObservedAt
				result.SynthesisCompletedAt = existing.SynthesisCompletedAt
				result.SynthesisCompletedObservedAt = existing.SynthesisCompletedObservedAt
				result.ReconciledAt = existing.ReconciledAt
				result.ReconciledObservedAt = existing.ReconciledObservedAt
				result.ReadyAt = existing.ReadyAt
				result.ReadyObservedAt = existing.ReadyObservedAt
				result.OutputObservedAt = existing.OutputObservedAt
				result.OutputValid = existing.OutputValid
				result.SynthesisUUID = existing.SynthesisUUID
				result.LastStatus = existing.LastStatus
				result.Failure = existing.Failure
			}
			data.CompositionState[key] = result
			if data.NamespaceInputs[result.Namespace] == nil {
				data.NamespaceInputs[result.Namespace] = map[string]*stressstate.InputTiming{}
			}
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	return records, expected, nil
}

func (r *runtime) executePhases(ctx context.Context, monitor *runMonitor) error {
	var namespaces []string
	var runID string
	r.store.Read(func(data *stressstate.State) error {
		namespaces = append(namespaces, data.Namespaces...)
		runID = data.RunID
		return nil
	})
	for _, phase := range r.plan.Spec.Test.Phases {
		for iteration := 1; iteration <= phase.Repetitions; iteration++ {
			for start := 0; start < len(namespaces); start += phase.BatchSize {
				end := min(start+phase.BatchSize, len(namespaces))
				batch := namespaces[start:end]
				taskCount := len(batch) * len(phase.Resources)
				r.output("phase %s iteration %d: namespaces %d-%d (%d objects)\n", phase.Name, iteration, start+1, end, taskCount)
				if err := parallel(ctx, phase.Concurrency, taskCount, func(ctx context.Context, task int) error {
					namespaceOffset := task / len(phase.Resources)
					resourceOffset := task % len(phase.Resources)
					namespace := batch[namespaceOffset]
					namespaceIndex := start + namespaceOffset + 1
					resourceSpec := phase.Resources[resourceOffset]
					context := render.Context{
						RunID:          runID,
						PlanName:       r.plan.Metadata.Name,
						Namespace:      namespace,
						NamespaceIndex: namespaceIndex,
						Phase:          phase.Name,
						Iteration:      iteration,
						Variables:      r.plan.Spec.Run.Variables,
						Labels:         r.plan.Spec.Run.Labels,
					}
					object, err := render.Resource(r.baseDir, resourceSpec, context)
					if err != nil {
						return err
					}
					startedAt := time.Now()
					if resourceSpec.Operation == "create" {
						created, resource, err := r.client.CreateOwned(ctx, object, runID, false)
						acknowledgedAt := time.Now()
						if err != nil {
							return err
						}
						if err := recordResourceMemory(r.store, resourceSpec.Name, phase.Name, created, resource, startedAt, acknowledgedAt); err != nil {
							return err
						}
						return monitor.InputAcknowledged(namespace, resourceSpec.Name, created, startedAt, acknowledgedAt, iteration > 1)
					}
					created, resource, err := r.client.ApplyOwned(ctx, object, runID)
					acknowledgedAt := time.Now()
					if err != nil {
						return err
					}
					if err := recordResourceMemory(r.store, resourceSpec.Name, phase.Name, created, resource, startedAt, acknowledgedAt); err != nil {
						return err
					}
					return monitor.InputAcknowledged(namespace, resourceSpec.Name, created, startedAt, acknowledgedAt, true)
				}); err != nil {
					return fmt.Errorf("phase %s iteration %d: %w", phase.Name, iteration, err)
				}
				if err := r.store.Save(); err != nil {
					return fmt.Errorf("persisting phase %s iteration %d: %w", phase.Name, iteration, err)
				}
				if end < len(namespaces) {
					delay := phase.Delay
					if delay == "" {
						delay = r.plan.Spec.Run.BatchDelay
					}
					if delay != "" {
						duration, _ := time.ParseDuration(delay)
						timer := time.NewTimer(duration)
						select {
						case <-ctx.Done():
							timer.Stop()
							return ctx.Err()
						case <-timer.C:
						}
					}
				}
			}
		}
	}
	return nil
}

func (r *runtime) writeReport() error {
	var result *report.Report
	var runID string
	r.store.Read(func(data *stressstate.State) error {
		runID = data.RunID
		result = report.Build(data)
		return nil
	})
	path := report.ResolvePath(r.baseDir, r.plan.Spec.Metrics.ReportFile, runID)
	if err := report.Write(path, result); err != nil {
		return err
	}
	report.Print(result, r.output)
	r.output("JSON report: %s\n", filepath.Clean(path))
	if len(result.Failures) > 0 {
		return fmt.Errorf("run completed with %d failures", len(result.Failures))
	}
	return nil
}
