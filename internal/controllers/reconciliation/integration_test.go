package reconciliation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	testv1 "github.com/Azure/eno/internal/controllers/reconciliation/fixtures/v1"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/reconstitution"
	"github.com/Azure/eno/internal/testutil"
)

func TestControllerPodBasics(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

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
		// TODO: This should use multiple containers
		switch s.Spec.Image {
		case "create":
			slice.Spec.Resources = []apiv1.Manifest{{
				Manifest: `{
					"apiVersion": "v1",
					"kind": "Pod",
					"metadata": {
						"name": "test-pod",
						"namespace": "default"
					},
					"spec": {
						"containers": [{
							"name": "test",
							"image": "test-image-1"
						}]
					}
				}`,
			}}
		case "update":
			slice.Spec.Resources = []apiv1.Manifest{{
				Manifest: `{
					"apiVersion": "v1",
					"kind": "Pod",
					"metadata": {
						"name": "test-pod",
						"namespace": "default"
					},
					"spec": {
						"containers": [{
							"name": "test",
							"image": "test-image-2"
						}]
					}
				}`,
			}}
		default:
			t.Fatalf("unknown pseudo-image: %s", s.Spec.Image)
		}
		return []*apiv1.ResourceSlice{slice}
	})

	require.NoError(t, New(rm, mgr.DownstreamRestConfig))
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "create"
	require.NoError(t, upstream.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, upstream.Create(ctx, comp))

	t.Run("creation", func(t *testing.T) {
		testutil.Eventually(t, func() bool {
			pod := &corev1.Pod{}
			pod.Name = "test-pod"
			pod.Namespace = "default"
			return downstream.Get(ctx, client.ObjectKeyFromObject(pod), pod) == nil
		})
	})

	// we expect this to use strategic merge
	t.Run("update", func(t *testing.T) {
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			if err := upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn); err != nil {
				return err
			}
			syn.Spec.Image = "update"
			return upstream.Update(ctx, syn)
		})
		require.NoError(t, err)

		testutil.Eventually(t, func() bool {
			pod := &corev1.Pod{}
			pod.Name = "test-pod"
			pod.Namespace = "default"
			require.NoError(t, downstream.Get(ctx, client.ObjectKeyFromObject(pod), pod))
			return pod.Spec.Containers[0].Image == "test-image-2"
		})
	})
}

func TestControllerCRBasics(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

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
					"apiVersion": "enotest.azure.io/v1",
					"kind": "TestResource",
					"metadata": {
						"name": "test-resource",
						"namespace": "default"
					},
					"spec": {
						"values": [{ "int": 123 }]
					}
				}`,
			}}
		case "update":
			slice.Spec.Resources = []apiv1.Manifest{{
				Manifest: `{
					"apiVersion": "enotest.azure.io/v1",
					"kind": "TestResource",
					"metadata": {
						"name": "test-resource",
						"namespace": "default"
					},
					"spec": {
						"values": [{ "int": 234 }, { "int": 345 }]
					}
				}`,
			}}
		default:
			t.Fatalf("unknown pseudo-image: %s", s.Spec.Image)
		}
		return []*apiv1.ResourceSlice{slice}
	})

	require.NoError(t, New(rm, mgr.DownstreamRestConfig))
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "create"
	require.NoError(t, upstream.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, upstream.Create(ctx, comp))

	t.Run("creation", func(t *testing.T) {
		testutil.Eventually(t, func() bool {
			cr := &testv1.TestResource{}
			cr.Name = "test-resource"
			cr.Namespace = "default"
			return downstream.Get(ctx, client.ObjectKeyFromObject(cr), cr) == nil
		})
	})

	// we do not expect this to use strategic merge because CRs do not support it
	t.Run("update", func(t *testing.T) {
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			if err := upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn); err != nil {
				return err
			}
			syn.Spec.Image = "update"
			return upstream.Update(ctx, syn)
		})
		require.NoError(t, err)

		testutil.Eventually(t, func() bool {
			cr := &testv1.TestResource{}
			cr.Name = "test-resource"
			cr.Namespace = "default"
			require.NoError(t, downstream.Get(ctx, client.ObjectKeyFromObject(cr), cr))
			return len(cr.Spec.Values) == 2 && cr.Spec.Values[0].Int == 234 && cr.Spec.Values[1].Int == 345
		})
	})
}
