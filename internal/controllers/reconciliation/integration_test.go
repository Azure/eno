package reconciliation

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	testv1 "github.com/Azure/eno/internal/controllers/reconciliation/fixtures/v1"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/reconstitution"
	"github.com/Azure/eno/internal/testutil"
)

func TestControllerBasics(t *testing.T) {
	tests := []struct {
		Name                         string
		Empty, Initial, Updated      client.Object
		AssertCreated, AssertUpdated func(t *testing.T, obj client.Object)
	}{
		{
			Name:  "pod",
			Empty: &corev1.Pod{},
			Initial: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "container-1",
							Image: "image-1",
						},
						{
							Name:  "container-2",
							Image: "image-2",
						},
					},
				},
			},
			AssertCreated: func(t *testing.T, obj client.Object) {
				expected := []corev1.Container{
					{
						Name:  "container-1",
						Image: "image-1",
					},
					{
						Name:  "container-2",
						Image: "image-2",
					},
				}
				pod := obj.(*corev1.Pod)
				assert.Equal(t, expected, pod.Spec.Containers)
			},
			Updated: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "container-2",
							Image: "image-3",
						},
						{
							Name:  "container-1",
							Image: "image-1",
						},
					},
				},
			},
			AssertUpdated: func(t *testing.T, obj client.Object) {
				expected := []corev1.Container{
					{
						Name:  "container-1",
						Image: "image-1",
					},
					{
						Name:  "container-2",
						Image: "image-3",
					},
				}
				pod := obj.(*corev1.Pod)
				assert.Equal(t, expected, pod.Spec.Containers)
			},
		},
	}

	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	for _, test := range tests {
		test := test
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.NewContext(t)
			mgr := testutil.NewManager(t)
			upstream := mgr.GetClient()
			downstream := mgr.DownstreamClient

			// Register supporting controllers
			rm, err := reconstitution.New(mgr.Manager, time.Millisecond)
			require.NoError(t, err)
			require.NoError(t, synthesis.NewRolloutController(mgr.Manager, time.Millisecond))
			require.NoError(t, synthesis.NewStatusController(mgr.Manager))
			require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, &synthesis.Config{
				WrapperImage: "test-wrapper",
				MaxRestarts:  2,
				Timeout:      time.Second * 5,
			}))

			// Simulate synthesis of our test composition into the resources specified by the test case
			testutil.NewPodController(t, mgr.Manager, func(c *apiv1.Composition, s *apiv1.Synthesizer) []*apiv1.ResourceSlice {
				var obj client.Object
				switch s.Spec.Image {
				case "create":
					obj = test.Initial.DeepCopyObject().(client.Object)
				case "update":
					obj = test.Updated.DeepCopyObject().(client.Object)
				default:
					t.Fatalf("unknown pseudo-image: %s", s.Spec.Image)
				}

				gvks, _, err := scheme.ObjectKinds(obj)
				require.NoError(t, err)
				obj.GetObjectKind().SetGroupVersionKind(gvks[0])

				js, err := json.Marshal(obj)
				require.NoError(t, err)

				slice := &apiv1.ResourceSlice{}
				slice.GenerateName = "test-"
				slice.Namespace = "default"
				slice.Spec.CompositionGeneration = c.Generation
				slice.Spec.Resources = []apiv1.Manifest{{Manifest: string(js)}}
				return []*apiv1.ResourceSlice{slice}
			})

			// Test subject
			// Only enable rediscoverWhenNotFound on k8s versions that can support it.
			require.NoError(t, New(rm, mgr.DownstreamRestConfig, 5, testutil.AtLeastVersion(t, 15)))
			mgr.Start(t)

			// Any syn/comp will do since we faked out the synthesizer pod
			syn := &apiv1.Synthesizer{}
			syn.Name = "test-syn"
			syn.Spec.Image = "create"
			require.NoError(t, upstream.Create(ctx, syn))

			comp := &apiv1.Composition{}
			comp.Name = "test-comp"
			comp.Namespace = "default"
			comp.Spec.Synthesizer.Name = syn.Name
			require.NoError(t, upstream.Create(ctx, comp))

			var initialResourceVersion string
			t.Run("creation", func(t *testing.T) {
				obj := test.Empty.DeepCopyObject().(client.Object)
				obj.SetName(test.Initial.GetName())
				obj.SetNamespace(test.Initial.GetNamespace())
				testutil.Eventually(t, func() bool {
					return downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj) == nil
				})
				initialResourceVersion = obj.GetResourceVersion()
				test.AssertCreated(t, obj)
			})

			t.Run("update", func(t *testing.T) {
				err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
					if err := upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn); err != nil {
						return err
					}
					syn.Spec.Image = "update"
					return upstream.Update(ctx, syn)
				})
				require.NoError(t, err)

				obj := test.Empty.DeepCopyObject().(client.Object)
				obj.SetName(test.Initial.GetName())
				obj.SetNamespace(test.Initial.GetNamespace())
				testutil.Eventually(t, func() bool {
					return downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj) == nil && obj.GetResourceVersion() != initialResourceVersion
				})
				test.AssertUpdated(t, obj)
			})
		})
	}
}
