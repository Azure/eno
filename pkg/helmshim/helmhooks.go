package helmshim

import (
	"fmt"
	"slices"
	"sort"
	"strconv"

	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/release"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TODO: move to a common package
const readinessKey = "eno.azure.io/readiness-group"

func AddReadinessGroup(hooks []*release.Hook) ([]*release.Hook, error) {
	preHooks := []*release.Hook{}
	postHooks := []*release.Hook{}

	for _, hook := range hooks {
		// build pre actions
		if containsPreHooks(hook) {
			preHooks = append(preHooks, hook)
		}
		// build post actions
		if containsPostHooks(hook) {
			postHooks = append(postHooks, hook)
		}
	}

	err := addReadinesGroupToPreHooks(preHooks)
	if err != nil {
		return nil, err
	}

	hs := append(preHooks, postHooks...)

	return hs, nil
}

func containsPreHooks(hook *release.Hook) bool {
	return slices.Contains(hook.Events, release.HookPreInstall) ||
		slices.Contains(hook.Events, release.HookPreUpgrade) ||
		slices.Contains(hook.Events, release.HookPreRollback)
}

func containsPostHooks(hook *release.Hook) bool {
	return slices.Contains(hook.Events, release.HookPostInstall) ||
		slices.Contains(hook.Events, release.HookPostUpgrade) ||
		slices.Contains(hook.Events, release.HookPostRollback)
}

func addReadinesGroupToPreHooks(hooks []*release.Hook) error {
	if hooks == nil || len(hooks) == 0 {
		return nil
	}

	// descending sort the hooks by weight
	sort.Slice(hooks, func(i, j int) bool {
		return hooks[i].Weight > hooks[j].Weight
	})

	// start from the hook with largest weight which readiness group will be -1
	prevWeight := hooks[0].Weight
	readinessGroup := -1
	for _, h := range hooks {
		// if the weight is different from the previous one, decrease the readiness group
		if h.Weight != prevWeight {
			prevWeight = h.Weight
			readinessGroup--
		}

		var err error
		h.Manifest, err = addAnnotation(h.Manifest, readinessGroup)
		if err != nil {
			return err
		}
	}

	return nil
}

func addReadinessToPostHooks(hooks []*release.Hook) error {
	if hooks == nil || len(hooks) == 0 {
		return nil
	}

	// acending sort the hooks by weight
	sort.Slice(hooks, func(i, j int) bool {
		return hooks[i].Weight < hooks[j].Weight
	})

	prevWeight := hooks[0].Weight
	readinessGroup := 1

	for _, h := range hooks {
		// if the weight is different from the previous one, increase the readiness group
		if h.Weight != prevWeight {
			prevWeight = h.Weight
			readinessGroup++
		}

		var err error
		h.Manifest, err = addAnnotation(h.Manifest, readinessGroup)
		if err != nil {
			return err
		}
	}

	return nil
}

func addAnnotation(manifest string, readinessGroup int) (string, error) {
	obj := map[string]interface{}{}
	err := yaml.Unmarshal([]byte(manifest), &obj)
	if err != nil {
		return manifest, err
	}

	un := &unstructured.Unstructured{
		Object: obj,
	}

	// helm hook is defined in the annotations and it is impossible to have a helm hook without annotations
	anno := un.GetAnnotations()
	if anno == nil {
		return manifest, fmt.Errorf("annotations not found in helm hook manifest %s", un.GetName())
	}

	anno[readinessKey] = strconv.Itoa(readinessGroup)
	un.SetAnnotations(anno)

	m, _ := yaml.Marshal(un)

	return string(m), nil
}
