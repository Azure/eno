package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

func (p *Plan) Validate(baseDir string) error {
	if p.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion must be %q", APIVersion)
	}
	if p.Kind != Kind {
		return fmt.Errorf("kind must be %q", Kind)
	}
	if errs := validation.IsDNS1123Subdomain(p.Metadata.Name); len(errs) > 0 {
		return fmt.Errorf("metadata.name is invalid: %s", strings.Join(errs, ", "))
	}
	if errs := validation.IsDNS1123Label(p.Spec.Run.NamespacePrefix); len(errs) > 0 {
		return fmt.Errorf("spec.run.namespacePrefix is invalid: %s", strings.Join(errs, ", "))
	}
	if p.Spec.Run.NamespaceCount <= 0 {
		return fmt.Errorf("spec.run.namespaceCount must be positive")
	}
	if !slices.Contains([]CompletionStage{CompletionStagePreSynthesis, CompletionStagePostSynthesis, CompletionStageReconcile}, p.Spec.Run.CompletionStage) {
		return fmt.Errorf("spec.run.completionStage must be PreSynthesis, PostSynthesis, or Reconcile")
	}
	if p.Spec.Run.Concurrency <= 0 || p.Spec.Run.BatchSize <= 0 {
		return fmt.Errorf("spec.run concurrency and batchSize must be positive")
	}
	if _, err := p.Timeout(); err != nil {
		return err
	}
	if p.Spec.Run.BatchDelay != "" {
		if delay, err := time.ParseDuration(p.Spec.Run.BatchDelay); err != nil || delay < 0 {
			return fmt.Errorf("spec.run.batchDelay must be a non-negative duration")
		}
	}

	setupNames := map[string]struct{}{}
	for index := range p.Spec.Setup.Resources {
		resource := &p.Spec.Setup.Resources[index]
		if resource.Operation != "" && resource.Operation != "create" && resource.Operation != "observe" {
			return fmt.Errorf("setup resource %q has unsupported operation %q", resource.Name, resource.Operation)
		}
		if err := validateResource(baseDir, resource); err != nil {
			return fmt.Errorf("setup resource %q: %w", resource.Name, err)
		}
		if _, found := setupNames[resource.Name]; found {
			return fmt.Errorf("duplicate setup resource name %q", resource.Name)
		}
		setupNames[resource.Name] = struct{}{}
	}
	for _, resource := range p.Spec.Setup.Resources {
		for _, dependency := range resource.DependsOn {
			if _, found := setupNames[dependency]; !found {
				return fmt.Errorf("setup resource %q depends on unknown resource %q", resource.Name, dependency)
			}
		}
	}
	for _, readiness := range p.Spec.Setup.Readiness {
		if _, found := setupNames[readiness.Resource]; !found {
			return fmt.Errorf("readiness references unknown resource %q", readiness.Resource)
		}
	}

	phaseNames := map[string]struct{}{}
	for phaseIndex := range p.Spec.Test.Phases {
		phase := &p.Spec.Test.Phases[phaseIndex]
		if phase.Name == "" {
			return fmt.Errorf("test phase %d has no name", phaseIndex)
		}
		if _, found := phaseNames[phase.Name]; found {
			return fmt.Errorf("duplicate test phase name %q", phase.Name)
		}
		for _, dependency := range phase.DependsOn {
			if _, found := phaseNames[dependency]; !found {
				return fmt.Errorf("test phase %q depends on unknown or later phase %q", phase.Name, dependency)
			}
		}
		phaseNames[phase.Name] = struct{}{}
		resourceNames := map[string]struct{}{}
		for resourceIndex := range phase.Resources {
			resource := &phase.Resources[resourceIndex]
			if resource.ExpandedCount() != 1 {
				return fmt.Errorf("test phase %q resource %q cannot set count", phase.Name, resource.Name)
			}
			if err := validateResource(baseDir, resource); err != nil {
				return fmt.Errorf("test phase %q resource %q: %w", phase.Name, resource.Name, err)
			}
			if _, found := resourceNames[resource.Name]; found {
				return fmt.Errorf("duplicate resource name %q in phase %q", resource.Name, phase.Name)
			}
			resourceNames[resource.Name] = struct{}{}
			if !slices.Contains([]string{"create", "apply", "update"}, resource.Operation) {
				return fmt.Errorf("test phase %q resource %q has unsupported operation %q", phase.Name, resource.Name, resource.Operation)
			}
		}
	}
	if len(p.Spec.Test.Phases) == 0 {
		return fmt.Errorf("spec.test.phases must not be empty")
	}
	if p.Spec.Output.Kind == "" || p.Spec.Output.Name == "" {
		return fmt.Errorf("spec.output kind and name are required")
	}
	return nil
}

func validateResource(baseDir string, resource *ResourceSpec) error {
	if resource.Name == "" {
		return fmt.Errorf("name is required")
	}
	if resource.Count < 0 {
		return fmt.Errorf("count must not be negative")
	}
	if resource.Operation == "observe" {
		if resource.TemplateFile != "" || len(resource.Template) != 0 {
			return fmt.Errorf("observed resources cannot define templateFile or template")
		}
		if resource.APIVersion == "" || resource.Kind == "" {
			return fmt.Errorf("observed resources require apiVersion and kind")
		}
		if resource.Scope != "cluster" && resource.Scope != "namespace" {
			return fmt.Errorf("observed resources require scope cluster or namespace")
		}
		return nil
	}
	if resource.TemplateFile == "" && len(resource.Template) == 0 {
		return fmt.Errorf("exactly one of templateFile or template is required")
	}
	if resource.TemplateFile != "" && len(resource.Template) != 0 {
		return fmt.Errorf("templateFile and template are mutually exclusive")
	}
	if resource.Scope != "" && resource.Scope != "cluster" && resource.Scope != "namespace" {
		return fmt.Errorf("scope must be cluster or namespace")
	}
	if resource.Scope == "cluster" && resource.ForEachNamespace {
		return fmt.Errorf("cluster-scoped resources cannot set forEachNamespace")
	}
	if resource.TemplateFile != "" {
		path := resource.TemplateFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("template file: %w", err)
		}
		if info.IsDir() {
			return fmt.Errorf("template file %q is a directory", resource.TemplateFile)
		}
	}
	return nil
}
