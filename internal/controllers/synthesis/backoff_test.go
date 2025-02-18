package synthesis

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/scheduling"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestControllerBackoff(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewPodLifecycleController(mgr.Manager, minimalTestConfig))
	require.NoError(t, scheduling.NewController(mgr.Manager, 10, 2*time.Second, time.Second))
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn-1"
	syn.Spec.Image = "initial-image"
	syn.Spec.PodTimeout = &metav1.Duration{Duration: time.Millisecond * 2}
	syn.Spec.ExecTimeout = &metav1.Duration{Duration: time.Millisecond}
	require.NoError(t, cli.Create(ctx, syn))

	start := time.Now()
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, cli.Create(ctx, comp))

	t.Run("initial creation", func(t *testing.T) {
		testutil.Eventually(t, func() bool {
			require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
			return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Attempts >= 10
		})

		// It shouldn't be possible to try this many times within 250ms
		assert.Greater(t, int(time.Since(start).Milliseconds()), 250)
	})
}
