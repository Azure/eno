package reconciliation

import (
	"testing"
	"time"

	"github.com/Azure/eno/internal/flowcontrol"
	"github.com/Azure/eno/internal/readiness"
	"github.com/Azure/eno/internal/reconstitution"
	"github.com/Azure/eno/internal/resource"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/util/workqueue"
)

func setupTestSubject(t *testing.T, mgr *testutil.Manager) *Controller {
	rswb := flowcontrol.NewResourceSliceWriteBufferForManager(mgr.Manager)
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedItemBasedRateLimiter[resource.Request]())
	renv, err := readiness.NewEnv()
	require.NoError(t, err)
	cache := resource.NewCache(renv, queue)

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
