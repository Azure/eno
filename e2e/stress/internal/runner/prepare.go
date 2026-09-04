// Setup-resource creation and MissingInputs gating.
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

func Prepare(ctx context.Context, options Options) error {
	r, err := loadRuntime(options, true)
	if err != nil {
		return err
	}
	timeout, _ := r.plan.Timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var runID string
	r.store.Read(func(data *stressstate.State) error {
		runID = data.RunID
		return nil
	})
	namespaces := namespaceNames(r.plan, runID)
	if err := r.store.Update(func(data *stressstate.State) error {
		data.Phase = "preparing"
		data.Namespaces = namespaces
		return nil
	}); err != nil {
		return err
	}
	r.output("run ID: %s\n", runID)
	r.output("creating %d namespaces\n", len(namespaces))
	if err := parallel(ctx, r.plan.Spec.Run.Concurrency, len(namespaces), func(ctx context.Context, index int) error {
		object := namespaceObject(namespaces[index], runID, r.plan.Metadata.Name, r.plan.Spec.Run.Labels)
		startedAt := time.Now()
		created, resource, err := r.client.CreateOwned(ctx, object, runID, false)
		acknowledgedAt := time.Now()
		if err != nil {
			return fmt.Errorf("creating namespace %s: %w", namespaces[index], err)
		}
		return recordResource(r.store, "namespace", "setup", created, resource, startedAt, acknowledgedAt)
	}); err != nil {
		return err
	}

	ordered, err := orderedSetup(r.plan.Spec.Setup.Resources)
	if err != nil {
		return err
	}
	for _, resourceSpec := range ordered {
		if resourceSpec.Operation == "observe" {
			r.output("observing generated setup resource %s\n", resourceSpec.Name)
			if err := r.observeSetupResource(ctx, resourceSpec, namespaces, runID); err != nil {
				return err
			}
			continue
		}
		contexts := renderContexts(r.plan, runID, namespaces, "setup", 1, resourceSpec)
		r.output("creating setup resource %s (%d objects)\n", resourceSpec.Name, len(contexts))
		if err := parallel(ctx, r.plan.Spec.Run.Concurrency, len(contexts), func(ctx context.Context, index int) error {
			object, err := render.Resource(r.baseDir, resourceSpec, contexts[index])
			if err != nil {
				return err
			}
			startedAt := time.Now()
			created, resource, err := r.client.CreateSetup(ctx, object, runID, false, resourceSpec.Reuse)
			acknowledgedAt := time.Now()
			if err != nil {
				return fmt.Errorf("creating %s %s/%s: %w", object.GetKind(), object.GetNamespace(), object.GetName(), err)
			}
			return recordResource(r.store, resourceSpec.Name, "setup", created, resource, startedAt, acknowledgedAt)
		}); err != nil {
			return err
		}
	}

	for _, readiness := range r.plan.Spec.Setup.Readiness {
		r.output("waiting for %s resources to enter %s\n", readiness.Resource, readiness.Condition.Status)
		if err := r.waitForReadiness(ctx, readiness.Resource, readiness.Condition.Status, readiness.Condition.ExpectedMissingInputs); err != nil {
			return err
		}
	}
	now := time.Now()
	if err := r.store.Update(func(data *stressstate.State) error {
		data.Phase = "prepared"
		data.PreparedAt = &now
		return nil
	}); err != nil {
		return err
	}
	r.output("prepared %d compositions in MissingInputs; state: %s\n", expandedResourceCount(r.plan, "composition"), options.StatePath)
	return nil
}

func (r *runtime) observeSetupResource(ctx context.Context, resourceSpec plan.ResourceSpec, namespaces []string, runID string) error {
	probe := &unstructured.Unstructured{}
	probe.SetAPIVersion(resourceSpec.APIVersion)
	probe.SetKind(resourceSpec.Kind)
	if resourceSpec.Scope == "namespace" && len(namespaces) > 0 {
		probe.SetNamespace(namespaces[0])
	}
	resource, err := r.client.ResourceFor(probe)
	if err != nil {
		return err
	}

	expectedNamespaces := map[string]bool{}
	expectedPerNamespace := resourceSpec.ExpandedCount()
	expectedCount := expectedPerNamespace
	if resourceSpec.ForEachNamespace {
		expectedCount *= len(namespaces)
		for _, namespace := range namespaces {
			expectedNamespaces[namespace] = true
		}
	}

	watchContext, cancel := context.WithCancel(ctx)
	defer cancel()
	ready := make(chan struct{})
	done := make(chan struct{})
	errChannel := make(chan error, 1)
	var once sync.Once
	var mutex sync.Mutex
	seen := map[string]string{}
	seenByNamespace := map[string]int{}
	handler := func(object *unstructured.Unstructured, observedAt time.Time) {
		if resourceSpec.ForEachNamespace && !expectedNamespaces[object.GetNamespace()] {
			return
		}
		key := compositionKey(object.GetNamespace(), object.GetName())
		mutex.Lock()
		defer mutex.Unlock()
		if previousUID, found := seen[key]; found {
			if previousUID != string(object.GetUID()) {
				once.Do(func() {
					errChannel <- fmt.Errorf("generated %s %q changed UID", resourceSpec.Kind, key)
					close(done)
				})
			}
			return
		}
		startedAt := object.GetCreationTimestamp().Time
		if startedAt.IsZero() {
			startedAt = observedAt
		}
		if err := recordResourceMemory(r.store, resourceSpec.Name, "setup", object, resource, startedAt, observedAt); err != nil {
			once.Do(func() {
				errChannel <- err
				close(done)
			})
			return
		}
		seen[key] = string(object.GetUID())
		seenByNamespace[object.GetNamespace()]++
		if resourceSpec.ForEachNamespace && seenByNamespace[object.GetNamespace()] > expectedPerNamespace {
			once.Do(func() {
				errChannel <- fmt.Errorf("observed more than %d generated %s resources in namespace %s", expectedPerNamespace, resourceSpec.Kind, object.GetNamespace())
				close(done)
			})
			return
		}
		if len(seen) == expectedCount {
			once.Do(func() { close(done) })
		}
	}
	selector := render.RunIDLabel + "=" + runID + "," + render.ResourceIDLabel + "=" + resourceSpec.Name
	go func() {
		err := r.client.Watch(watchContext, resource.GVR, metav1.NamespaceAll, selector, ready, handler)
		if watchContext.Err() == nil {
			errChannel <- err
		}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errChannel:
		return err
	case <-ready:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errChannel:
		return err
	case <-done:
		select {
		case err := <-errChannel:
			return err
		default:
			return r.store.Save()
		}
	}
}

func (r *runtime) waitForReadiness(ctx context.Context, logicalName, expectedStatus string, expectedMissing []string) error {
	var records []stressstate.Resource
	var runID string
	r.store.Read(func(data *stressstate.State) error {
		runID = data.RunID
		for _, resource := range data.Resources {
			if resource.Phase == "setup" && resource.LogicalName == logicalName {
				records = append(records, resource)
			}
		}
		return nil
	})
	if len(records) == 0 {
		return fmt.Errorf("readiness resource %q has no recorded objects", logicalName)
	}
	gvr := gvrFor(records[0])
	for _, record := range records[1:] {
		if gvrFor(record) != gvr {
			return fmt.Errorf("readiness resource %q expands to multiple GVRs", logicalName)
		}
	}

	watchContext, cancel := context.WithCancel(ctx)
	defer cancel()
	ready := make(chan struct{})
	done := make(chan struct{})
	errChannel := make(chan error, 1)
	var once sync.Once
	var mutex sync.Mutex
	seen := map[string]bool{}
	handler := func(object *unstructured.Unstructured, _ time.Time) {
		if object.GetLabels()[render.ResourceIDLabel] != logicalName {
			return
		}
		key := object.GetNamespace() + "/" + object.GetName()
		mutex.Lock()
		defer mutex.Unlock()
		if synthesisExists(object) {
			once.Do(func() {
				errChannel <- fmt.Errorf("%s became synthesizable before run", key)
				close(done)
			})
			return
		}
		if simplifiedStatus(object) != expectedStatus {
			delete(seen, key)
			return
		}
		revisions := inputRevisionKeys(object)
		for _, missing := range expectedMissing {
			if _, found := revisions[missing]; found {
				once.Do(func() {
					errChannel <- fmt.Errorf("%s unexpectedly has required input %q", key, missing)
					close(done)
				})
				return
			}
		}
		seen[key] = true
		if len(seen) == len(records) {
			once.Do(func() { close(done) })
		}
	}
	go func() {
		err := r.client.Watch(watchContext, gvr, metav1.NamespaceAll, render.RunIDLabel+"="+runID, ready, handler)
		if watchContext.Err() == nil {
			errChannel <- err
		}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errChannel:
		return err
	case <-ready:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errChannel:
		return err
	case <-done:
		select {
		case err := <-errChannel:
			return err
		default:
			return nil
		}
	}
}

func listResource(ctx context.Context, r *runtime, gvr schema.GroupVersionResource, namespace string) (*unstructured.UnstructuredList, error) {
	return r.client.Dynamic.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
}
