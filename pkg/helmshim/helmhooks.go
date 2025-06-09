package helmshim

import (
	"fmt"
	"slices"
	"sort"
	"strconv"

	"helm.sh/helm/v3/pkg/release"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// TODO: move to a common package
const readinessKey = "eno.azure.io/readiness-group"

func setReadinessToHooks(hooks []*release.Hook) error {
	preHooks := []*release.Hook{}
	postHooks := []*release.Hook{}

	for _, hook := range hooks {
		// Build pre actions
		if containsPreHooks(hook) {
			preHooks = append(preHooks, hook)
		}
		// Build post actions
		if containsPostHooks(hook) {
			postHooks = append(postHooks, hook)
		}
	}

	err := setReadinessToPreHooks(preHooks)
	if err != nil {
		return err
	}
	err = setReadinessToPostHooks(postHooks)
	if err != nil {
		return err
	}

	hooks = append(preHooks, postHooks...)

	return nil
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

func setReadinessToPreHooks(hooks []*release.Hook) error {
	if hooks == nil || len(hooks) == 0 {
		return nil
	}

	// Descending sort the hooks by weight
	sort.Slice(hooks, func(i, j int) bool {
		return hooks[i].Weight > hooks[j].Weight
	})

	// Start from the hook with largest weight which readiness group will be -1
	prevWeight := hooks[0].Weight
	readinessGroup := -1
	for _, h := range hooks {
		// If the weight is different from the previous one, decrease the readiness group
		if h.Weight != prevWeight {
			prevWeight = h.Weight
			readinessGroup--
		}

		err := setReadinessAnnotation(h, readinessGroup)
		if err != nil {
			return err
		}
	}

	return nil
}

func setReadinessToPostHooks(hooks []*release.Hook) error {
	if hooks == nil || len(hooks) == 0 {
		return nil
	}

	// Acending sort the hooks by weight
	sort.Slice(hooks, func(i, j int) bool {
		return hooks[i].Weight < hooks[j].Weight
	})

	// Start from the hook with the most small weight which readiness group will be 1
	prevWeight := hooks[0].Weight
	readinessGroup := 1
	for _, h := range hooks {
		// If the weight is different from the previous one, increase the readiness group
		if h.Weight != prevWeight {
			prevWeight = h.Weight
			readinessGroup++
		}

		err := setReadinessAnnotation(h, readinessGroup)
		if err != nil {
			return err
		}
	}

	return nil
}

func setReadinessAnnotation(hook *release.Hook, readinessGroup int) error {
	un := &unstructured.Unstructured{}
	err := yaml.Unmarshal([]byte(hook.Manifest), &un.Object)
	if err != nil {
		return err
	}

	// Helm hook is defined in the annotations and it is impossible to have a helm hook without annotations
	anno := un.GetAnnotations()
	if anno == nil {
		return fmt.Errorf("annotations not found in helm hook manifest: %s", un.GetName())
	}

	anno[readinessKey] = strconv.Itoa(readinessGroup)
	un.SetAnnotations(anno)

	m, _ := yaml.Marshal(un.Object)
	hook.Manifest = string(m)

	return nil
}
