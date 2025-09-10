package resource

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var newResourceTests = []struct {
	Name        string
	Manifest    string
	Composition apiv1.Composition
	Assert      func(*testing.T, *Snapshot)
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
					"eno.azure.io/disable-reconciliation": "true",
					"eno.azure.io/disable-updates": "true",
					"eno.azure.io/deletion-strategy": "orphan",
					"eno.azure.io/ordered-deletion": "true",
					"eno.azure.io/overrides": "[{\"path\":\".self.foo\"}, {\"path\":\".self.bar\", \"condition\": \"false\"}]"
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
			assert.True(t, r.Disable)
			assert.True(t, r.DisableUpdates)
			assert.True(t, r.Replace)
			assert.True(t, r.Orphan)
			assert.True(t, r.OrderedDeletion)
			assert.Equal(t, int(250), r.readinessGroup)
			assert.Len(t, r.overrides, 2)
			assert.Equal(t, ".self.foo=Active, .self.bar=Inactive", r.OverrideStatus())
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
		Name: "replace override",
		Manifest: `{
			"apiVersion": "apps/v1",
			"kind": "Deployment",
			"metadata": {
				"name": "foo",
				"namespace": "bar",
				"annotations": {
					"eno.azure.io/overrides": "[{\"path\":\"self.metadata.annotations[\\\"eno.azure.io/replace\\\"]\", \"value\":\"true\"}]"
				}
			}
		}`,
		Assert: func(t *testing.T, r *Snapshot) {
			assert.True(t, r.Replace)
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
		Name: "composition-precedence-positive",
		Manifest: `{
			"apiVersion": "v1",
			"kind": "ConfigMap",
			"metadata": {
				"name": "foo",
				"annotations": {
				  "eno.azure.io/deletion-strategy": ""
				}
			}
		}`,
		Composition: apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					"eno.azure.io/deletion-strategy": "orphan",
				},
			},
		},
		Assert: func(t *testing.T, r *Snapshot) {
			assert.False(t, r.Orphan)
		},
	},
	{
		Name: "composition-precedence-negative",
		Manifest: `{
			"apiVersion": "v1",
			"kind": "ConfigMap",
			"metadata": {
				"name": "foo",
				"annotations": {}
			}
		}`,
		Composition: apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					"eno.azure.io/deletion-strategy": "orphan",
				},
			},
		},
		Assert: func(t *testing.T, r *Snapshot) {
			assert.True(t, r.Orphan)
		},
	},
	{
		Name: "strict-deletion",
		Manifest: `{
			"apiVersion": "v1",
			"kind": "ConfigMap",
			"metadata": {
				"name": "foo",
				"annotations": {
				  "eno.azure.io/deletion-strategy": "strict"
				}
			}
		}`,
		Assert: func(t *testing.T, r *Snapshot) {
			assert.False(t, r.Orphan)
			assert.True(t, r.StrictDeletion)
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
			r, err := FromSlice(ctx, &apiv1.ResourceSlice{
				Spec: apiv1.ResourceSliceSpec{
					Resources: []apiv1.Manifest{{Manifest: tc.Manifest}},
				},
			}, 0)
			require.NoError(t, err)

			rs, err := r.Snapshot(t.Context(), &tc.Composition, nil)
			require.NoError(t, err)
			tc.Assert(t, rs)
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

func TestSnapshotPatch(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		Name     string
		Manifest string
		Assert   func(*testing.T, *Snapshot)
	}{
		{
			Name: "non-patch resource",
			Manifest: `{
				"apiVersion": "v1",
				"kind": "ConfigMap",
				"metadata": {
					"name": "foo",
					"namespace": "bar"
				},
				"data": {
					"key": "value"
				}
			}`,
			Assert: func(t *testing.T, s *Snapshot) {
				patch, isPatch, err := s.Patch()
				require.NoError(t, err)
				assert.False(t, isPatch)
				assert.Nil(t, patch)
			},
		},
		{
			Name: "patch with operations",
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
						{ "op": "add", "path": "/data/foo", "value": "bar" },
						{ "op": "replace", "path": "/data/existing", "value": "new" }
					]
				}
			}`,
			Assert: func(t *testing.T, s *Snapshot) {
				patch, isPatch, err := s.Patch()
				require.NoError(t, err)
				assert.True(t, isPatch)
				assert.NotNil(t, patch)
			},
		},
		{
			Name: "patch with empty ops",
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
					"ops": []
				}
			}`,
			Assert: func(t *testing.T, s *Snapshot) {
				patch, isPatch, err := s.Patch()
				require.NoError(t, err)
				assert.True(t, isPatch)
				assert.Nil(t, patch)
			},
		},
		{
			Name: "patch without ops field",
			Manifest: `{
				"apiVersion": "eno.azure.io/v1",
				"kind": "Patch",
				"metadata": {
					"name": "foo",
					"namespace": "bar"
				},
				"patch": {
					"apiVersion": "v1",
					"kind": "ConfigMap"
				}
			}`,
			Assert: func(t *testing.T, s *Snapshot) {
				patch, isPatch, err := s.Patch()
				require.NoError(t, err)
				assert.True(t, isPatch)
				assert.Nil(t, patch)
			},
		},
		{
			Name: "patch with single operation",
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
						{ "op": "remove", "path": "/data/unwanted" }
					]
				}
			}`,
			Assert: func(t *testing.T, s *Snapshot) {
				patch, isPatch, err := s.Patch()
				require.NoError(t, err)
				assert.True(t, isPatch)
				assert.NotNil(t, patch)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			r, err := FromSlice(ctx, &apiv1.ResourceSlice{
				Spec: apiv1.ResourceSliceSpec{
					Resources: []apiv1.Manifest{{Manifest: tc.Manifest}},
				},
			}, 0)
			require.NoError(t, err)

			snapshot, err := r.Snapshot(ctx, &apiv1.Composition{}, nil)
			require.NoError(t, err)
			tc.Assert(t, snapshot)
		})
	}
}

func TestComparisons(t *testing.T) {
	env := &envtest.Environment{}
	t.Cleanup(func() {
		err := env.Stop()
		if err != nil {
			panic(err)
		}
	})

	var cfg *rest.Config
	var err error
	for i := 0; i < 2; i++ {
		cfg, err = env.Start()
		if err != nil {
			t.Logf("failed to start test environment: %s", err)
			continue
		}
		break
	}
	require.NoError(t, err)

	cli, err := client.New(cfg, client.Options{})
	require.NoError(t, err)

	tests := []struct {
		Resource  map[string]any
		Mutations []map[string]any
	}{
		{
			Resource: map[string]any{
				"apiVersion": "v1",
				"kind":       "Service",
				"metadata": map[string]any{
					"name":      "test",
					"namespace": "default",
				},
				"spec": map[string]any{
					"ports": []any{
						map[string]any{
							"name":       "http",
							"port":       int64(80),
							"targetPort": int64(8080),
						},
					},
					"selector": map[string]any{
						"app": "simple",
					},
				},
			},
			Mutations: []map[string]any{
				{"op": "replace", "path": "/spec/ports/0/name", "value": "modified"},
			},
		},
		{
			Resource: map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]any{
					"name":      "test",
					"namespace": "default",
				},
				"spec": map[string]any{
					"selector": map[string]any{
						"matchLabels": map[string]any{
							"app": "simple",
						},
					},
					"template": map[string]any{
						"metadata": map[string]any{
							"labels": map[string]any{
								"app": "simple",
							},
						},
						"spec": map[string]any{
							"containers": []any{
								map[string]any{
									"name":  "simple",
									"image": "nginx:latest",
									"ports": []any{
										map[string]any{
											"name":          "http",
											"containerPort": int64(80),
										},
									},
									"resources": map[string]any{
										"limits": map[string]any{
											"cpu":    int64(1),
											"memory": "2048Ki",
										},
										"requests": map[string]any{
											"cpu":    "100m",
											"memory": "1Mi",
										},
									},
								},
							},
						},
					},
				},
			},
			Mutations: []map[string]any{
				{"op": "replace", "path": "/spec/template/spec/containers/0/name", "value": "updated"},
			},
		},
		{
			Resource: map[string]any{
				"apiVersion": "policy/v1",
				"kind":       "PodDisruptionBudget", // only PDBs set patch strategy == replace
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
				},
				"spec": map[string]any{
					"maxUnavailable": int64(1),
					"selector": map[string]any{
						"matchLabels": map[string]any{"app": "foobar"},
					},
				},
			},
			Mutations: []map[string]any{
				{"op": "replace", "path": "/spec/maxUnavailable", "value": int64(2)},
			},
		},
	}

	ctx := context.Background()
	assert.True(t, Compare(nil, nil))
	for _, tc := range tests {
		u := &unstructured.Unstructured{Object: tc.Resource}
		name := fmt.Sprintf("%s/%s", u.GetKind(), u.GetName())
		t.Run(name, func(t *testing.T) {
			// Create the resource
			initial := u.DeepCopy()
			require.NoError(t, cli.Patch(ctx, initial, client.Apply, client.ForceOwnership, client.FieldOwner("eno")))
			assert.False(t, Compare(initial, nil))
			assert.False(t, Compare(nil, initial))
			assert.True(t, Compare(initial, initial))

			// Dry-run'ing a server-side apply should not change anything
			dryRun := u.DeepCopy()
			require.NoError(t, cli.Patch(ctx, dryRun, client.Apply, client.DryRunAll, client.ForceOwnership, client.FieldOwner("eno")))
			assert.True(t, Compare(initial, dryRun))
			assert.True(t, Compare(dryRun, initial))
			assert.True(t, Compare(dryRun, dryRun))

			// Removing the managed fields should always cause comparison to fail
			dryRun.SetManagedFields(nil)
			assert.False(t, Compare(initial, dryRun))
			assert.False(t, Compare(dryRun, initial))
			assert.True(t, Compare(dryRun, dryRun))

			// Applying the mutation should cause comparison to fail
			patch, err := json.Marshal(&tc.Mutations)
			require.NoError(t, err)
			require.NoError(t, cli.Patch(ctx, u.DeepCopy(), client.RawPatch(types.JSONPatchType, patch)))

			mutated := u.DeepCopy()
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(mutated), mutated))

			js, _ := mutated.MarshalJSON()
			t.Logf("mutated json: %s", js)

			afterMutation := u.DeepCopy()
			require.NoError(t, cli.Patch(ctx, afterMutation, client.Apply, client.DryRunAll, client.ForceOwnership, client.FieldOwner("eno")))
			assert.False(t, Compare(mutated, afterMutation))
			assert.False(t, Compare(afterMutation, mutated))
		})
	}
}
