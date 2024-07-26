package reconciliation

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/aggregation"
	"github.com/Azure/eno/internal/controllers/flowcontrol"
	"github.com/Azure/eno/internal/controllers/liveness"
	"github.com/Azure/eno/internal/controllers/replication"
	"github.com/Azure/eno/internal/controllers/rollout"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/controllers/watchdog"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	require.NoError(t, liveness.NewNamespaceController(mgr.Manager))
}

func writeGenericComposition(t *testing.T, client client.Client) (*apiv1.Synthesizer, *apiv1.Composition) {
	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "create"
	require.NoError(t, client.Create(context.Background(), syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, client.Create(context.Background(), comp))

	return syn, comp
}
