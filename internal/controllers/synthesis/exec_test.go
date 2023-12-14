package synthesis

import (
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/Azure/eno/internal/testutil"
)

func TestExecIntegration(t *testing.T) {
	// Skip this by default since it requires a real k8s cluster and doesn't currently clean up after itself
	// I'm thinking we may want to replace this with a proper e2e test once things are glued together.
	if os.Getenv("RUN_EXEC_INTEGRATION") == "" {
		t.Skipf("skipping pod exec integration test")
	}

	ctx := testutil.NewContext(t)
	mgr, err := manager.New(logr.FromContextOrDiscard(ctx), &manager.Options{
		Rest: ctrl.GetConfigOrDie(),
	})
	require.NoError(t, err)
	cli := mgr.GetClient()

	require.NoError(t, NewPodLifecycleController(mgr, minimalTestConfig))
	require.NoError(t, NewStatusController(mgr))
	require.NoError(t, NewRolloutController(mgr, time.Millisecond*10))
	require.NoError(t, NewExecController(mgr))
	go mgr.Start(ctx)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "alpine:latest"
	require.NoError(t, cli.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, cli.Create(ctx, comp))

	// The pod eventually performs the synthesis
	testutil.SomewhatEventually(t, time.Second*30, func() bool {
		err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentState != nil && comp.Status.CurrentState.Synthesized
	})
}
