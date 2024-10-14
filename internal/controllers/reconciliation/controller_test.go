package reconciliation

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/discovery"
	"github.com/Azure/eno/internal/flowcontrol"
	"github.com/Azure/eno/internal/reconstitution"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestMungePatch(t *testing.T) {
	patch, err := mungePatch([]byte(`{"metadata":{"creationTimestamp":"2024-03-05T00:45:27Z"}, "foo":"bar"}`), "test-rv")
	require.NoError(t, err)
	assert.JSONEq(t, `{"metadata":{"resourceVersion":"test-rv"},"foo":"bar"}`, string(patch))
}

func TestMungePatchEmpty(t *testing.T) {
	patch, err := mungePatch([]byte(`{}`), "test-rv")
	require.NoError(t, err)
	assert.Nil(t, patch)
}

func TestMungePatchOnlyCreationTimestamp(t *testing.T) {
	patch, err := mungePatch([]byte(`{"metadata":{"creationTimestamp":"2024-03-05T00:45:27Z"}}`), "test-rv")
	require.NoError(t, err)
	assert.Nil(t, patch)
}

func TestBuildPatchEmpty(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	dc, err := discovery.NewCache(mgr.DownstreamRestConfig, 10)
	require.NoError(t, err)
	c := &Controller{discovery: dc}

	tests := []struct {
		Name          string
		Type          types.PatchType
		Next, Current map[string]any
	}{
		{
			Name: "empty non-strategic",
			Type: types.MergePatchType,
			Current: map[string]any{
				"apiVersion": "test.io/v1",
				"kind":       "anything",
				"metadata":   map[string]any{"name": "foo", "namespace": "default"},
				"spec":       map[string]any{"value": "initial"},
			},
		},
		{
			Name: "empty non-strategic with status",
			Type: types.MergePatchType,
			Current: map[string]any{
				"apiVersion": "test.io/v1",
				"kind":       "anything",
				"metadata":   map[string]any{"name": "foo", "namespace": "default"},
				"spec":       map[string]any{"value": "initial"},
				"status":     map[string]any{"statusValue": "initial"},
			},
		},
		{
			Name: "empty strategic",
			Type: types.StrategicMergePatchType,
			Current: map[string]any{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata":   map[string]any{"name": "foo", "namespace": "default"},
				"spec":       map[string]any{"serviceAccountName": "initial"},
			},
		},
		{
			Name: "status mismatched non-strategic",
			Type: types.MergePatchType,
			Current: map[string]any{
				"apiVersion": "test.io/v1",
				"kind":       "anything",
				"metadata":   map[string]any{"name": "foo", "namespace": "default"},
				"spec":       map[string]any{"value": "initial"},
				"status":     map[string]any{"statusValue": "initial"},
			},
			Next: map[string]any{
				"apiVersion": "test.io/v1",
				"kind":       "anything",
				"metadata":   map[string]any{"name": "foo", "namespace": "default"},
				"spec":       map[string]any{"value": "initial"},
				"status":     map[string]any{"statusValue": "updated"},
			},
		},
		{
			Name: "status mismatched strategic",
			Type: types.StrategicMergePatchType,
			Current: map[string]any{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata":   map[string]any{"name": "foo", "namespace": "default"},
				"status":     map[string]any{"message": "initial"},
			},
			Next: map[string]any{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata":   map[string]any{"name": "foo", "namespace": "default"},
				"status":     map[string]any{"message": "updated"},
			},
		},
		{
			Name: "unordered non-strategic",
			Type: types.MergePatchType,
			Current: map[string]any{
				"apiVersion": "test.io/v1",
				"kind":       "anything",
				"metadata":   map[string]any{"name": "foo", "namespace": "default"},
				"spec":       map[string]any{"one": "first", "two": "second"},
			},
			Next: map[string]any{
				"apiVersion": "test.io/v1",
				"kind":       "anything",
				"metadata":   map[string]any{"name": "foo", "namespace": "default"},
				"spec":       map[string]any{"two": "second", "one": "first"},
			},
		},
		{
			Name: "unordered strategic",
			Type: types.StrategicMergePatchType,
			Current: map[string]any{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata":   map[string]any{"name": "foo", "namespace": "default"},
				"spec":       map[string]any{"serviceAccountName": "initial", "initContainers": []any{}},
			},
			Next: map[string]any{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata":   map[string]any{"name": "foo", "namespace": "default"},
				"spec":       map[string]any{"initContainers": []any{}, "serviceAccountName": "initial"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			if test.Next == nil {
				test.Next = test.Current
			}

			current, prev := mapToResource(t, test.Current)
			_, next := mapToResource(t, test.Next)

			patch, kind, err := c.buildPatch(ctx, prev, next, current)
			require.NoError(t, err)

			patch, err = mungePatch(patch, "random-rv")
			require.NoError(t, err)
			assert.Nil(t, patch)
			assert.Equal(t, test.Type, kind)
		})
	}
}

func mapToResource(t *testing.T, res map[string]any) (*unstructured.Unstructured, *reconstitution.Resource) {
	obj := &unstructured.Unstructured{Object: res}
	js, err := obj.MarshalJSON()
	require.NoError(t, err)

	rr := &reconstitution.Resource{
		Manifest: &apiv1.Manifest{Manifest: string(js)},
		GVK:      obj.GroupVersionKind(),
	}
	return obj, rr
}

func setupTestSubject(t *testing.T, mgr *testutil.Manager) *Controller {
	rswb := flowcontrol.NewResourceSliceWriteBufferForManager(mgr.Manager, time.Millisecond*10, 1)
	cache := reconstitution.NewCache(mgr.GetClient())
	rc, err := New(Options{
		Manager:               mgr.Manager,
		Cache:                 cache,
		WriteBuffer:           rswb,
		Downstream:            mgr.DownstreamRestConfig,
		DiscoveryRPS:          5,
		Timeout:               time.Minute,
		ReadinessPollInterval: time.Hour,
	})
	require.NoError(t, err)

	err = reconstitution.New(mgr.Manager, cache, rc)
	require.NoError(t, err)

	return rc
}
