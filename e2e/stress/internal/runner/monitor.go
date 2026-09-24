// Composition and output event correlation for workload runs.
package runner

import (
	"context"
	"fmt"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Azure/eno/e2e/stress/internal/plan"
	"github.com/Azure/eno/e2e/stress/internal/render"
	stressstate "github.com/Azure/eno/e2e/stress/internal/state"
)

type runMonitor struct {
	runtime        *runtime
	runID          string
	stage          plan.CompletionStage
	compositionGVR schema.GroupVersionResource
	expectedInputs []string

	mutex       sync.Mutex
	latest      map[string]*unstructured.Unstructured
	completed   map[string]bool
	total       int
	armed       bool
	done        chan struct{}
	errors      chan error
	complete    sync.Once
	watchCancel context.CancelFunc
}

func newRunMonitor(r *runtime, compositionGVR schema.GroupVersionResource, expectedInputs []string) (*runMonitor, error) {
	monitor := &runMonitor{
		runtime:        r,
		stage:          r.plan.Spec.Run.CompletionStage,
		compositionGVR: compositionGVR,
		expectedInputs: expectedInputs,
		latest:         map[string]*unstructured.Unstructured{},
		completed:      map[string]bool{},
		done:           make(chan struct{}),
		errors:         make(chan error, 1),
	}
	r.store.Read(func(data *stressstate.State) error {
		monitor.runID = data.RunID
		monitor.total = len(data.CompositionState)
		for key, result := range data.CompositionState {
			if monitor.resultComplete(result) {
				monitor.completed[key] = true
			}
		}
		return nil
	})
	return monitor, nil
}

func (m *runMonitor) Start(ctx context.Context) error {
	watchContext, cancel := context.WithCancel(ctx)
	m.watchCancel = cancel
	compositionReady := make(chan struct{})
	selector := render.RunIDLabel + "=" + m.runID
	go func() {
		err := m.runtime.client.Watch(watchContext, m.compositionGVR, metav1.NamespaceAll, selector, compositionReady, m.handleComposition)
		if watchContext.Err() == nil {
			m.errors <- err
		}
	}()
	go m.checkpoint(watchContext)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-m.errors:
		return err
	case <-compositionReady:
	}
	return nil
}

func (m *runMonitor) checkpoint(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.runtime.store.Save(); err != nil {
				select {
				case <-ctx.Done():
					return
				case m.errors <- fmt.Errorf("checkpointing run state: %w", err):
					return
				}
			}
		}
	}
}

func (m *runMonitor) Stop() {
	if m.watchCancel != nil {
		m.watchCancel()
	}
}

func (m *runMonitor) Arm() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.armed = true
	if len(m.completed) == m.total {
		m.complete.Do(func() { close(m.done) })
	}
}

func (m *runMonitor) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		m.markIncomplete(ctx.Err().Error())
		return ctx.Err()
	case err := <-m.errors:
		m.markIncomplete(err.Error())
		return err
	case <-m.done:
		return nil
	}
}

func (m *runMonitor) InputAcknowledged(namespace, logicalName string, object *unstructured.Unstructured, startedAt, acknowledgedAt time.Time, overwrite bool) error {
	if err := m.runtime.store.Mutate(func(data *stressstate.State) error {
		inputs := data.NamespaceInputs[namespace]
		if inputs == nil {
			return fmt.Errorf("no input state for namespace %s", namespace)
		}
		if _, found := inputs[logicalName]; found && !overwrite {
			return nil
		}
		inputs[logicalName] = &stressstate.InputTiming{
			Name:              object.GetName(),
			Kind:              object.GetKind(),
			RequestStartedAt:  startedAt,
			APIAcknowledgedAt: acknowledgedAt,
			ResourceVersion:   object.GetResourceVersion(),
			UID:               string(object.GetUID()),
		}
		return nil
	}); err != nil {
		return err
	}
	m.mutex.Lock()
	var latest []*unstructured.Unstructured
	for _, object := range m.latest {
		if object.GetNamespace() == namespace {
			latest = append(latest, object.DeepCopy())
		}
	}
	m.mutex.Unlock()
	for _, object := range latest {
		m.handleComposition(object, time.Now())
	}
	return nil
}

func (m *runMonitor) handleComposition(object *unstructured.Unstructured, observedAt time.Time) {
	namespace := object.GetNamespace()
	key := compositionKey(namespace, object.GetName())
	m.mutex.Lock()
	m.latest[key] = object.DeepCopy()
	m.mutex.Unlock()
	nowComplete := false
	_ = m.runtime.store.Mutate(func(data *stressstate.State) error {
		result := data.CompositionState[key]
		if result == nil {
			return nil
		}
		inputs := data.NamespaceInputs[namespace]
		result.LastStatus = simplifiedStatus(object)
		revisions := inputRevisionKeys(object)
		allRevisions := len(m.expectedInputs) > 0
		for _, key := range m.expectedInputs {
			input := inputs[key]
			if input == nil || revisions[key] != input.ResourceVersion {
				allRevisions = false
				break
			}
		}
		if allRevisions && result.InputRevisionObservedAt == nil {
			result.InputRevisionObservedAt = timePointer(observedAt)
		}
		if result.InputRevisionObservedAt != nil && result.ExitedMissingInputsAt == nil && result.LastStatus != "MissingInputs" {
			result.ExitedMissingInputsAt = timePointer(observedAt)
		}
		if result.InputRevisionObservedAt != nil {
			if synthesis, found := synthesisMap(object); found {
				if uuid, _, _ := unstructured.NestedString(synthesis, "uuid"); uuid != "" {
					result.SynthesisUUID = uuid
				}
				if initialized, _, _ := unstructured.NestedString(synthesis, "initialized"); initialized != "" && result.SynthesisInitializedObservedAt == nil {
					result.SynthesisInitializedAt = parseTime(initialized)
					result.SynthesisInitializedObservedAt = timePointer(observedAt)
				}
				if synthesized, _, _ := unstructured.NestedString(synthesis, "synthesized"); synthesized != "" && result.SynthesisCompletedObservedAt == nil {
					result.SynthesisCompletedAt = parseTime(synthesized)
					result.SynthesisCompletedObservedAt = timePointer(observedAt)
				}
			}
		}
		current, found, _ := unstructured.NestedMap(object.Object, "status", "currentSynthesis")
		if found {
			if synthesized, _, _ := unstructured.NestedString(current, "synthesized"); synthesized != "" && result.SynthesisCompletedObservedAt == nil {
				result.SynthesisCompletedAt = parseTime(synthesized)
				result.SynthesisCompletedObservedAt = timePointer(observedAt)
			}
			if reconciled, _, _ := unstructured.NestedString(current, "reconciled"); reconciled != "" && result.ReconciledObservedAt == nil {
				result.ReconciledAt = parseTime(reconciled)
				result.ReconciledObservedAt = timePointer(observedAt)
			}
			if ready, _, _ := unstructured.NestedString(current, "ready"); ready != "" && result.ReadyObservedAt == nil {
				result.ReadyAt = parseTime(ready)
				result.ReadyObservedAt = timePointer(observedAt)
			}
		}
		nowComplete = m.resultComplete(result)
		return nil
	})
	if nowComplete {
		m.recordCompletion(key)
	}
}

func (m *runMonitor) recordCompletion(key string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.completed[key] = true
	if m.armed && len(m.completed) == m.total {
		m.complete.Do(func() { close(m.done) })
	}
}

func (m *runMonitor) resultComplete(result *stressstate.CompositionResult) bool {
	if result == nil || result.InputRevisionObservedAt == nil || result.ExitedMissingInputsAt == nil {
		return false
	}
	switch m.stage {
	case plan.CompletionStagePreSynthesis:
		return true
	case plan.CompletionStagePostSynthesis:
		return result.SynthesisCompletedObservedAt != nil
	case plan.CompletionStageReconcile:
		return result.ReconciledObservedAt != nil
	default:
		return false
	}
}

func (m *runMonitor) markIncomplete(reason string) {
	_ = m.runtime.store.Mutate(func(data *stressstate.State) error {
		for _, result := range data.CompositionState {
			if result == nil {
				continue
			}
			if m.resultComplete(result) {
				continue
			}
			if result.Failure == "" {
				result.Failure = "incomplete: " + reason
			}
		}
		return nil
	})
	_ = m.runtime.store.Save()
}

func synthesisMap(object *unstructured.Unstructured) (map[string]any, bool) {
	if inFlight, found, _ := unstructured.NestedMap(object.Object, "status", "inFlightSynthesis"); found && len(inFlight) > 0 {
		return inFlight, true
	}
	current, found, _ := unstructured.NestedMap(object.Object, "status", "currentSynthesis")
	return current, found && len(current) > 0
}

func parseTime(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return &parsed
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}
