// Status reporting and deterministic owned-resource cleanup.
package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/Azure/eno/e2e/stress/internal/kube"
	"github.com/Azure/eno/e2e/stress/internal/report"
	stressstate "github.com/Azure/eno/e2e/stress/internal/state"
)

func Status(options Options) error {
	store, err := stressstate.Load(options.StatePath)
	if err != nil {
		return err
	}
	output := options.Output
	if output == nil {
		output = func(string, ...any) {}
	}
	return store.Read(func(data *stressstate.State) error {
		output("run ID: %s\nphase: %s\ncompletion stage: %s\nnamespaces: %d\ncompositions: %d\nresources: %d\n", data.RunID, data.Phase, data.CompletionStage, len(data.Namespaces), len(data.CompositionState), len(data.Resources))
		report.Print(report.Build(data), output)
		return nil
	})
}

func Cleanup(ctx context.Context, options Options) error {
	store, err := stressstate.Load(options.StatePath)
	if err != nil {
		return err
	}
	client, err := kube.New(options.Kubeconfig, 200, 400)
	if err != nil {
		return err
	}
	output := options.Output
	if output == nil {
		output = func(string, ...any) {}
	}
	var resources []stressstate.Resource
	var runID string
	store.Read(func(data *stressstate.State) error {
		runID = data.RunID
		resources = append(resources, data.Resources...)
		return nil
	})
	for index := len(resources) - 1; index >= 0; index-- {
		resource := resources[index]
		if resource.Kind != "Namespace" || resource.LogicalName != "namespace" {
			continue
		}
		if err := client.DeleteOwned(ctx, gvrFor(resource), resource.Namespace, resource.Name, resource.UID, runID); err != nil {
			return fmt.Errorf("deleting %s %s/%s: %w", resource.Kind, resource.Namespace, resource.Name, err)
		}
		output("deleted %s %s/%s\n", resource.Kind, resource.Namespace, resource.Name)
	}
	now := time.Now()
	return store.Update(func(data *stressstate.State) error {
		data.Phase = "cleaned"
		data.CompletedAt = &now
		return nil
	})
}
