package reconciliation

import (
	"testing"
	"time"

	"github.com/Azure/eno/internal/flowcontrol"
	"github.com/Azure/eno/internal/reconstitution"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMungePatch(t *testing.T) {
	patch, err := mungePatch([]byte(`{"metadata":{"creationTimestamp":"2024-03-05T00:45:27Z"}, "foo":"bar"}`), "test-rv")
	require.NoError(t, err)
	assert.JSONEq(t, `{"metadata":{"resourceVersion":"test-rv"},"foo":"bar"}`, string(patch))
}

func TestMungePatchEmpty(t *testing.T) {
	patch, err := mungePatch([]byte(`{"metadata":{"creationTimestamp":"2024-03-05T00:45:27Z"}}`), "test-rv")
	require.NoError(t, err)
	assert.Nil(t, patch)
}

func setupTestSubject(t *testing.T, mgr *testutil.Manager) *Controller {
	rswb := flowcontrol.NewResourceSliceWriteBufferForManager(mgr.Manager, time.Millisecond*10, 1)
	cache := reconstitution.NewCache(mgr.GetClient())
	rc, err := New(Options{
		Manager:                mgr.Manager,
		Cache:                  cache,
		WriteBuffer:            rswb,
		Downstream:             mgr.DownstreamRestConfig,
		DiscoveryRPS:           5,
		RediscoverWhenNotFound: testutil.AtLeastVersion(t, 15),
		Timeout:                time.Minute,
		ReadinessPollInterval:  time.Hour,
	})
	require.NoError(t, err)

	err = reconstitution.New(mgr.Manager, cache, rc)
	require.NoError(t, err)

	return rc
}
