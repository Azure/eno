package reconciliation

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	testv1 "github.com/Azure/eno/internal/controllers/reconciliation/fixtures/v1"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/reconstitution"
	"github.com/Azure/eno/internal/testutil"
)

// https://github.com/kubernetes/kubectl/blob/eb3138bd9f8c0a4ec3dece5d431ba21be9c0bcf1/pkg/cmd/apply/patcher.go#L187

// TODO: Add CR test

func TestTEMP(t *testing.T) { // TODO
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)

	// Register supporting controllers
	rm, err := reconstitution.New(mgr.Manager, time.Millisecond)
	require.NoError(t, err)

	c, err := New(rm, mgr.DownstreamRestConfig, 5, testutil.AtLeastVersion(t, 15))
	require.NoError(t, err)
	mgr.Start(t)

	svc := &corev1.Service{}
	svc.Name = "test-service"
	svc.Namespace = "default"
	svc.APIVersion = "v1"
	svc.Kind = "Service"
	svc.Spec.Ports = []corev1.ServicePort{{
		Name: "foo",
		Port: 123,
	}}
	require.NoError(t, mgr.DownstreamClient.Create(ctx, svc))
	prevMani, _ := json.Marshal(svc)
	svc.APIVersion = "v1" // ?
	svc.Kind = "Service"

	copy := svc.DeepCopy()
	copy.Spec.Ports = append(copy.Spec.Ports, corev1.ServicePort{
		Name: "external",
		Port: 234,
	})
	require.NoError(t, mgr.DownstreamClient.Update(ctx, copy))
	currentMani, _ := json.Marshal(copy)

	svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{
		Name: "bar",
		Port: 234,
	})
	nextMani, _ := json.Marshal(svc)

	prev := &reconstitution.Resource{Manifest: string(prevMani)}
	next := &reconstitution.Resource{Manifest: string(nextMani)}
	current := &unstructured.Unstructured{Object: map[string]interface{}{}}
	require.NoError(t, current.UnmarshalJSON([]byte(currentMani)))

	patch, _, err := c.buildPatch(ctx, prev, next, current)
	require.NoError(t, err)

	t.Errorf("PATCH: %s", patch)
}

func TestControllerBasics(t *testing.T) {
	tests := []struct {
		Name                         string
		Empty, Initial, Updated      client.Object
		AssertCreated, AssertUpdated func(t *testing.T, obj client.Object)
		ApplyExternalUpdate          func(t *testing.T, obj client.Object) client.Object
	}{
		{
			Name:  "strategic-merge", // this test covers list merge logic and will fail if non-strategic merge is used
			Empty: &corev1.Service{},
			Initial: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-obj",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{{
						Name:       "first",
						Port:       1234,
						Protocol:   corev1.ProtocolTCP,
						TargetPort: intstr.FromInt(1234), // TODO: Shouldn't be necessary
					}},
				},
			},
			AssertCreated: func(t *testing.T, obj client.Object) {
				svc := obj.(*corev1.Service).Spec
				assert.Equal(t, []corev1.ServicePort{{
					Name:       "first",
					Port:       1234,
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.FromInt(1234),
				}}, svc.Ports)
			},
			ApplyExternalUpdate: func(t *testing.T, obj client.Object) client.Object {
				svc := obj.(*corev1.Service).DeepCopy()
				svc.Spec.Ports = []corev1.ServicePort{{
					Name:       "second",
					Port:       2345,
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.FromInt(2345),
				}}
				return svc
			},
			Updated: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-obj",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{{
						Name:       "third",
						Port:       3456,
						Protocol:   corev1.ProtocolTCP,
						TargetPort: intstr.FromInt(3456),
					}},
				},
			},
			AssertUpdated: func(t *testing.T, obj client.Object) {
				svc := obj.(*corev1.Service).Spec
				assert.Equal(t, []corev1.ServicePort{
					{
						Name:       "third",
						Port:       3456,
						Protocol:   corev1.ProtocolTCP,
						TargetPort: intstr.FromInt(3456),
					},
					{
						Name:       "second",
						Port:       2345,
						Protocol:   corev1.ProtocolTCP,
						TargetPort: intstr.FromInt(2345),
					},
				}, svc.Ports)
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
				t.Logf("resource json %s", js)

				slice := &apiv1.ResourceSlice{}
				slice.GenerateName = "test-"
				slice.Namespace = "default"
				slice.Spec.CompositionGeneration = c.Generation
				slice.Spec.Resources = []apiv1.Manifest{{Manifest: string(js)}}
				return []*apiv1.ResourceSlice{slice}
			})

			// Test subject
			// Only enable rediscoverWhenNotFound on k8s versions that can support it.
			_, err = New(rm, mgr.DownstreamRestConfig, 5, testutil.AtLeastVersion(t, 15))
			require.NoError(t, err)
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

			var lastResourceVersion string
			t.Run("creation", func(t *testing.T) {
				obj := test.Empty.DeepCopyObject().(client.Object)
				obj.SetName(test.Initial.GetName())
				obj.SetNamespace(test.Initial.GetNamespace())
				testutil.Eventually(t, func() bool {
					return downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj) == nil
				})
				test.AssertCreated(t, obj)
				lastResourceVersion = obj.GetResourceVersion()

				// wait for the initial patch to hit the informer
				testutil.Eventually(t, func() bool {
					return downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj) == nil && obj.GetResourceVersion() == lastResourceVersion
				})
			})

			if test.ApplyExternalUpdate != nil {
				t.Run("external update", func(t *testing.T) {
					err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
						obj := test.Empty.DeepCopyObject().(client.Object)
						obj.SetName(test.Initial.GetName())
						obj.SetNamespace(test.Initial.GetNamespace())
						if err := downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
							return err
						}

						updatedObj := test.ApplyExternalUpdate(t, obj)
						if err := downstream.Update(ctx, updatedObj); err != nil {
							return err
						}

						lastResourceVersion = updatedObj.GetResourceVersion()
						t.Logf("external update version %s", lastResourceVersion)
						return nil
					})
					require.NoError(t, err)

					// wait for this write to hit the informer cache
					obj := test.Empty.DeepCopyObject().(client.Object)
					obj.SetName(test.Initial.GetName())
					obj.SetNamespace(test.Initial.GetNamespace())
					testutil.Eventually(t, func() bool {
						return downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj) == nil && obj.GetResourceVersion() == lastResourceVersion
					})
					lastResourceVersion = obj.GetResourceVersion()
				})
			}

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
					return downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj) == nil && obj.GetResourceVersion() != lastResourceVersion
				})
				test.AssertUpdated(t, obj)
			})
		})
	}
}
