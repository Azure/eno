package plan

import (
	"fmt"
	"time"
)

const (
	APIVersion = "stress.eno.azure.io/v1alpha1"
	Kind       = "StressTestPlan"

	CompletionStagePreSynthesis  CompletionStage = "PreSynthesis"
	CompletionStagePostSynthesis CompletionStage = "PostSynthesis"
	CompletionStageReconcile     CompletionStage = "Reconcile"
)

type CompletionStage string

type Plan struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind" json:"kind"`
	Metadata   Metadata `yaml:"metadata" json:"metadata"`
	Spec       Spec     `yaml:"spec" json:"spec"`
}

type Metadata struct {
	Name string `yaml:"name" json:"name"`
}

type Spec struct {
	Run     RunSpec     `yaml:"run" json:"run"`
	Setup   SetupSpec   `yaml:"setup" json:"setup"`
	Test    TestSpec    `yaml:"test" json:"test"`
	Output  OutputSpec  `yaml:"output" json:"output"`
	Metrics MetricsSpec `yaml:"metrics,omitempty" json:"metrics,omitempty"`
}

type RunSpec struct {
	NamespacePrefix string            `yaml:"namespacePrefix" json:"namespacePrefix"`
	NamespaceCount  int               `yaml:"namespaceCount" json:"namespaceCount"`
	CompletionStage CompletionStage   `yaml:"completionStage,omitempty" json:"completionStage,omitempty"`
	Concurrency     int               `yaml:"concurrency" json:"concurrency"`
	BatchSize       int               `yaml:"batchSize,omitempty" json:"batchSize,omitempty"`
	BatchDelay      string            `yaml:"batchDelay,omitempty" json:"batchDelay,omitempty"`
	Repetitions     int               `yaml:"repetitions,omitempty" json:"repetitions,omitempty"`
	Timeout         string            `yaml:"timeout" json:"timeout"`
	ClientQPS       float32           `yaml:"clientQPS,omitempty" json:"clientQPS,omitempty"`
	ClientBurst     int               `yaml:"clientBurst,omitempty" json:"clientBurst,omitempty"`
	Labels          map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Variables       map[string]string `yaml:"variables,omitempty" json:"variables,omitempty"`
}

type SetupSpec struct {
	Resources []ResourceSpec  `yaml:"resources" json:"resources"`
	Readiness []ReadinessSpec `yaml:"readiness" json:"readiness"`
}

type ResourceSpec struct {
	Name             string         `yaml:"name" json:"name"`
	Count            int            `yaml:"count,omitempty" json:"count,omitempty"`
	Reuse            bool           `yaml:"reuse,omitempty" json:"reuse,omitempty"`
	APIVersion       string         `yaml:"apiVersion,omitempty" json:"apiVersion,omitempty"`
	Kind             string         `yaml:"kind,omitempty" json:"kind,omitempty"`
	Scope            string         `yaml:"scope,omitempty" json:"scope,omitempty"`
	ForEachNamespace bool           `yaml:"forEachNamespace,omitempty" json:"forEachNamespace,omitempty"`
	TemplateFile     string         `yaml:"templateFile,omitempty" json:"templateFile,omitempty"`
	Template         map[string]any `yaml:"template,omitempty" json:"template,omitempty"`
	DependsOn        []string       `yaml:"dependsOn,omitempty" json:"dependsOn,omitempty"`
	Operation        string         `yaml:"operation,omitempty" json:"operation,omitempty"`
}

func (r ResourceSpec) ExpandedCount() int {
	if r.Count > 0 {
		return r.Count
	}
	return 1
}

type ReadinessSpec struct {
	Resource  string             `yaml:"resource" json:"resource"`
	Condition ReadinessCondition `yaml:"condition" json:"condition"`
}

type ReadinessCondition struct {
	Status                string   `yaml:"status" json:"status"`
	ExpectedMissingInputs []string `yaml:"expectedMissingInputs,omitempty" json:"expectedMissingInputs,omitempty"`
}

type TestSpec struct {
	Phases          []PhaseSpec        `yaml:"phases" json:"phases"`
	SuccessCriteria []SuccessCriterion `yaml:"successCriteria" json:"successCriteria"`
}

type PhaseSpec struct {
	Name        string         `yaml:"name" json:"name"`
	Concurrency int            `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`
	BatchSize   int            `yaml:"batchSize,omitempty" json:"batchSize,omitempty"`
	Delay       string         `yaml:"delay,omitempty" json:"delay,omitempty"`
	Repetitions int            `yaml:"repetitions,omitempty" json:"repetitions,omitempty"`
	DependsOn   []string       `yaml:"dependsOn,omitempty" json:"dependsOn,omitempty"`
	Resources   []ResourceSpec `yaml:"resources" json:"resources"`
}

type SuccessCriterion map[string]string

type OutputSpec struct {
	APIVersion   string            `yaml:"apiVersion,omitempty" json:"apiVersion,omitempty"`
	Kind         string            `yaml:"kind" json:"kind"`
	Name         string            `yaml:"name" json:"name"`
	ExpectedData map[string]string `yaml:"expectedData" json:"expectedData"`
}

type MetricsSpec struct {
	Collect    []string `yaml:"collect,omitempty" json:"collect,omitempty"`
	ReportFile string   `yaml:"reportFile,omitempty" json:"reportFile,omitempty"`
}

func (p *Plan) ApplyDefaults() {
	if p.Spec.Run.CompletionStage == "" {
		p.Spec.Run.CompletionStage = CompletionStageReconcile
	}
	if p.Spec.Run.Concurrency == 0 {
		p.Spec.Run.Concurrency = 1
	}
	if p.Spec.Run.BatchSize == 0 {
		p.Spec.Run.BatchSize = p.Spec.Run.NamespaceCount
	}
	if p.Spec.Run.Repetitions == 0 {
		p.Spec.Run.Repetitions = 1
	}
	if p.Spec.Run.ClientQPS == 0 {
		p.Spec.Run.ClientQPS = float32(max(100, p.Spec.Run.Concurrency*2))
	}
	if p.Spec.Run.ClientBurst == 0 {
		p.Spec.Run.ClientBurst = max(200, p.Spec.Run.Concurrency*4)
	}
	if p.Spec.Output.APIVersion == "" {
		p.Spec.Output.APIVersion = "v1"
	}
	for phaseIndex := range p.Spec.Test.Phases {
		phase := &p.Spec.Test.Phases[phaseIndex]
		if phase.Concurrency == 0 {
			phase.Concurrency = p.Spec.Run.Concurrency
		}
		if phase.BatchSize == 0 {
			phase.BatchSize = p.Spec.Run.BatchSize
		}
		if phase.Repetitions == 0 {
			phase.Repetitions = p.Spec.Run.Repetitions
		}
		for resourceIndex := range phase.Resources {
			if phase.Resources[resourceIndex].Operation == "" {
				phase.Resources[resourceIndex].Operation = "create"
			}
		}
	}
}

func (p *Plan) Timeout() (time.Duration, error) {
	timeout, err := time.ParseDuration(p.Spec.Run.Timeout)
	if err != nil || timeout <= 0 {
		return 0, fmt.Errorf("spec.run.timeout must be a positive duration: %q", p.Spec.Run.Timeout)
	}
	return timeout, nil
}
