package reconciliation

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/aggregation"
	"github.com/Azure/eno/internal/controllers/liveness"
	"github.com/Azure/eno/internal/controllers/replication"
	"github.com/Azure/eno/internal/controllers/scheduling"
	"github.com/Azure/eno/internal/controllers/selfhealing"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/controllers/watch"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func registerControllers(t *testing.T, mgr *testutil.Manager) {
	require.NoError(t, aggregation.NewSliceController(mgr.Manager))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, defaultConf))
	require.NoError(t, synthesis.NewSliceCleanupController(mgr.Manager))
	require.NoError(t, replication.NewSymphonyController(mgr.Manager))
	require.NoError(t, aggregation.NewSymphonyController(mgr.Manager))
	require.NoError(t, aggregation.NewCompositionController(mgr.Manager))
	require.NoError(t, scheduling.NewController(mgr.Manager, 10, time.Millisecond, time.Second))
	require.NoError(t, liveness.NewNamespaceController(mgr.Manager, 3, time.Second))
	require.NoError(t, watch.NewController(mgr.Manager))
	require.NoError(t, selfhealing.NewSliceController(mgr.Manager, time.Minute*5))
}

func writeGenericComposition(t *testing.T, client client.Client) (*apiv1.Synthesizer, *apiv1.Composition) {
	return writeComposition(t, client, false)
}

func writeComposition(t *testing.T, client client.Client, orphan bool) (*apiv1.Synthesizer, *apiv1.Composition) {
	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "create"
	require.NoError(t, client.Create(context.Background(), syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	if orphan {
		comp.Annotations = map[string]string{"eno.azure.io/deletion-strategy": "orphan"}
	}
	require.NoError(t, client.Create(context.Background(), comp))

	return syn, comp
}
