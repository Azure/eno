package reconciliation

import (
	"testing"
	"time"

	"github.com/Azure/eno/internal/controllers/aggregation"
	"github.com/Azure/eno/internal/controllers/flowcontrol"
	"github.com/Azure/eno/internal/controllers/replication"
	"github.com/Azure/eno/internal/controllers/rollout"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/controllers/watchdog"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
)

func registerControllers(t *testing.T, mgr *testutil.Manager) {
	require.NoError(t, aggregation.NewSliceController(mgr.Manager))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, defaultConf))
	require.NoError(t, synthesis.NewSliceCleanupController(mgr.Manager))
	require.NoError(t, watchdog.NewController(mgr.Manager, time.Second*10))
	require.NoError(t, replication.NewSymphonyController(mgr.Manager))
	require.NoError(t, aggregation.NewSymphonyController(mgr.Manager))
	require.NoError(t, aggregation.NewCompositionController(mgr.Manager))
	require.NoError(t, rollout.NewController(mgr.Manager, time.Millisecond))
	require.NoError(t, rollout.NewSynthesizerController(mgr.Manager))
	require.NoError(t, flowcontrol.NewSynthesisConcurrencyLimiter(mgr.Manager, 10, 0))
}
