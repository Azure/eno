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
	rswb := flowcontrol.NewResourceSliceWriteBufferForManager(mgr.Manager, time.Hour, 1)

	var c Controller
	rm, err := reconstitution.New(mgr.Manager, &c)
	require.NoError(t, err)

	ctmp, err := New(rm, rswb, mgr.DownstreamRestConfig, 5, testutil.AtLeastVersion(t, 15), time.Hour)
	require.NoError(t, err)
	c = *ctmp

	return &c
}
