// Plan validation and expansion for the stress CLI.
package runner

import (
	"context"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Azure/eno/e2e/stress/internal/plan"
	"github.com/Azure/eno/e2e/stress/internal/render"
)

func Validate(ctx context.Context, options Options) error {
	r, err := loadRuntime(options, false)
	if err != nil {
		return err
	}
	namespaces := namespaceNames(r.plan, "validation-00000000")
	type identity struct {
		phase string
		name  string
	}
	identities := map[string]identity{}
	counts := map[string]int{"Namespace": len(namespaces)}
	renderOne := func(phase string, resource plan.ResourceSpec, context render.Context) error {
		object, err := render.Resource(r.baseDir, resource, context)
		if err != nil {
			return err
		}
		mapped, err := r.client.ResourceFor(object)
		if err != nil {
			return err
		}
		key := mapped.GVR.String() + "/" + object.GetNamespace() + "/" + object.GetName()
		if previous, found := identities[key]; found && resource.Operation != "update" && resource.Operation != "apply" {
			return fmt.Errorf("duplicate rendered identity %s from %s/%s and %s/%s", key, previous.phase, previous.name, phase, resource.Name)
		}
		identities[key] = identity{phase: phase, name: resource.Name}
		counts[object.GetKind()]++
		if options.ServerDryRun {
			if _, _, err := r.client.CreateSetup(ctx, object, context.RunID, true, resource.Reuse); err != nil {
				return fmt.Errorf("server dry-run %s/%s: %w", phase, resource.Name, err)
			}
		}
		return nil
	}

	ordered, err := orderedSetup(r.plan.Spec.Setup.Resources)
	if err != nil {
		return err
	}
	for _, resource := range ordered {
		if resource.Operation == "observe" {
			probe := &unstructured.Unstructured{}
			probe.SetAPIVersion(resource.APIVersion)
			probe.SetKind(resource.Kind)
			if resource.ForEachNamespace && len(namespaces) > 0 {
				probe.SetNamespace(namespaces[0])
			}
			if _, err := r.client.ResourceFor(probe); err != nil {
				return err
			}
			count := resource.ExpandedCount()
			if resource.ForEachNamespace {
				count *= len(namespaces)
			}
			counts[resource.Kind] += count
			continue
		}
		for _, context := range renderContexts(r.plan, "validation-00000000", namespaces, "setup", 1, resource) {
			if err := renderOne("setup", resource, context); err != nil {
				return err
			}
		}
	}
	for _, phase := range r.plan.Spec.Test.Phases {
		for _, resource := range phase.Resources {
			for _, context := range renderContexts(r.plan, "validation-00000000", namespaces, phase.Name, 1, resource) {
				if err := renderOne(phase.Name, resource, context); err != nil {
					return err
				}
			}
		}
	}

	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	r.output("plan %q is valid\n", r.plan.Metadata.Name)
	r.output("expected resources:\n")
	for _, kind := range kinds {
		r.output("  %-24s %d\n", kind, counts[kind])
	}
	r.output("execution plan:\n  prepare: namespaces -> ")
	for index, resource := range ordered {
		if index > 0 {
			r.output(" -> ")
		}
		r.output("%s", resource.Name)
	}
	r.output(" -> readiness\n")
	for _, phase := range r.plan.Spec.Test.Phases {
		r.output("  run: %s (concurrency=%d batchSize=%d repetitions=%d)\n", phase.Name, phase.Concurrency, phase.BatchSize, phase.Repetitions)
	}
	return nil
}
