package reconstitution

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	"github.com/Azure/eno/internal/testutil"
)

func TestCacheBasics(t *testing.T) {
	ctx := testutil.NewContext(t)

	client := testutil.NewClient(t)
	c := NewCache(client)

	comp, synth, resources, expectedReqs := newCacheTestFixtures(2, 3)
	compRef := NewSynthesisRef(comp)
	t.Run("fill", func(t *testing.T) {
		reqs, err := c.fill(ctx, comp, synth, resources)
		require.NoError(t, err)
		assert.Equal(t, expectedReqs, reqs)
	})

	t.Run("check", func(t *testing.T) {
		// positive
		assert.True(t, c.hasSynthesis(comp, synth))

		// negative
		assert.False(t, c.hasSynthesis(comp, &apiv1.Synthesis{UUID: uuid.NewString()}))

	})

	t.Run("getByIndex", func(t *testing.T) {
		// positive
		res, ok := c.getByIndex(&sliceIndex{
			Index:     1,
			SliceName: resources[0].Name,
			Namespace: resources[0].Namespace,
		})
		assert.True(t, ok)
		assert.Equal(t, "slice-0-resource-1", res.Ref.Name)

		// negative
		_, ok = c.getByIndex(&sliceIndex{
			Index:     1000,
			SliceName: resources[0].Name,
			Namespace: resources[0].Namespace,
		})
		assert.False(t, ok)

		_, ok = c.getByIndex(&sliceIndex{
			Index:     1,
			SliceName: "nope",
			Namespace: resources[0].Namespace,
		})
		assert.False(t, ok)
	})

	t.Run("get", func(t *testing.T) {
		// positive
		resource, exists := c.Get(ctx, compRef, &expectedReqs[0].Resource)
		require.True(t, exists)
		assert.NotEmpty(t, resource.Manifest)
		assert.Equal(t, "ConfigMap", resource.GVK.Kind)
		assert.Equal(t, "slice-0-resource-0", resource.Ref.Name)
		assert.False(t, resource.Manifest.Deleted)
		assert.Len(t, resource.ReadinessChecks, 2)
		assert.Equal(t, "default", resource.ReadinessChecks[0].Name)
		assert.Equal(t, "test-check", resource.ReadinessChecks[1].Name)

		// negative
		copy := *compRef
		copy.UUID = uuid.NewString()
		_, exists = c.Get(ctx, &copy, &expectedReqs[0].Resource)
		assert.False(t, exists)
	})

	t.Run("purge", func(t *testing.T) {
		c.purge(types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}, nil)

		// confirm
		_, exists := c.Get(ctx, compRef, &expectedReqs[0].Resource)
		assert.False(t, exists)

		assert.Len(t, c.resources, 0)
	})

	t.Run("getByIndex missing", func(t *testing.T) {
		_, ok := c.getByIndex(&sliceIndex{
			Index:     1,
			SliceName: resources[0].Name,
			Namespace: resources[0].Namespace,
		})
		assert.False(t, ok)
	})
}

func TestCacheCleanup(t *testing.T) {
	ctx := testutil.NewContext(t)

	client := testutil.NewClient(t)
	c := NewCache(client)

	now := metav1.Now()
	comp, synth, resources, expectedReqs := newCacheTestFixtures(2, 3)
	comp.DeletionTimestamp = &now
	for i := range resources {
		resources[i].DeletionTimestamp = &now
	}
	t.Run("fill", func(t *testing.T) {
		reqs, err := c.fill(ctx, comp, synth, resources)
		require.NoError(t, err)
		assert.Equal(t, expectedReqs, reqs)
	})
	compRef := NewSynthesisRef(comp)

	t.Run("get", func(t *testing.T) {
		resource, exists := c.Get(ctx, compRef, &expectedReqs[0].Resource)
		require.True(t, exists)
		assert.NotEmpty(t, resource.Manifest)
		assert.Equal(t, "ConfigMap", resource.GVK.Kind)
		assert.Equal(t, "slice-0-resource-0", resource.Ref.Name)
		assert.False(t, resource.Manifest.Deleted)
	})

	t.Run("partial purge", func(t *testing.T) {
		c.purge(types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}, comp)
		assert.Len(t, c.resources, 1)
		assert.Len(t, c.byIndex, 6)
	})

	t.Run("purge", func(t *testing.T) {
		c.purge(types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}, nil)
		assert.Len(t, c.resources, 0)
		assert.Len(t, c.byIndex, 0)
	})
}

func TestCacheInvalidManifest(t *testing.T) {
	ctx := testutil.NewContext(t)

	client := testutil.NewClient(t)
	c := NewCache(client)

	comp, synth, resources, _ := newCacheTestFixtures(1, 1)
	resources[0].Spec.Resources[0].Manifest = "not valid json"

	_, err := c.fill(ctx, comp, synth, resources)
	require.ErrorContains(t, err, "invalid json:")
}

func TestCacheManifestMissingName(t *testing.T) {
	ctx := testutil.NewContext(t)

	client := testutil.NewClient(t)
	c := NewCache(client)

	comp, synth, resources, _ := newCacheTestFixtures(1, 1)
	resources[0].Spec.Resources[0].Manifest = `{"kind":"ConfigMap"}`

	_, err := c.fill(ctx, comp, synth, resources)
	require.ErrorContains(t, err, "missing name, kind, or apiVersion")
}

func TestCachePartialPurge(t *testing.T) {
	ctx := testutil.NewContext(t)

	client := testutil.NewClient(t)
	c := NewCache(client)

	// Fill our main composition
	comp, synth, resources, _ := newCacheTestFixtures(3, 4)
	_, err := c.fill(ctx, comp, synth, resources)
	require.NoError(t, err)
	originalUUID := synth.UUID
	compNSN := types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}

	// Add another resource to the composition but from a newer synthesis
	_, _, resources, expectedReqs := newCacheTestFixtures(1, 1)
	synth.UUID = uuid.NewString()
	expectedReqs[0].Composition = types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}
	_, err = c.fill(ctx, comp, synth, resources)
	require.NoError(t, err)
	compRef := NewSynthesisRef(comp)

	// Fill another composition - this one shouldn't be purged
	var toBePreserved *Request
	{
		comp, synth, resources, expectedReqs := newCacheTestFixtures(3, 4)
		_, err := c.fill(ctx, comp, synth, resources)
		require.NoError(t, err)
		toBePreserved = expectedReqs[0]
	}

	comp.Status.CurrentSynthesis = synth // only reference the most recent synthesis

	// Purge only a single synthesis
	c.purge(compNSN, comp)

	// The newer resource should still exist
	_, exists := c.Get(ctx, compRef, &expectedReqs[0].Resource)
	assert.True(t, exists)

	// The older resource is not referenced by the composition and should have been removed
	compRef.UUID = originalUUID
	_, exists = c.Get(ctx, compRef, &expectedReqs[0].Resource)
	assert.False(t, exists)

	// Resource of the other composition are unaffected
	_, exists = c.Get(ctx, NewSynthesisRef(comp), &toBePreserved.Resource)
	assert.True(t, exists)

	// The cache should only be internally tracking the remaining synthesis of our test composition
	assert.Len(t, c.synthesisUUIDsByComposition[compNSN], 1)
}

func newCacheTestFixtures(sliceCount, resPerSliceCount int) (*apiv1.Composition, *apiv1.Synthesis, []apiv1.ResourceSlice, []*Request) {
	comp := &apiv1.Composition{}
	comp.Namespace = string(uuid.NewString())
	comp.Name = string(uuid.NewString())
	synth := &apiv1.Synthesis{UUID: uuid.NewString()} // just not 0
	comp.Status.CurrentSynthesis = synth

	resources := make([]apiv1.ResourceSlice, sliceCount)
	requests := []*Request{}
	for i := 0; i < sliceCount; i++ {
		slice := apiv1.ResourceSlice{}
		slice.Name = string(uuid.NewString())
		slice.Namespace = "slice-ns"
		slice.Spec.Resources = make([]apiv1.Manifest, resPerSliceCount)

		for j := 0; j < resPerSliceCount; j++ {
			obj := &corev1.ConfigMap{}
			obj.Name = fmt.Sprintf("slice-%d-resource-%d", i, j)
			obj.Namespace = "resource-ns"
			obj.Kind = "ConfigMap"
			obj.APIVersion = "v1"
			obj.Annotations = map[string]string{
				"eno.azure.io/readiness":            "self.foo > self.bar",
				"eno.azure.io/readiness-group":      fmt.Sprintf("%d", j%2),
				"eno.azure.io/readiness-test-check": "self.bar > self.baz",
			}
			js, _ := json.Marshal(obj)

			slice.Spec.Resources[j] = apiv1.Manifest{
				Manifest: string(js),
			}
			requests = append(requests, &Request{
				Resource: resource.Ref{
					Name:      obj.Name,
					Namespace: obj.Namespace,
					Kind:      obj.Kind,
				},
				Composition: types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace},
			})
		}
		resources[i] = slice
	}

	return comp, synth, resources, requests
}

func TestCacheRangeByReadinessGroup(t *testing.T) {
	ctx := testutil.NewContext(t)

	cli := testutil.NewClient(t)
	c := NewCache(cli)

	comp := &apiv1.Composition{}
	comp.Namespace = string(uuid.NewString())
	comp.Name = string(uuid.NewString())
	synth := &apiv1.Synthesis{UUID: uuid.NewString()} // just not 0
	comp.Status.CurrentSynthesis = synth
	compRef := NewSynthesisRef(comp)

	obj := &corev1.ConfigMap{}
	obj.Name = "default-group"
	obj.Namespace = "default"
	obj.Kind = "ConfigMap"
	obj.APIVersion = "v1"
	resources := []client.Object{}
	resources = append(resources, obj)

	obj = obj.DeepCopy()
	obj.Name = "group-1"
	obj.Annotations = map[string]string{
		"eno.azure.io/readiness-group": "1",
	}
	resources = append(resources, obj)

	obj = obj.DeepCopy()
	obj.Name = "group-3"
	obj.Annotations = map[string]string{
		"eno.azure.io/readiness-group": "3",
	}
	resources = append(resources, obj)

	obj = obj.DeepCopy()
	obj.Name = "group-also-1"
	obj.Annotations = map[string]string{
		"eno.azure.io/readiness-group": "1",
	}
	resources = append(resources, obj)

	slice := apiv1.ResourceSlice{}
	slice.Name = string(uuid.NewString())
	slice.Namespace = "slice-ns"
	for _, obj := range resources {
		js, _ := json.Marshal(obj)
		slice.Spec.Resources = append(slice.Spec.Resources, apiv1.Manifest{Manifest: string(js)})
	}

	_, err := c.fill(ctx, comp, synth, []apiv1.ResourceSlice{slice})
	require.NoError(t, err)

	// Ranging backwards from 0 should never return anything
	refs := c.RangeByReadinessGroup(ctx, compRef, 0, RangeDesc)
	assert.Equal(t, []string{}, reqsToNames(refs))

	// Ranging forwards after all groups should not return anything
	refs = c.RangeByReadinessGroup(ctx, compRef, 100, RangeAsc)
	assert.Equal(t, []string{}, reqsToNames(refs))

	// Prove synthesis refs are honored
	refs = c.RangeByReadinessGroup(ctx, &SynthesisRef{CompositionName: "nope"}, 1, RangeAsc)
	assert.Equal(t, []string{}, reqsToNames(refs))

	refs = c.RangeByReadinessGroup(ctx, &SynthesisRef{CompositionName: "nope"}, 1, RangeDesc)
	assert.Equal(t, []string{}, reqsToNames(refs))

	// This node doesn't exist in the tree, this isn't possible at runtime
	refs = c.RangeByReadinessGroup(ctx, compRef, 100, RangeDesc)
	assert.Equal(t, []string{}, reqsToNames(refs))

	// Ranging forwards returns all resources in the next group, but not groups after that
	refs = c.RangeByReadinessGroup(ctx, compRef, 0, RangeAsc)
	assert.Equal(t, []string{"group-1", "group-also-1"}, reqsToNames(refs))

	refs = c.RangeByReadinessGroup(ctx, compRef, 1, RangeAsc)
	assert.Equal(t, []string{"group-3"}, reqsToNames(refs))

	refs = c.RangeByReadinessGroup(ctx, compRef, 3, RangeAsc)
	assert.Equal(t, []string{}, reqsToNames(refs))

	// Ranging backwards returns all resources in the previous group, but not groups before that
	refs = c.RangeByReadinessGroup(ctx, compRef, 1, RangeDesc)
	assert.Equal(t, []string{"default-group"}, reqsToNames(refs))

	refs = c.RangeByReadinessGroup(ctx, compRef, 3, RangeDesc)
	assert.Equal(t, []string{"group-1", "group-also-1"}, reqsToNames(refs))
}

func reqsToNames(resources []*Resource) []string {
	strs := make([]string, len(resources))
	for i, resource := range resources {
		strs[i] = resource.Ref.Name
	}
	return strs
}
