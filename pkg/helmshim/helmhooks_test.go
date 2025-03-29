package helmshim

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"helm.sh/helm/v3/pkg/release"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

type hookName string
type readinessGroup string

func TestSetReadinessAnnotations(t *testing.T) {
	testCases := []struct {
		name           string
		hook           *release.Hook
		readinessGroup int
	}{
		{
			name: "test add negative readiness group annotation to hooks",
			hook: &release.Hook{
				Name:           "test-hook",
				Events:         []release.HookEvent{release.HookPreInstall},
				Weight:         1,
				DeletePolicies: []release.HookDeletePolicy{release.HookBeforeHookCreation},
			},
			readinessGroup: -1,
		},
		{
			name: "test add positive readiness group annotation to hooks",
			hook: &release.Hook{
				Name:           "test-hook",
				Events:         []release.HookEvent{release.HookPostInstall},
				Weight:         1,
				DeletePolicies: []release.HookDeletePolicy{release.HookBeforeHookCreation},
			},
			readinessGroup: 5,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			hook := tc.hook
			setConfigMapManifest(t, hook)
			setReadinessAnnotation(hook, tc.readinessGroup)

			un := &unstructured.Unstructured{}
			err := yaml.Unmarshal([]byte(hook.Manifest), &un.Object)
			assert.NoError(t, err)
			anno := un.GetAnnotations()
			assert.NotNil(t, anno)
			assert.Equal(t, strconv.Itoa(tc.readinessGroup), anno[readinessKey])
		})
	}
}

func TestSetReadinessToHooks(t *testing.T) {
	testCases := []struct {
		name     string
		hooks    []*release.Hook
		expected map[hookName]readinessGroup
	}{
		{
			name: "test readiness group annotations for pre hooks",
			hooks: []*release.Hook{
				{
					Name:           "test-hook",
					Events:         []release.HookEvent{release.HookPreInstall},
					Weight:         1,
					DeletePolicies: []release.HookDeletePolicy{release.HookBeforeHookCreation},
				},
				{
					Name:           "test-hook2",
					Events:         []release.HookEvent{release.HookPreUpgrade},
					Weight:         1,
					DeletePolicies: []release.HookDeletePolicy{release.HookBeforeHookCreation},
				},
				{
					Name:           "test-hook3",
					Events:         []release.HookEvent{release.HookPreInstall, release.HookPreUpgrade},
					Weight:         5,
					DeletePolicies: []release.HookDeletePolicy{release.HookBeforeHookCreation, release.HookSucceeded},
				},
				{
					Name:           "test-hook4",
					Events:         []release.HookEvent{release.HookPreInstall},
					Weight:         -4,
					DeletePolicies: []release.HookDeletePolicy{release.HookFailed},
				},
				{
					Name:           "test-hook5",
					Events:         []release.HookEvent{release.HookPreInstall},
					Weight:         0,
					DeletePolicies: []release.HookDeletePolicy{release.HookSucceeded},
				},
			},
			// Descending order with helm hook name and helm hook weight
			// [{name: "test-hook3", weight: "5"}, {name: "test-hook2", weight: "1"}, {name: "test-hook1", weight: "1"}, {name: "test-hook5", weight: "0"}, {name: "test-hook4", wegiht: "-4"}]
			// The first one is the last one to be executed and starts with readiness group -1 for pre hooks
			expected: map[hookName]readinessGroup{
				"test-hook":  "-2",
				"test-hook2": "-2",
				"test-hook3": "-1",
				"test-hook4": "-4",
				"test-hook5": "-3",
			},
		},
		{
			name: "test readiness group annotations for post hooks",
			hooks: []*release.Hook{
				{
					Name:           "test-hook",
					Events:         []release.HookEvent{release.HookPostInstall},
					Weight:         1,
					DeletePolicies: []release.HookDeletePolicy{release.HookBeforeHookCreation},
				},
				{
					Name:           "test-hook2",
					Events:         []release.HookEvent{release.HookPostUpgrade, release.HookPostRollback},
					Weight:         1,
					DeletePolicies: []release.HookDeletePolicy{release.HookBeforeHookCreation},
				},
				{
					Name:           "test-hook3",
					Events:         []release.HookEvent{release.HookPostInstall, release.HookPostUpgrade},
					Weight:         5,
					DeletePolicies: []release.HookDeletePolicy{release.HookBeforeHookCreation, release.HookSucceeded},
				},
				{
					Name:           "test-hook4",
					Events:         []release.HookEvent{release.HookPostInstall},
					Weight:         -4,
					DeletePolicies: []release.HookDeletePolicy{release.HookFailed},
				},
				{
					Name:           "test-hook5",
					Events:         []release.HookEvent{release.HookPostInstall},
					Weight:         0,
					DeletePolicies: []release.HookDeletePolicy{release.HookSucceeded},
				},
			},
			// Ascending order with helm hook name and helm hook weight
			// [{name: "test-hook4", wegiht: "-4"}, {name: "test-hook5", weight: "0"}, {name: "test-hook1", weight: "1"}, {name: "test-hook2", weight: "1"}, {name: "test-hook3", weight: "5"}]
			// The first one is the first one to be executed and starts with readiness group 1 for post hooks
			expected: map[hookName]readinessGroup{
				"test-hook":  "3",
				"test-hook2": "3",
				"test-hook3": "4",
				"test-hook4": "1",
				"test-hook5": "2",
			},
		},
		{
			name: "test readiness group annotations for chart with both pre hooks and post hooks",
			hooks: []*release.Hook{
				{
					Name:           "test-hook",
					Events:         []release.HookEvent{release.HookPreInstall},
					Weight:         1,
					DeletePolicies: []release.HookDeletePolicy{release.HookBeforeHookCreation},
				},
				{
					Name:           "test-hook2",
					Events:         []release.HookEvent{release.HookPostUpgrade, release.HookPostRollback},
					Weight:         1,
					DeletePolicies: []release.HookDeletePolicy{release.HookBeforeHookCreation},
				},
				{
					Name:           "test-hook3",
					Events:         []release.HookEvent{release.HookPostInstall, release.HookPostUpgrade},
					Weight:         5,
					DeletePolicies: []release.HookDeletePolicy{release.HookBeforeHookCreation, release.HookSucceeded},
				},
				{
					Name:           "test-hook4",
					Events:         []release.HookEvent{release.HookPreUpgrade},
					Weight:         -4,
					DeletePolicies: []release.HookDeletePolicy{release.HookFailed},
				},
				{
					Name:           "test-hook5",
					Events:         []release.HookEvent{release.HookPostInstall},
					Weight:         0,
					DeletePolicies: []release.HookDeletePolicy{release.HookSucceeded},
				},
			},
			expected: map[hookName]readinessGroup{
				"test-hook":  "-1",
				"test-hook2": "2",
				"test-hook3": "3",
				"test-hook4": "-2",
				"test-hook5": "1",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			for _, h := range tc.hooks {
				setConfigMapManifest(t, h)
			}

			err := setReadinessToHooks(tc.hooks)
			assert.NoError(t, err)

			// Validate readiness group annotations in hooks
			for _, h := range tc.hooks {
				un := &unstructured.Unstructured{}
				err := yaml.Unmarshal([]byte(h.Manifest), &un.Object)
				assert.NoError(t, err)

				anno := un.GetAnnotations()
				assert.NotNil(t, anno)
				assert.Equal(t, string(tc.expected[hookName(un.GetName())]), anno[readinessKey])
			}
		})
	}
}

func setConfigMapManifest(t *testing.T, hook *release.Hook) {
	manifest := fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  annotations:
    helm.sh/hook: %s
    helm.sh/hook-delete-policy: %s
    helm.sh/hook-weight: "%s"`,
		hook.Name, joinWithComma(hook.Events), joinWithComma(hook.DeletePolicies), strconv.Itoa(hook.Weight))

	hook.Manifest = manifest
}

func joinWithComma[T release.HookEvent | release.HookDeletePolicy](arr []T) string {
	res := []string{}
	for _, v := range arr {
		res = append(res, string(v))
	}
	return strings.Join(res, ",")
}
