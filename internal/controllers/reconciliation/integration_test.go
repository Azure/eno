package reconciliation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/reconstitution"
	"github.com/Azure/eno/internal/testutil"
)

func TestControllerBasics(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, synthesis.NewRolloutController(mgr.Manager, time.Millisecond))
	require.NoError(t, synthesis.NewStatusController(mgr.Manager))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, &synthesis.Config{
		WrapperImage: "test-wrapper",
		MaxRestarts:  2,
		Timeout:      time.Second * 5,
	}))

	rm, err := reconstitution.New(mgr.Manager, time.Millisecond)
	require.NoError(t, err)

	testutil.NewPodController(t, mgr.Manager, func(c *apiv1.Composition, s *apiv1.Synthesizer) []*apiv1.ResourceSlice {
		slice := &apiv1.ResourceSlice{}
		slice.GenerateName = "test-"
		slice.Namespace = "default"
		slice.Spec.CompositionGeneration = c.Generation
		switch s.Spec.Image {
		case "create":
			slice.Spec.Resources = []apiv1.Manifest{{
				Manifest: `{
					"apiVersion": "v1",
					"kind": "ConfigMap",
					"metadata": {
						"name": "test-configmap",
						"namespace": "default"
					}
				}`,
			}}
		case "update":
			slice.Spec.Resources = []apiv1.Manifest{{
				Manifest: `{
					"apiVersion": "v1",
					"kind": "ConfigMap",
					"metadata": {
						"name": "test-configmap",
						"namespace": "default"
					},
					"data": {
						"test-key": "test-value"
					}
				}`,
			}}
		default:
			t.Fatalf("unknown pseudo-image: %s", s.Spec.Image)
		}
		return []*apiv1.ResourceSlice{slice}
	})

	require.NoError(t, New(rm, mgr.RestConfig))
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "create"
	require.NoError(t, cli.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, cli.Create(ctx, comp))

	t.Run("creation", func(t *testing.T) {
		testutil.Eventually(t, func() bool {
			cm := &corev1.ConfigMap{}
			cm.Name = "test-configmap"
			cm.Namespace = "default"
			return cli.Get(ctx, client.ObjectKeyFromObject(cm), cm) == nil
		})
	})

	t.Run("update", func(t *testing.T) {
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			if err := cli.Get(ctx, client.ObjectKeyFromObject(syn), syn); err != nil {
				return err
			}
			syn.Spec.Image = "update"
			return cli.Update(ctx, syn)
		})
		require.NoError(t, err)

		testutil.Eventually(t, func() bool {
			cm := &corev1.ConfigMap{}
			cm.Name = "test-configmap"
			cm.Namespace = "default"
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(cm), cm))
			return cm.Data != nil && cm.Data["test-key"] == "test-value"
		})
	})
}