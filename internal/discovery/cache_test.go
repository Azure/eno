package discovery

import (
	"context"
	"testing"

	openapi_v2 "github.com/google/gnostic-models/openapiv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/fake"
)

// Positive behavior is covered by controller integration tests

func TestDiscoveryCacheRefill(t *testing.T) {
	client := &fakeDiscovery{}
	d := &Cache{client: client, fillWhenNotFound: true}

	gvk := schema.GroupVersionKind{
		Group:   "test-group",
		Version: "test-version",
		Kind:    "TestKind1",
	}

	s, err := d.Get(context.Background(), gvk)
	require.NoError(t, err)
	assert.Nil(t, s)

	s, err = d.Get(context.Background(), gvk)
	require.NoError(t, err)
	assert.Nil(t, s)

	assert.Equal(t, 4, client.Calls)
}

func TestDiscoveryCacheRefillDisabled(t *testing.T) {
	client := &fakeDiscovery{}
	d := &Cache{client: client, fillWhenNotFound: false}

	gvk := schema.GroupVersionKind{
		Group:   "test-group",
		Version: "test-version",
		Kind:    "TestKind1",
	}

	s, err := d.Get(context.Background(), gvk)
	require.NoError(t, err)
	assert.Nil(t, s)

	s, err = d.Get(context.Background(), gvk)
	require.NoError(t, err)
	assert.Nil(t, s)

	assert.Equal(t, 1, client.Calls)
}

// the fake.FakeDiscovery doesn't allow fake OpenAPISchema return values.
type fakeDiscovery struct {
	fake.FakeDiscovery
	Calls int
}

func (f *fakeDiscovery) OpenAPISchema() (*openapi_v2.Document, error) {
	f.Calls++
	return &openapi_v2.Document{}, nil
}
