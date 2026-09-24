// Shared orchestration helpers for live stress commands.
package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Azure/eno/e2e/stress/internal/kube"
	"github.com/Azure/eno/e2e/stress/internal/plan"
	"github.com/Azure/eno/e2e/stress/internal/render"
	stressstate "github.com/Azure/eno/e2e/stress/internal/state"
)

type Options struct {
	PlanPath     string
	StatePath    string
	Kubeconfig   string
	RunID        string
	ServerDryRun bool
	Output       func(string, ...any)
}

type runtime struct {
	plan     *plan.Plan
	planPath string
	baseDir  string
	client   *kube.Client
	store    *stressstate.Store
	output   func(string, ...any)
}

func loadRuntime(options Options, withState bool) (*runtime, error) {
	planPath, err := filepath.Abs(options.PlanPath)
	if err != nil {
		return nil, err
	}
	loadedPlan, err := plan.Load(planPath)
	if err != nil {
		return nil, err
	}
	client, err := kube.New(options.Kubeconfig, loadedPlan.Spec.Run.ClientQPS, loadedPlan.Spec.Run.ClientBurst)
	if err != nil {
		return nil, err
	}
	r := &runtime{
		plan:     loadedPlan,
		planPath: planPath,
		baseDir:  filepath.Dir(planPath),
		client:   client,
		output:   options.Output,
	}
	if r.output == nil {
		r.output = func(string, ...any) {}
	}
	if withState {
		if options.StatePath == "" {
			return nil, fmt.Errorf("--state is required")
		}
		r.store, err = prepareStore(options, loadedPlan, planPath)
		if err != nil {
			return nil, err
		}
	}
	return r, nil
}

func loadRunRuntime(options Options) (*runtime, error) {
	if options.StatePath == "" {
		return nil, fmt.Errorf("--state is required")
	}
	store, err := stressstate.Load(options.StatePath)
	if err != nil {
		return nil, err
	}
	var planPath, expectedDigest string
	store.Read(func(data *stressstate.State) error {
		planPath = data.PlanPath
		expectedDigest = data.PlanDigest
		return nil
	})
	loadedPlan, err := plan.Load(planPath)
	if err != nil {
		return nil, err
	}
	digest, err := stressstate.FileDigest(planPath)
	if err != nil {
		return nil, err
	}
	if digest != expectedDigest {
		return nil, fmt.Errorf("plan changed after prepare: expected %s, got %s", expectedDigest, digest)
	}
	client, err := kube.New(options.Kubeconfig, loadedPlan.Spec.Run.ClientQPS, loadedPlan.Spec.Run.ClientBurst)
	if err != nil {
		return nil, err
	}
	output := options.Output
	if output == nil {
		output = func(string, ...any) {}
	}
	return &runtime{
		plan:     loadedPlan,
		planPath: planPath,
		baseDir:  filepath.Dir(planPath),
		client:   client,
		store:    store,
		output:   output,
	}, nil
}

func prepareStore(options Options, loadedPlan *plan.Plan, planPath string) (*stressstate.Store, error) {
	digest, err := stressstate.FileDigest(planPath)
	if err != nil {
		return nil, err
	}
	if existing, err := stressstate.Load(options.StatePath); err == nil {
		var mismatch error
		existing.Read(func(data *stressstate.State) error {
			if data.PlanDigest != digest {
				mismatch = fmt.Errorf("state plan digest does not match %s", planPath)
			}
			return nil
		})
		return existing, mismatch
	}
	runID := options.RunID
	if runID == "" {
		runID = newRunID(loadedPlan.Metadata.Name)
	}
	now := time.Now()
	data := &stressstate.State{
		SchemaVersion:    stressstate.SchemaVersion,
		RunID:            runID,
		PlanName:         loadedPlan.Metadata.Name,
		PlanPath:         planPath,
		PlanDigest:       digest,
		CompletionStage:  string(loadedPlan.Spec.Run.CompletionStage),
		Phase:            "preparing",
		CreatedAt:        now,
		NamespaceInputs:  map[string]map[string]*stressstate.InputTiming{},
		CompositionState: map[string]*stressstate.CompositionResult{},
	}
	store := stressstate.NewStore(options.StatePath, data)
	return store, store.Save()
}

func newRunID(planName string) string {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		panic(err)
	}
	suffix := hex.EncodeToString(random)
	maxPrefix := 63 - len(suffix) - 1
	if len(planName) > maxPrefix {
		planName = strings.TrimRight(planName[:maxPrefix], "-")
	}
	return planName + "-" + suffix
}

func namespaceNames(p *plan.Plan, runID string) []string {
	names := make([]string, p.Spec.Run.NamespaceCount)
	for index := range names {
		prefix := p.Spec.Run.NamespacePrefix
		guid := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%s/%d", runID, index+1)))
		guidSuffix := strings.ReplaceAll(guid.String(), "-", "")[:16]
		if len(prefix)+len(guidSuffix) > 63 {
			prefix = strings.TrimRight(prefix[:63-len(guidSuffix)], "-")
		}
		names[index] = prefix + guidSuffix
	}
	return names
}

func renderContexts(p *plan.Plan, runID string, namespaces []string, phase string, iteration int, spec plan.ResourceSpec) []render.Context {
	base := render.Context{
		RunID:     runID,
		PlanName:  p.Metadata.Name,
		Phase:     phase,
		Iteration: iteration,
		Variables: p.Spec.Run.Variables,
		Labels:    p.Spec.Run.Labels,
	}
	count := spec.ExpandedCount()
	if spec.ForEachNamespace {
		contexts := make([]render.Context, 0, len(namespaces)*count)
		for namespaceIndex, namespace := range namespaces {
			for resourceIndex := 1; resourceIndex <= count; resourceIndex++ {
				context := base
				context.Namespace = namespace
				context.NamespaceIndex = namespaceIndex + 1
				context.ResourceIndex = resourceIndex
				contexts = append(contexts, context)
			}
		}
		return contexts
	}
	contexts := make([]render.Context, count)
	for resourceIndex := 1; resourceIndex <= count; resourceIndex++ {
		context := base
		context.ResourceIndex = resourceIndex
		contexts[resourceIndex-1] = context
	}
	return contexts
}

func orderedSetup(resources []plan.ResourceSpec) ([]plan.ResourceSpec, error) {
	pending := make(map[string]plan.ResourceSpec, len(resources))
	for _, resource := range resources {
		pending[resource.Name] = resource
	}
	done := map[string]bool{}
	ordered := make([]plan.ResourceSpec, 0, len(resources))
	for len(pending) > 0 {
		progress := false
		names := make([]string, 0, len(pending))
		for name := range pending {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			resource := pending[name]
			ready := true
			for _, dependency := range resource.DependsOn {
				ready = ready && done[dependency]
			}
			if !ready {
				continue
			}
			ordered = append(ordered, resource)
			done[name] = true
			delete(pending, name)
			progress = true
		}
		if !progress {
			return nil, fmt.Errorf("setup resource dependency cycle")
		}
	}
	return ordered, nil
}

func parallel(ctx context.Context, concurrency int, count int, fn func(context.Context, int) error) error {
	group, groupContext := errgroup.WithContext(ctx)
	group.SetLimit(concurrency)
	for index := 0; index < count; index++ {
		index := index
		group.Go(func() error { return fn(groupContext, index) })
	}
	return group.Wait()
}

func recordResource(store *stressstate.Store, logicalName, phase string, object *unstructured.Unstructured, resource *kube.Resource, startedAt, acknowledgedAt time.Time) error {
	return store.Update(func(data *stressstate.State) error {
		upsertResource(data, resourceRecord(logicalName, phase, object, resource, startedAt, acknowledgedAt))
		return nil
	})
}

func recordResourceMemory(store *stressstate.Store, logicalName, phase string, object *unstructured.Unstructured, resource *kube.Resource, startedAt, acknowledgedAt time.Time) error {
	return store.Mutate(func(data *stressstate.State) error {
		upsertResource(data, resourceRecord(logicalName, phase, object, resource, startedAt, acknowledgedAt))
		return nil
	})
}

func resourceRecord(logicalName, phase string, object *unstructured.Unstructured, resource *kube.Resource, startedAt, acknowledgedAt time.Time) stressstate.Resource {
	record := stressstate.Resource{
		LogicalName:       logicalName,
		Phase:             phase,
		APIVersion:        object.GetAPIVersion(),
		Kind:              object.GetKind(),
		Group:             resource.GVR.Group,
		Version:           resource.GVR.Version,
		Resource:          resource.GVR.Resource,
		Scope:             resource.Scope,
		Namespace:         object.GetNamespace(),
		Name:              object.GetName(),
		UID:               string(object.GetUID()),
		ResourceVersion:   object.GetResourceVersion(),
		CreationTimestamp: object.GetCreationTimestamp().Time,
		RequestStartedAt:  startedAt,
		APIAcknowledgedAt: acknowledgedAt,
	}
	return record
}

func upsertResource(data *stressstate.State, record stressstate.Resource) {
	for index := range data.Resources {
		existing := &data.Resources[index]
		if existing.Phase == record.Phase && existing.LogicalName == record.LogicalName && existing.Namespace == record.Namespace && existing.Name == record.Name {
			*existing = record
			return
		}
	}
	data.Resources = append(data.Resources, record)
}

func gvrFor(record stressstate.Resource) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: record.Group, Version: record.Version, Resource: record.Resource}
}

func compositionKey(namespace, name string) string {
	return namespace + "/" + name
}

func expandedResourceCount(p *plan.Plan, logicalName string) int {
	for _, resource := range p.Spec.Setup.Resources {
		if resource.Name != logicalName {
			continue
		}
		count := resource.ExpandedCount()
		if resource.ForEachNamespace {
			count *= p.Spec.Run.NamespaceCount
		}
		return count
	}
	return 0
}

func simplifiedStatus(object *unstructured.Unstructured) string {
	value, _, _ := unstructured.NestedString(object.Object, "status", "simplified", "status")
	return value
}

func synthesisExists(object *unstructured.Unstructured) bool {
	inFlight, foundInFlight, _ := unstructured.NestedMap(object.Object, "status", "inFlightSynthesis")
	current, foundCurrent, _ := unstructured.NestedMap(object.Object, "status", "currentSynthesis")
	return (foundInFlight && len(inFlight) > 0) || (foundCurrent && len(current) > 0)
}

func inputRevisionKeys(object *unstructured.Unstructured) map[string]string {
	items, _, _ := unstructured.NestedSlice(object.Object, "status", "inputRevisions")
	result := map[string]string{}
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key, _, _ := unstructured.NestedString(entry, "key")
		resourceVersion, _, _ := unstructured.NestedString(entry, "resourceVersion")
		result[key] = resourceVersion
	}
	return result
}

func namespaceObject(name, runID, planName string, labels map[string]string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name": name,
		},
	}}
	allLabels := map[string]string{}
	for key, value := range labels {
		allLabels[key] = value
	}
	allLabels[render.RunIDLabel] = runID
	allLabels[render.PlanLabel] = planName
	allLabels[render.ResourceIDLabel] = "namespace"
	object.SetLabels(allLabels)
	return object
}

func creationTime(object *unstructured.Unstructured) time.Time {
	if object.GetCreationTimestamp() == (metav1.Time{}) {
		return time.Time{}
	}
	return object.GetCreationTimestamp().Time
}
