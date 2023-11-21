package reconciliation

import (
	"context"
	"testing"

	openapi_v2 "github.com/google/gnostic-models/openapiv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/fake"
)

func TestDiscoveryCacheTypeMissingInitially(t *testing.T) {
	client := &fakeDiscovery{}
	d := &discoveryCache{client: client}

	gvk := schema.GroupVersionKind{
		Group:   "test-group",
		Version: "test-version",
		Kind:    "TestKind1",
	}

	t.Run("missing from spec", func(t *testing.T) {
		s, err := d.Get(context.Background(), gvk)
		require.EqualError(t, err, "resource was not found in openapi spec")
		assert.Nil(t, s)
	})

	t.Run("added to spec after initial cache fill", func(t *testing.T) {
		client.Schema = &openapi_v2.Document{} // TODO

		s, err := d.Get(context.Background(), gvk)
		require.NoError(t, err)
		assert.NotNil(t, s)
	})

	t.Run("cache hit", func(t *testing.T) {
		s, err := d.Get(context.Background(), gvk)
		require.NoError(t, err)
		assert.NotNil(t, s)
	})
}

// the fake.FakeDiscovery doesn't allow fake OpenAPISchema return values.
type fakeDiscovery struct {
	fake.FakeDiscovery
	Schema *openapi_v2.Document
}

func (f *fakeDiscovery) OpenAPISchema() (*openapi_v2.Document, error) {
	return f.Schema, nil
}
