package reconstitution

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

func TestCacheBasics(t *testing.T) {
	ctx := testutil.NewContext(t)

	client := testutil.NewClient(t)
	c := newCache(client)

	comp, synth, resources, expectedReqs := newCacheTestFixtures(2, 3)
	t.Run("fill", func(t *testing.T) {
		reqs, err := c.Fill(ctx, comp, synth, resources)
		require.NoError(t, err)
		assert.Equal(t, expectedReqs, reqs)
	})

	t.Run("check", func(t *testing.T) {
		// positive
		assert.True(t, c.HasSynthesis(ctx, comp, synth))

		// negative
		assert.False(t, c.HasSynthesis(ctx, comp, &apiv1.Synthesis{ObservedCompositionGeneration: 123}))
	})

	t.Run("get", func(t *testing.T) {
		// positive
		resource, exists := c.Get(ctx, &expectedReqs[0].ResourceRef, synth.ObservedCompositionGeneration)
		require.True(t, exists)
		assert.NotEmpty(t, resource.Manifest)
		assert.Equal(t, "ConfigMap", resource.Object.GetKind())
		assert.Equal(t, "slice-0-resource-0", resource.Object.GetName())

		// negative
		_, exists = c.Get(ctx, &expectedReqs[0].ResourceRef, 123)
		assert.False(t, exists)
	})

	t.Run("purge", func(t *testing.T) {
		c.Purge(ctx, types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}, nil)

		// confirm
		_, exists := c.Get(ctx, &expectedReqs[0].ResourceRef, synth.ObservedCompositionGeneration)
		assert.False(t, exists)

		assert.Len(t, c.resources, 0)
	})
}

func TestCacheInvalidManifest(t *testing.T) {
	ctx := testutil.NewContext(t)

	client := testutil.NewClient(t)
	c := newCache(client)

	comp, synth, resources, _ := newCacheTestFixtures(1, 1)
	resources[0].Spec.Resources[0].Manifest = "not valid json"

	_, err := c.Fill(ctx, comp, synth, resources)
	require.ErrorContains(t, err, "invalid json:")
}

func TestCacheManifestMissingName(t *testing.T) {
	ctx := testutil.NewContext(t)

	client := testutil.NewClient(t)
	c := newCache(client)

	comp, synth, resources, _ := newCacheTestFixtures(1, 1)
	resources[0].Spec.Resources[0].Manifest = `{"kind":"ConfigMap"}`

	_, err := c.Fill(ctx, comp, synth, resources)
	require.ErrorContains(t, err, "missing name, kind, or apiVersion")
}

func TestCachePartialPurge(t *testing.T) {
	ctx := testutil.NewContext(t)

	client := testutil.NewClient(t)
	c := newCache(client)

	// Fill our main composition
	comp, synth, resources, _ := newCacheTestFixtures(3, 4)
	_, err := c.Fill(ctx, comp, synth, resources)
	require.NoError(t, err)
	originalGen := synth.ObservedCompositionGeneration
	compNSN := types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}

	// Add another resource to the composition but synthesized from a newer generation
	_, _, resources, expectedReqs := newCacheTestFixtures(1, 1)
	synth.ObservedCompositionGeneration++
	expectedReqs[0].Composition = compNSN
	_, err = c.Fill(ctx, comp, synth, resources)
	require.NoError(t, err)

	// Fill another composition - this one shouldn't be purged
	var toBePreserved *ResourceRef
	{
		comp, synth, resources, expectedReqs := newCacheTestFixtures(3, 4)
		_, err := c.Fill(ctx, comp, synth, resources)
		require.NoError(t, err)
		toBePreserved = &expectedReqs[0].ResourceRef
	}

	comp.Status.CurrentState = synth // only reference the most recent synthesis

	// Purge only a single synthesis of a generation
	c.Purge(ctx, compNSN, comp)

	// The newer resource should still exist
	_, exists := c.Get(ctx, &expectedReqs[0].ResourceRef, synth.ObservedCompositionGeneration)
	assert.True(t, exists)

	// The older resource is not referenced by the composition and should have been removed
	_, exists = c.Get(ctx, &expectedReqs[0].ResourceRef, originalGen)
	assert.False(t, exists)

	// Resource of the other composition are unaffected
	_, exists = c.Get(ctx, toBePreserved, originalGen)
	assert.True(t, exists)

	// The cache should only be internally tracking the remaining synthesis of our test composition
	assert.Len(t, c.synthesesByComposition[compNSN], 1)
}

func newCacheTestFixtures(sliceCount, resPerSliceCount int) (*apiv1.Composition, *apiv1.Synthesis, []apiv1.ResourceSlice, []*Request) {
	comp := &apiv1.Composition{}
	comp.Namespace = string(uuid.NewUUID())
	comp.Name = string(uuid.NewUUID())
	synth := &apiv1.Synthesis{ObservedCompositionGeneration: 3} // just not 0

	resources := make([]apiv1.ResourceSlice, sliceCount)
	requests := []*Request{}
	for i := 0; i < sliceCount; i++ {
		slice := apiv1.ResourceSlice{}
		slice.Name = string(uuid.NewUUID())
		slice.Namespace = "slice-ns"
		slice.Spec.Resources = make([]apiv1.Manifest, resPerSliceCount)

		for j := 0; j < resPerSliceCount; j++ {
			resource := &corev1.ConfigMap{}
			resource.Name = fmt.Sprintf("slice-%d-resource-%d", i, j)
			resource.Namespace = "resource-ns"
			resource.Kind = "ConfigMap"
			resource.APIVersion = "v1"
			js, _ := json.Marshal(resource)

			slice.Spec.Resources[j] = apiv1.Manifest{
				Manifest: string(js),
			}
			requests = append(requests, &Request{
				ResourceRef: ResourceRef{
					Composition: types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace},
					Name:        resource.Name,
					Namespace:   resource.Namespace,
					Kind:        resource.Kind,
				},
				Manifest: ManifestRef{
					Slice: types.NamespacedName{
						Name:      slice.Name,
						Namespace: slice.Namespace,
					},
					Index: j,
				},
			})
		}
		resources[i] = slice
	}

	return comp, synth, resources, requests
}
