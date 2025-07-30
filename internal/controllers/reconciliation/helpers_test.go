package reconciliation

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/composition"
	"github.com/Azure/eno/internal/controllers/liveness"
	"github.com/Azure/eno/internal/controllers/resourceslice"
	"github.com/Azure/eno/internal/controllers/scheduling"
	"github.com/Azure/eno/internal/controllers/symphony"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/controllers/watch"
	"github.com/Azure/eno/internal/flowcontrol"
	"github.com/Azure/eno/internal/resource"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func registerControllers(t *testing.T, mgr *testutil.Manager) {
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, defaultConf))
	require.NoError(t, synthesis.NewPodGC(mgr.Manager, time.Second))
	require.NoError(t, scheduling.NewController(mgr.Manager, 10, time.Millisecond, time.Second))
	require.NoError(t, liveness.NewNamespaceController(mgr.Manager, 3, time.Second))
	require.NoError(t, watch.NewController(mgr.Manager))
	require.NoError(t, resourceslice.NewController(mgr.Manager))
	require.NoError(t, resourceslice.NewCleanupController(mgr.Manager))
	require.NoError(t, composition.NewController(mgr.Manager))
	require.NoError(t, symphony.NewController(mgr.Manager))
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

func setupTestSubject(t *testing.T, mgr *testutil.Manager) {
	setupTestSubjectForOptions(t, mgr, Options{
		Manager:                mgr.Manager,
		Timeout:                time.Minute,
		ReadinessPollInterval:  time.Hour,
		DisableServerSideApply: mgr.NoSsaSupport,
	})
}

func setupTestSubjectForOptions(t *testing.T, mgr *testutil.Manager, opts Options) {
	opts.WriteBuffer = flowcontrol.NewResourceSliceWriteBufferForManager(mgr.Manager)

	var cache resource.Cache
	rateLimiter := workqueue.DefaultTypedItemBasedRateLimiter[resource.Request]()
	queue := workqueue.NewTypedRateLimitingQueue(rateLimiter)
	cache.SetQueue(queue)

	opts.Downstream = rest.CopyConfig(mgr.DownstreamRestConfig)
	opts.Downstream.QPS = 200 // minimal throttling for the tests

	err := New(mgr.Manager, opts)
	require.NoError(t, err)
}

func requireSSA(t *testing.T, mgr *testutil.Manager) {
	if mgr.DownstreamVersion > 0 && mgr.DownstreamVersion < 16 {
		t.Skipf("skipping test because it requires server-side apply which isn't supported on k8s 1.%d", mgr.DownstreamVersion)
	}
}
