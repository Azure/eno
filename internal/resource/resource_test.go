package resource

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
)

var newResourceTests = []struct {
	Name     string
	Manifest string
	Assert   func(*testing.T, *Snapshot)
}{
	{
		Name: "configmap",
		Manifest: `{
			"apiVersion": "v1",
			"kind": "ConfigMap",
			"metadata": {
				"name": "foo",
				"annotations": {
					"foo": "bar",
					"eno.azure.io/reconcile-interval": "10s",
					"eno.azure.io/readiness-group": "250",
					"eno.azure.io/readiness": "true",
					"eno.azure.io/readiness-test": "false",
					"eno.azure.io/replace": "true",
					"eno.azure.io/disable-updates": "true",
					"eno.azure.io/overrides": "[{\"path\":\".self.foo\"}, {\"path\":\".self.bar\"}]"
				}
			}
		}`,
		Assert: func(t *testing.T, r *Snapshot) {
			assert.Equal(t, schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, r.GVK)
			assert.Len(t, r.ReadinessChecks, 2)
			assert.Equal(t, time.Second*10, r.ReconcileInterval.Duration)
			assert.Equal(t, Ref{
				Name:      "foo",
				Namespace: "",
				Group:     "",
				Kind:      "ConfigMap",
			}, r.Ref)
			assert.True(t, r.DisableUpdates)
			assert.True(t, r.Replace)
			assert.Equal(t, int(250), r.readinessGroup)
			assert.Len(t, r.overrides, 2)
		},
	},
	{
		Name: "reconcile interval override",
		Manifest: `{
			"apiVersion": "apps/v1",
			"kind": "Deployment",
			"metadata": {
				"name": "foo",
				"namespace": "bar",
				"annotations": {
					"eno.azure.io/reconcile-interval": "10s",
					"eno.azure.io/overrides": "[{\"path\":\".self.metadata.annotations[\\\"eno.azure.io/reconcile-interval\\\"]\", \"value\":\"20s\"}]"
				}
			}
		}`,
		Assert: func(t *testing.T, r *Snapshot) {
			assert.Equal(t, 20*time.Second, r.ReconcileInterval.Duration)
		},
	},
	{
		Name: "zero-readiness-group",
		Manifest: `{
			"apiVersion": "v1",
			"kind": "ConfigMap",
			"metadata": {
				"name": "foo",
				"annotations": {
					"eno.azure.io/readiness-group": "0"
				}
			}
		}`,
		Assert: func(t *testing.T, r *Snapshot) {
			assert.Equal(t, int(0), r.readinessGroup)
			assert.False(t, r.DisableUpdates)
			assert.False(t, r.Replace)
		},
	},
	{
		Name: "negative-readiness-group",
		Manifest: `{
			"apiVersion": "v1",
			"kind": "ConfigMap",
			"metadata": {
				"name": "foo",
				"annotations": {
					"eno.azure.io/readiness-group": "-10"
				}
			}
		}`,
		Assert: func(t *testing.T, r *Snapshot) {
			assert.Equal(t, int(-10), r.readinessGroup)
		},
	},
	{
		Name: "deployment",
		Manifest: `{
			"apiVersion": "apps/v1",
			"kind": "Deployment",
			"metadata": {
				"name": "foo",
				"namespace": "bar"
			}
		}`,
		Assert: func(t *testing.T, r *Snapshot) {
			assert.Equal(t, schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, r.GVK)
			assert.Len(t, r.ReadinessChecks, 0)
			assert.Nil(t, r.ReconcileInterval)
			assert.Equal(t, Ref{
				Name:      "foo",
				Namespace: "bar",
				Group:     "apps",
				Kind:      "Deployment",
			}, r.Ref)
		},
	},
	{
		Name: "patch",
		Manifest: `{
			"apiVersion": "eno.azure.io/v1",
			"kind": "Patch",
			"metadata": {
				"name": "foo",
				"namespace": "bar"
			},
			"patch": {
				"apiVersion": "v1",
				"kind": "ConfigMap",
				"ops": [
					{ "op": "add", "path": "/data/foo", "value": "bar" }
				]
			}
		}`,
		Assert: func(t *testing.T, r *Snapshot) {
			assert.Equal(t, schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, r.GVK)
			assert.False(t, r.patchSetsDeletionTimestamp())
		},
	},
	{
		Name: "deletionPatch",
		Manifest: `{
			"apiVersion": "eno.azure.io/v1",
			"kind": "Patch",
			"metadata": {
				"name": "foo",
				"namespace": "bar"
			},
			"patch": {
				"apiVersion": "v1",
				"kind": "ConfigMap",
				"ops": [
					{"op": "add", "path": "/metadata/deletionTimestamp", "value": "anything"}
				]
			}
		}`,
		Assert: func(t *testing.T, r *Snapshot) {
			assert.Equal(t, schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, r.GVK)
			assert.True(t, r.patchSetsDeletionTimestamp())
		},
	},
	{
		Name: "deletionPatchEmptyStr",
		Manifest: `{
			"apiVersion": "eno.azure.io/v1",
			"kind": "Patch",
			"metadata": {
				"name": "foo",
				"namespace": "bar"
			},
			"patch": {
				"apiVersion": "v1",
				"kind": "ConfigMap",
				"ops": [
					{"op": "add", "path": "/metadata/deletionTimestamp", "value": ""}
				]
			}
		}`,
		Assert: func(t *testing.T, r *Snapshot) {
			assert.Equal(t, schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, r.GVK)
			assert.False(t, r.patchSetsDeletionTimestamp())
		},
	},
	{
		Name: "crd",
		Manifest: `{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind": "CustomResourceDefinition",
			"metadata": {
				"name": "foo"
			},
			"spec": {
				"group": "test-group",
				"names": {
					"kind": "TestKind"
				}
			}
		}`,
		Assert: func(t *testing.T, r *Snapshot) {
			assert.Equal(t, schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}, r.GVK)
			assert.Equal(t, &schema.GroupKind{Group: "test-group", Kind: "TestKind"}, r.DefinedGroupKind)
		},
	},
	{
		Name: "empty-crd",
		Manifest: `{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind": "CustomResourceDefinition",
			"metadata": {
				"name": "foo"
			}
		}`,
		Assert: func(t *testing.T, r *Snapshot) {
			assert.Equal(t, schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}, r.GVK)
			assert.Equal(t, &schema.GroupKind{Group: "", Kind: ""}, r.DefinedGroupKind)
		},
	},
	{
		Name: "extra-metadata",
		Manifest: `{
			"apiVersion": "v1",
			"kind": "ConfigMap",
			"metadata": {
				"name": "foo",
				"labels": {
					"test-label": "should not be pruned",
					"eno.azure.io/extra-label": "should be pruned"
				},
				"annotations": {
					"test-annotation": "should not be pruned",
					"eno.azure.io/extra-annotation": "should be pruned",
					"eno.azure.io/reconcile-interval": "10s"
				}
			}
		}`,
		Assert: func(t *testing.T, r *Snapshot) {
			assert.Equal(t, time.Second*10, r.ReconcileInterval.Duration)
			assert.Equal(t, &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":        "foo",
						"annotations": map[string]any{"test-annotation": "should not be pruned"},
						"labels":      map[string]any{"test-label": "should not be pruned"},
					},
				},
			}, r.parsed)
		},
	},
	{
		Name: "empty-metadata",
		Manifest: `{
			"apiVersion": "v1",
			"kind": "ConfigMap",
			"metadata": {
				"name": "foo",
				"labels": {
					"eno.azure.io/extra-label": "should be pruned"
				},
				"annotations": {
					"eno.azure.io/extra-annotation": "should be pruned"
				}
			}
		}`,
		Assert: func(t *testing.T, r *Snapshot) {
			assert.Equal(t, &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name": "foo",
					},
				},
			}, r.parsed)
		},
	},
	{
		Name: "invalid-override-json",
		Manifest: `{
			"apiVersion": "v1",
			"kind": "ConfigMap",
			"metadata": {
				"name": "foo",
				"annotations": {
					"eno.azure.io/overrides": "not json"
				}
			}
		}`,
		Assert: func(t *testing.T, r *Snapshot) {
			assert.Len(t, r.overrides, 0)
		},
	},
	{
		Name: "labels",
		Manifest: `{
			"apiVersion": "v1",
			"kind": "ConfigMap",
			"metadata": {
				"name": "foo",
				"labels": {
					"test-label": "label-value",
					"eno.azure.io/extra-label": "should be pruned from resource"
				}
			}
		}`,
		Assert: func(t *testing.T, r *Snapshot) {
			assert.Equal(t, &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name": "foo",
						"labels": map[string]any{
							"test-label": "label-value",
						},
					},
				},
			}, r.parsed)

			assert.Equal(t, map[string]string{
				"test-label":               "label-value",
				"eno.azure.io/extra-label": "should be pruned from resource",
			}, r.Labels)
		},
	},
}

func TestNewResource(t *testing.T) {
	ctx := context.Background()
	for _, tc := range newResourceTests {
		t.Run(tc.Name, func(t *testing.T) {
			r, err := NewResource(ctx, &apiv1.ResourceSlice{
				Spec: apiv1.ResourceSliceSpec{
					Resources: []apiv1.Manifest{{Manifest: tc.Manifest}},
				},
			}, 0)
			require.NoError(t, err)

			rs, err := r.Snapshot(t.Context(), &apiv1.Composition{}, nil)
			require.NoError(t, err)
			tc.Assert(t, rs)

			noOverrides := r.UnstructuredWithoutOverrides()
			for key := range noOverrides.GetAnnotations() {
				if strings.HasPrefix(key, "eno.azure.io/") {
					t.Errorf("expected no overrides in unstructured, but found %s", key)
				}
			}
		})
	}
}

func TestResourceOrdering(t *testing.T) {
	resources := []*Resource{
		{manifestHash: []byte("a")},
		{},
		{manifestHash: []byte("b")},
		{},
		{manifestHash: []byte("c")},
	}
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].Less(resources[j])
	})

	assert.Equal(t, []*Resource{
		{},
		{},
		{manifestHash: []byte("a")},
		{manifestHash: []byte("b")},
		{manifestHash: []byte("c")},
	}, resources)
}

func TestEnsureManagementOfPrunedFields(t *testing.T) {
	ctx := context.Background()

	// Helper to create a resource with the given manifest
	createResource := func(manifest string) *Resource {
		r, err := NewResource(ctx, &apiv1.ResourceSlice{
			Spec: apiv1.ResourceSliceSpec{
				Resources: []apiv1.Manifest{{Manifest: manifest}},
			},
		}, 0)
		require.NoError(t, err)
		return r
	}

	// Helper to create a snapshot from a resource
	createSnapshot := func(resource *Resource, annotations map[string]string) *Snapshot {
		snap, err := resource.Snapshot(ctx, &apiv1.Composition{}, nil)
		require.NoError(t, err)

		// Apply any additional annotations to the snapshot
		if annotations != nil {
			parsed := snap.Unstructured()
			existingAnnotations := parsed.GetAnnotations()
			if existingAnnotations == nil {
				existingAnnotations = make(map[string]string)
			}
			for k, v := range annotations {
				existingAnnotations[k] = v
			}
			parsed.SetAnnotations(existingAnnotations)
			snap.parsed = parsed
		}

		return snap
	}

	t.Run("returns false when current is nil", func(t *testing.T) {
		prev := createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value"}}`)
		next := createSnapshot(prev, nil)

		result := EnsureManagementOfPrunedFields(ctx, prev, next, nil)
		assert.False(t, result)
	})

	t.Run("returns false when prev is nil", func(t *testing.T) {
		next := createSnapshot(createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}}`), nil)
		current := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]any{"name": "test"},
			},
		}

		result := EnsureManagementOfPrunedFields(ctx, nil, next, current)
		assert.False(t, result)
	})

	t.Run("returns false when replace is true", func(t *testing.T) {
		prev := createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value"}}`)
		next := createSnapshot(prev, map[string]string{"eno.azure.io/replace": "true"})
		current := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]any{"name": "test"},
				"data":       map[string]any{"key": "value"},
			},
		}

		result := EnsureManagementOfPrunedFields(ctx, prev, next, current)
		assert.False(t, result)
	})

	t.Run("returns false when no fields are pruned", func(t *testing.T) {
		prev := createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value"}}`)
		next := createSnapshot(prev, nil)
		current := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]any{"name": "test"},
				"data":       map[string]any{"key": "value"},
			},
		}

		result := EnsureManagementOfPrunedFields(ctx, prev, next, current)
		assert.False(t, result)
	})

	t.Run("returns false when pruned fields don't exist in current", func(t *testing.T) {
		prev := createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value", "removed": "data"}}`)
		next := createSnapshot(createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value"}}`), nil)
		current := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]any{"name": "test"},
				"data":       map[string]any{"key": "value"},
			},
		}

		result := EnsureManagementOfPrunedFields(ctx, prev, next, current)
		assert.False(t, result)
	})

	t.Run("adds managed fields entry when none exists", func(t *testing.T) {
		prev := createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value", "removed": "data"}}`)
		next := createSnapshot(createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value"}}`), nil)
		current := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]any{"name": "test"},
				"data":       map[string]any{"key": "value", "removed": "data"},
			},
		}

		result := EnsureManagementOfPrunedFields(ctx, prev, next, current)
		assert.True(t, result)

		managedFields := current.GetManagedFields()
		assert.Len(t, managedFields, 1)
		assert.Equal(t, "eno", managedFields[0].Manager)
		assert.Equal(t, "v1", managedFields[0].APIVersion)
		assert.NotNil(t, managedFields[0].FieldsV1)
		assert.NotEmpty(t, managedFields[0].FieldsV1.Raw)
	})

	t.Run("updates existing managed fields entry", func(t *testing.T) {
		prev := createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value", "removed": "data"}}`)
		next := createSnapshot(createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value"}}`), nil)
		current := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]any{"name": "test"},
				"data":       map[string]any{"key": "value", "removed": "data"},
			},
		}

		// Add existing managed fields entry
		current.SetManagedFields([]metav1.ManagedFieldsEntry{
			{
				Manager:    "eno",
				Operation:  metav1.ManagedFieldsOperationApply,
				APIVersion: "v1",
				FieldsType: "FieldsV1",
				Time:       ptr.To(metav1.Now()),
				FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:key":{}}}`)},
			},
		})

		originalFieldsRaw := current.GetManagedFields()[0].FieldsV1.Raw

		result := EnsureManagementOfPrunedFields(ctx, prev, next, current)
		assert.True(t, result)

		managedFields := current.GetManagedFields()
		assert.Len(t, managedFields, 1)
		assert.Equal(t, "eno", managedFields[0].Manager)
		assert.NotEqual(t, originalFieldsRaw, managedFields[0].FieldsV1.Raw)
	})

	t.Run("ignores fields already managed by eno", func(t *testing.T) {
		prev := createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value", "removed": "data"}}`)
		next := createSnapshot(createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value"}}`), nil)
		current := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]any{"name": "test"},
				"data":       map[string]any{"key": "value", "removed": "data"},
			},
		}

		// Set managed fields to include the field that's being removed
		current.SetManagedFields([]metav1.ManagedFieldsEntry{
			{
				Manager:    "eno",
				Operation:  metav1.ManagedFieldsOperationApply,
				APIVersion: "v1",
				FieldsType: "FieldsV1",
				Time:       ptr.To(metav1.Now()),
				FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:key":{},"f:removed":{}}}`)},
			},
		})

		result := EnsureManagementOfPrunedFields(ctx, prev, next, current)
		assert.False(t, result)
	})

	t.Run("handles complex nested field pruning", func(t *testing.T) {
		prev := createResource(`{
			"apiVersion": "v1",
			"kind": "ConfigMap",
			"metadata": {"name": "test"},
			"data": {
				"config.yaml": "key: value\nremoved: data",
				"other": "data"
			}
		}`)
		next := createSnapshot(createResource(`{
			"apiVersion": "v1",
			"kind": "ConfigMap",
			"metadata": {"name": "test"},
			"data": {
				"config.yaml": "key: value"
			}
		}`), nil)
		current := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]any{"name": "test"},
				"data": map[string]any{
					"config.yaml": "key: value\nremoved: data",
					"other":       "data",
				},
			},
		}

		result := EnsureManagementOfPrunedFields(ctx, prev, next, current)
		assert.True(t, result)

		managedFields := current.GetManagedFields()
		assert.Len(t, managedFields, 1)
		assert.Equal(t, "eno", managedFields[0].Manager)
	})

	t.Run("removes pruned fields from other manager entries", func(t *testing.T) {
		prev := createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value", "removed": "data"}}`)
		next := createSnapshot(createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value"}}`), nil)
		current := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]any{"name": "test"},
				"data":       map[string]any{"key": "value", "removed": "data"},
			},
		}

		// Set up managed fields with multiple managers
		current.SetManagedFields([]metav1.ManagedFieldsEntry{
			{
				Manager:    "kubectl",
				Operation:  metav1.ManagedFieldsOperationApply,
				APIVersion: "v1",
				FieldsType: "FieldsV1",
				Time:       ptr.To(metav1.Now()),
				FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:key":{},"f:removed":{}}}`)},
			},
			{
				Manager:    "helm",
				Operation:  metav1.ManagedFieldsOperationApply,
				APIVersion: "v1",
				FieldsType: "FieldsV1",
				Time:       ptr.To(metav1.Now()),
				FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:removed":{}}}`)},
			},
		})

		result := EnsureManagementOfPrunedFields(ctx, prev, next, current)
		assert.True(t, result)

		managedFields := current.GetManagedFields()
		assert.Len(t, managedFields, 3) // kubectl, helm, and new eno entry

		// Find the kubectl entry and verify removed field was removed
		var kubectlEntry *metav1.ManagedFieldsEntry
		var helmEntry *metav1.ManagedFieldsEntry
		var enoEntry *metav1.ManagedFieldsEntry
		for i := range managedFields {
			switch managedFields[i].Manager {
			case "kubectl":
				kubectlEntry = &managedFields[i]
			case "helm":
				helmEntry = &managedFields[i]
			case "eno":
				enoEntry = &managedFields[i]
			}
		}

		require.NotNil(t, kubectlEntry)
		require.NotNil(t, helmEntry)
		require.NotNil(t, enoEntry)

		// kubectl should only have "key" field now (removed field should be gone)
		assert.Equal(t, `{"f:data":{"f:key":{}}}`, string(kubectlEntry.FieldsV1.Raw))

		// helm should have no fields now (only had removed field)
		assert.Equal(t, `{}`, string(helmEntry.FieldsV1.Raw))

		// eno should have the removed field
		assert.Contains(t, string(enoEntry.FieldsV1.Raw), "f:removed")
	})

	t.Run("skips non-FieldsV1 managers when removing fields", func(t *testing.T) {
		prev := createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value", "removed": "data"}}`)
		next := createSnapshot(createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value"}}`), nil)
		current := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]any{"name": "test"},
				"data":       map[string]any{"key": "value", "removed": "data"},
			},
		}

		// Set up managed fields with different field types and API versions
		current.SetManagedFields([]metav1.ManagedFieldsEntry{
			{
				Manager:    "kubectl",
				Operation:  metav1.ManagedFieldsOperationApply,
				APIVersion: "v1",
				FieldsType: "FieldsV1",
				Time:       ptr.To(metav1.Now()),
				FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:removed":{}}}`)},
			},
			{
				Manager:    "old-manager",
				Operation:  metav1.ManagedFieldsOperationApply,
				APIVersion: "apps/v1", // different API version
				FieldsType: "FieldsV1",
				Time:       ptr.To(metav1.Now()),
				FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:removed":{}}}`)},
			},
		})

		originalOldManagerFields := string(current.GetManagedFields()[1].FieldsV1.Raw)

		result := EnsureManagementOfPrunedFields(ctx, prev, next, current)
		assert.True(t, result)

		managedFields := current.GetManagedFields()
		assert.Len(t, managedFields, 3) // kubectl, old-manager, and new eno entry

		// kubectl should have been updated (different APIVersion should be ignored)
		assert.Equal(t, `{}`, string(managedFields[0].FieldsV1.Raw))

		// old-manager should be unchanged (different APIVersion)
		assert.Equal(t, originalOldManagerFields, string(managedFields[1].FieldsV1.Raw))
	})

	t.Run("handles malformed managed fields gracefully", func(t *testing.T) {
		prev := createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value", "removed": "data"}}`)
		next := createSnapshot(createResource(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "test"}, "data": {"key": "value"}}`), nil)
		current := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]any{"name": "test"},
				"data":       map[string]any{"key": "value", "removed": "data"},
			},
		}

		// Set up managed fields with one valid and one with malformed JSON
		current.SetManagedFields([]metav1.ManagedFieldsEntry{
			{
				Manager:    "kubectl",
				Operation:  metav1.ManagedFieldsOperationApply,
				APIVersion: "v1",
				FieldsType: "FieldsV1",
				Time:       ptr.To(metav1.Now()),
				FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:removed":{}}}`)},
			},
		})

		result := EnsureManagementOfPrunedFields(ctx, prev, next, current)
		assert.True(t, result)

		// Should have kubectl entry (updated) and new eno entry
		managedFields := current.GetManagedFields()
		assert.Len(t, managedFields, 2)
		
		var kubectlFound, enoFound bool
		for _, entry := range managedFields {
			if entry.Manager == "kubectl" {
				kubectlFound = true
				// kubectl should have its removed field taken away
				assert.Equal(t, `{}`, string(entry.FieldsV1.Raw))
			}
			if entry.Manager == "eno" {
				enoFound = true
				// eno should have the removed field
				assert.Contains(t, string(entry.FieldsV1.Raw), "f:removed")
			}
		}
		assert.True(t, kubectlFound)
		assert.True(t, enoFound)
	})
}
