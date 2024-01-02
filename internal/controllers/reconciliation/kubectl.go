package reconciliation

// Everything in this file was adapted from kubectl's openapi library.
// It essentially implements the same behavior with various performance optimizations.

import (
	openapi_v2 "github.com/google/gnostic-models/openapiv2"
	"gopkg.in/yaml.v2"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kube-openapi/pkg/util/proto"
)

func buildCurrentSchemaMap(doc *openapi_v2.Document) map[schema.GroupVersionKind]proto.Schema {
	models, err := proto.NewOpenAPIData(doc)
	if err != nil {
		panic(err) // TODO:?
	}

	allSupported := map[schema.GroupVersionKind]struct{}{}
	for _, path := range doc.GetPaths().GetPath() {
		for _, ex := range path.GetValue().GetPatch().GetVendorExtension() {
			if ex.GetValue().GetYaml() == "" ||
				ex.GetName() != "x-kubernetes-group-version-kind" {
				continue
			}

			var value map[string]string
			err := yaml.Unmarshal([]byte(ex.GetValue().GetYaml()), &value)
			if err != nil {
				continue
			}

			gvk := schema.GroupVersionKind{
				Group:   value["group"],
				Version: value["version"],
				Kind:    value["kind"],
			}
			for _, c := range path.GetValue().GetPatch().GetConsumes() {
				if c == string(types.StrategicMergePatchType) {
					allSupported[gvk] = struct{}{}
					break
				}
			}
		}
	}

	m := map[schema.GroupVersionKind]proto.Schema{}
	for _, modelName := range models.ListModels() {
		model := models.LookupModel(modelName)
		gvkList := parseGroupVersionKind(model)
		for _, gvk := range gvkList {
			if len(gvk.Kind) > 0 {
				if _, ok := allSupported[gvk]; ok {
					m[gvk] = model
				} else {
					m[gvk] = nil // unsupported == map key with nil model
				}
			}
		}
	}

	return m
}

func parseGroupVersionKind(s proto.Schema) []schema.GroupVersionKind {
	extensions := s.GetExtensions()
	gvkListResult := []schema.GroupVersionKind{}

	gvkExtension, ok := extensions["x-kubernetes-group-version-kind"]
	if !ok {
		return []schema.GroupVersionKind{}
	}

	gvkList, ok := gvkExtension.([]interface{})
	if !ok {
		return []schema.GroupVersionKind{}
	}

	for _, gvk := range gvkList {
		gvkMap, ok := gvk.(map[interface{}]interface{})
		if !ok {
			continue
		}
		group, ok := gvkMap["group"].(string)
		if !ok {
			continue
		}
		version, ok := gvkMap["version"].(string)
		if !ok {
			continue
		}
		kind, ok := gvkMap["kind"].(string)
		if !ok {
			continue
		}

		gvkListResult = append(gvkListResult, schema.GroupVersionKind{
			Group:   group,
			Version: version,
			Kind:    kind,
		})
	}

	return gvkListResult
}
