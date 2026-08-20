package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/Azure/eno/e2e/stress/internal/plan"
)

const (
	RunIDLabel      = "stress.eno.azure.io/run-id"
	PlanLabel       = "stress.eno.azure.io/plan"
	ResourceIDLabel = "stress.eno.azure.io/resource-id"
	VariationLabel  = "stress.eno.azure.io/variation"
)

var variablePattern = regexp.MustCompile(`\$\{([A-Za-z][A-Za-z0-9]*)\}`)

type Context struct {
	RunID          string
	PlanName       string
	Namespace      string
	NamespaceIndex int
	ResourceIndex  int
	Variation      string
	Phase          string
	Iteration      int
	Variables      map[string]string
	Labels         map[string]string
}

func Resource(baseDir string, spec plan.ResourceSpec, context Context) (*unstructured.Unstructured, error) {
	object, err := loadObject(baseDir, spec)
	if err != nil {
		return nil, err
	}

	variables := map[string]string{
		"runID":          context.RunID,
		"namespace":      context.Namespace,
		"namespaceIndex": strconv.Itoa(context.NamespaceIndex),
		"resourceIndex":  strconv.Itoa(context.ResourceIndex),
		"variation":      context.Variation,
		"phase":          context.Phase,
		"iteration":      strconv.Itoa(context.Iteration),
	}
	for key, value := range context.Variables {
		variables[key] = value
	}
	expanded, err := expand(object.Object, variables)
	if err != nil {
		return nil, fmt.Errorf("expanding resource %q: %w", spec.Name, err)
	}
	object.Object = expanded.(map[string]any)

	if object.GetAPIVersion() == "" {
		object.SetAPIVersion(spec.APIVersion)
	}
	if object.GetKind() == "" {
		object.SetKind(spec.Kind)
	}
	if object.GetName() == "" {
		object.SetName(spec.Name)
	}
	if spec.Scope != "cluster" && context.Namespace != "" {
		object.SetNamespace(context.Namespace)
	}
	if object.GetAPIVersion() == "" || object.GetKind() == "" || object.GetName() == "" {
		return nil, fmt.Errorf("resource %q must render apiVersion, kind, and metadata.name", spec.Name)
	}

	labels := object.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	for key, value := range context.Labels {
		labels[key] = value
	}
	labels[RunIDLabel] = context.RunID
	labels[PlanLabel] = context.PlanName
	labels[ResourceIDLabel] = spec.Name
	object.SetLabels(labels)
	return object, nil
}

func String(value string, context Context) (string, error) {
	variables := map[string]string{
		"runID":          context.RunID,
		"namespace":      context.Namespace,
		"namespaceIndex": strconv.Itoa(context.NamespaceIndex),
		"resourceIndex":  strconv.Itoa(context.ResourceIndex),
		"variation":      context.Variation,
		"phase":          context.Phase,
		"iteration":      strconv.Itoa(context.Iteration),
	}
	for key, variable := range context.Variables {
		variables[key] = variable
	}
	expanded, err := expand(value, variables)
	if err != nil {
		return "", err
	}
	return expanded.(string), nil
}

func loadObject(baseDir string, spec plan.ResourceSpec) (*unstructured.Unstructured, error) {
	if spec.TemplateFile == "" {
		raw, err := json.Marshal(spec.Template)
		if err != nil {
			return nil, fmt.Errorf("encoding inline template: %w", err)
		}
		object := &unstructured.Unstructured{}
		if err := json.Unmarshal(raw, &object.Object); err != nil {
			return nil, fmt.Errorf("decoding inline template: %w", err)
		}
		return object, nil
	}

	path := spec.TemplateFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading template %q: %w", spec.TemplateFile, err)
	}

	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	object := &unstructured.Unstructured{}
	if err := decoder.Decode(object); err != nil {
		return nil, fmt.Errorf("decoding template: %w", err)
	}
	var extra runtime.RawExtension
	if err := decoder.Decode(&extra); err == nil && len(bytes.TrimSpace(extra.Raw)) > 0 {
		return nil, fmt.Errorf("template must contain exactly one Kubernetes object")
	}
	return object, nil
}

func expand(value any, variables map[string]string) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			expanded, err := expand(item, variables)
			if err != nil {
				return nil, err
			}
			result[key] = expanded
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			expanded, err := expand(item, variables)
			if err != nil {
				return nil, err
			}
			result[index] = expanded
		}
		return result, nil
	case string:
		var missing string
		result := variablePattern.ReplaceAllStringFunc(typed, func(match string) string {
			key := strings.TrimSuffix(strings.TrimPrefix(match, "${"), "}")
			value, found := variables[key]
			if !found {
				missing = key
				return match
			}
			return value
		})
		if missing != "" {
			return nil, fmt.Errorf("unknown variable %q", missing)
		}
		return result, nil
	default:
		return value, nil
	}
}
