package discovery

import (
	"testing"
	"time"

	"github.com/Azure/eno/internal/testutil"
	openapi_v2 "github.com/google/gnostic-models/openapiv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/fake"
)

func TestDiscoveryCacheRefill(t *testing.T) {
	ctx := testutil.NewContext(t)
	client := &fakeDiscovery{Info: &openapi_v2.Info{Version: "v1.15.0"}}
	d := &Cache{client: client}

	gvk := schema.GroupVersionKind{
		Group:   "test-group",
		Version: "test-version",
		Kind:    "TestKind1",
	}

	s, err := d.Get(ctx, gvk)
	require.NoError(t, err)
	assert.Nil(t, s)

	s, err = d.Get(ctx, gvk)
	require.NoError(t, err)
	assert.Nil(t, s)

	assert.Equal(t, 4, client.Calls)
}

func TestDiscoveryCacheRefillDisabled(t *testing.T) {
	ctx := testutil.NewContext(t)
	client := &fakeDiscovery{Info: &openapi_v2.Info{Version: "v1.14.123"}}
	d := &Cache{client: client}

	gvk := schema.GroupVersionKind{
		Group:   "test-group",
		Version: "test-version",
		Kind:    "TestKind1",
	}

	s, err := d.Get(ctx, gvk)
	require.NoError(t, err)
	assert.Nil(t, s)

	s, err = d.Get(ctx, gvk)
	require.NoError(t, err)
	assert.Nil(t, s)

	assert.Equal(t, 1, client.Calls)
}

func TestDiscoveryCacheRefillVersionMissing(t *testing.T) {
	ctx := testutil.NewContext(t)
	client := &fakeDiscovery{}
	d := &Cache{client: client}

	gvk := schema.GroupVersionKind{
		Group:   "test-group",
		Version: "test-version",
		Kind:    "TestKind1",
	}

	s, err := d.Get(ctx, gvk)
	require.NoError(t, err)
	assert.Nil(t, s)

	s, err = d.Get(ctx, gvk)
	require.NoError(t, err)
	assert.Nil(t, s)

	assert.Equal(t, 4, client.Calls)
}

func TestDiscoveryCacheTimeout(t *testing.T) {
	ctx := testutil.NewContext(t)
	client := &fakeDiscovery{Info: &openapi_v2.Info{Version: "v1.14.123"}}
	d := &Cache{client: client}

	gvk := schema.GroupVersionKind{
		Group:   "test-group",
		Version: "test-version",
		Kind:    "TestKind1",
	}

	s, err := d.Get(ctx, gvk)
	require.NoError(t, err)
	assert.Nil(t, s)

	d.lastFill = d.lastFill.Add(-time.Hour * 25)

	s, err = d.Get(ctx, gvk)
	require.NoError(t, err)
	assert.Nil(t, s)

	assert.Equal(t, 2, client.Calls)
}

func TestWithRealApiserver(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cache, err := NewCache(mgr.DownstreamRestConfig, 10)
	require.NoError(t, err)

	gvk := schema.GroupVersionKind{
		Version: "v1",
		Kind:    "Pod",
	}
	s, err := cache.Get(ctx, gvk)
	require.NoError(t, err)
	assert.NotNil(t, s)
}

// the fake.FakeDiscovery doesn't allow fake OpenAPISchema return values.
type fakeDiscovery struct {
	fake.FakeDiscovery
	Info  *openapi_v2.Info
	Calls int
}

func (f *fakeDiscovery) OpenAPISchema() (*openapi_v2.Document, error) {
	f.Calls++
	return &openapi_v2.Document{Info: f.Info}, nil
}
