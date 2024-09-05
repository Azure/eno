package helmshim

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"helm.sh/helm/v3/pkg/release"
	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAddReadinessGroup(t *testing.T) {
	tests := []struct {
		name  string
		hooks []*release.Hook
	}{
		{
			name: "test add readiness group annotations",
			hooks: []*release.Hook{
				{
					Name:           "test-hook",
					Events:         []release.HookEvent{release.HookPreInstall},
					Weight:         1,
					DeletePolicies: []release.HookDeletePolicy{release.HookBeforeHookCreation},
					Manifest: testConfigMapManifest(
						"test-hook", []release.HookEvent{release.HookPreInstall}, []release.HookDeletePolicy{release.HookBeforeHookCreation}, 1,
					),
				},
				{
					Name:           "test-hook2",
					Events:         []release.HookEvent{release.HookPreUpgrade},
					Weight:         1,
					DeletePolicies: []release.HookDeletePolicy{release.HookBeforeHookCreation},
					Manifest: testConfigMapManifest(
						"test-hook2", []release.HookEvent{release.HookPreUpgrade}, []release.HookDeletePolicy{release.HookBeforeHookCreation}, 1,
					),
				},
				{
					Name:           "test-hook3",
					Events:         []release.HookEvent{release.HookPreInstall, release.HookPreUpgrade},
					Weight:         2,
					DeletePolicies: []release.HookDeletePolicy{release.HookBeforeHookCreation},
					Manifest: testConfigMapManifest(
						"test-hook3", []release.HookEvent{release.HookPreInstall, release.HookPreUpgrade},
						[]release.HookDeletePolicy{release.HookBeforeHookCreation}, 2,
					),
				},
				{
					Name:           "test-hook4",
					Events:         []release.HookEvent{release.HookPostInstall},
					Weight:         4,
					DeletePolicies: []release.HookDeletePolicy{release.HookBeforeHookCreation},
					Manifest: testConfigMapManifest(
						"test-hook4", []release.HookEvent{release.HookPostInstall}, []release.HookDeletePolicy{release.HookBeforeHookCreation}, 4,
					),
				},
				{
					Name:           "test-hook5",
					Events:         []release.HookEvent{release.HookPostUpgrade, release.HookPostInstall},
					Weight:         1,
					DeletePolicies: []release.HookDeletePolicy{release.HookBeforeHookCreation},
					Manifest: testConfigMapManifest(
						"test-hook5", []release.HookEvent{release.HookPostInstall, release.HookPostUpgrade},
						[]release.HookDeletePolicy{release.HookBeforeHookCreation}, 1,
					),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hs, err := AddReadinessGroup(tt.hooks)
			assert.NoError(t, err)
			assert.NotNil(t, hs)
			t.Log(hs)
		})
	}
}

func testConfigMapManifest(name string, events []release.HookEvent, policies []release.HookDeletePolicy, weight int) string {
	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	cm.Annotations = map[string]string{}
	eventStr := ""
	for _, e := range events {
		eventStr += string(e) + ","
	}
	deletPolicies := ""
	for _, e := range events {
		deletPolicies += string(e) + ","
	}
	cm.Annotations[release.HookAnnotation] = eventStr
	cm.Annotations[release.HookWeightAnnotation] = strconv.Itoa(weight)
	cm.Annotations[release.HookDeleteAnnotation] = deletPolicies

	y, _ := yaml.Marshal(cm)
	manifest := string(y)

	return manifest
}
