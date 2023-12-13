package reconciliation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// TODO: Test what happens if the resource already exists but we have no previous record of it

// TODO: Assert on status

type crudTestCase struct {
	Name                         string
	Empty, Initial, Updated      client.Object
	AssertCreated, AssertUpdated func(t *testing.T, obj client.Object)
	ApplyExternalUpdate          func(t *testing.T, obj client.Object) client.Object
}

var crudTests = []crudTestCase{
	{
		Name:  "strategic-merge", // will fail if non-strategic merge is used
		Empty: &corev1.Service{},
		Initial: &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-obj",
				Namespace: "default",
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{{
					Name:     "first",
					Port:     1234,
					Protocol: corev1.ProtocolTCP,
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
				Name:     "second",
				Port:     2345,
				Protocol: corev1.ProtocolTCP,
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
					Name:     "third",
					Port:     3456,
					Protocol: corev1.ProtocolTCP,
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
	{
		Name:  "cr-basics",
		Empty: &testv1.TestResource{},
		Initial: &testv1.TestResource{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cr",
				Namespace: "default",
			},
			Spec: testv1.TestResourceSpec{
				Values: []*testv1.TestValue{{Int: 1}, {Int: 2}},
			},
		},
		AssertCreated: func(t *testing.T, obj client.Object) {
			tr := obj.(*testv1.TestResource)
			assert.Equal(t, []*testv1.TestValue{{Int: 1}, {Int: 2}}, tr.Spec.Values)
		},
		Updated: &testv1.TestResource{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cr",
				Namespace: "default",
			},
			Spec: testv1.TestResourceSpec{
				Values: []*testv1.TestValue{{Int: 2}},
			},
		},
		AssertUpdated: func(t *testing.T, obj client.Object) {
			tr := obj.(*testv1.TestResource)
			assert.Equal(t, []*testv1.TestValue{{Int: 2}}, tr.Spec.Values)
		},
	},
}

func TestCRUD(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	for _, test := range crudTests {
		test := test
		t.Run(test.Name, func(t *testing.T) {
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
			testutil.NewPodController(t, mgr.Manager, newSliceBuilder(t, scheme, &test))

			// Test subject
			// Only enable rediscoverWhenNotFound on k8s versions that can support it.
			err = New(rm, mgr.DownstreamRestConfig, 5, testutil.AtLeastVersion(t, 15))
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

			t.Run("creation", func(t *testing.T) {
				var obj client.Object
				testutil.Eventually(t, func() bool {
					obj, err = test.Get(downstream)
					return err == nil
				})
				test.AssertCreated(t, obj)
				test.WaitForPhase(t, downstream, "create")
			})

			if test.ApplyExternalUpdate != nil {
				t.Run("external update", func(t *testing.T) {
					err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
						obj, err := test.Get(downstream)
						require.NoError(t, err)

						updatedObj := test.ApplyExternalUpdate(t, obj)
						updatedObj = setPhase(updatedObj, "external-update")
						if err := downstream.Update(ctx, updatedObj); err != nil {
							return err
						}

						return nil
					})
					require.NoError(t, err)
					test.WaitForPhase(t, downstream, "external-update")
				})
			}

			t.Run("update", func(t *testing.T) {
				setImage(t, upstream, syn, comp, "update")
				test.WaitForPhase(t, downstream, "update")

				obj, err := test.Get(downstream)
				require.NoError(t, err)
				test.AssertUpdated(t, obj)
			})

			// TODO
			// t.Run("delete", func(t *testing.T) {
			// 	setSynImage(t, upstream, syn, "delete")

			// 	testutil.Eventually(t, func() bool {
			// 		_, err = test.Get(downstream)
			// 		return errors.IsNotFound(err)
			// 	})
			// })
		})
	}
}

func (c *crudTestCase) WaitForPhase(t *testing.T, downstream client.Client, phase string) {
	testutil.Eventually(t, func() bool {
		obj, err := c.Get(downstream)
		return err == nil && getPhase(obj) == phase
	})
}

func (c *crudTestCase) Get(downstream client.Client) (client.Object, error) {
	obj := c.Empty.DeepCopyObject().(client.Object)
	obj.SetName(c.Initial.GetName())
	obj.SetNamespace(c.Initial.GetNamespace())
	return obj, downstream.Get(context.Background(), client.ObjectKeyFromObject(obj), obj)
}

func setImage(t *testing.T, upstream client.Client, syn *apiv1.Synthesizer, comp *apiv1.Composition, image string) {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := upstream.Get(context.Background(), client.ObjectKeyFromObject(syn), syn); err != nil {
			return err
		}
		syn.Spec.Image = image
		return upstream.Update(context.Background(), syn)
	})
	require.NoError(t, err)

	// Also pin the composition to >= this synthesizer version.
	// This is necessary to avoid deadlock in cases where incoherent cache causes the composition not to be updated on this tick of the rollout loop.
	// It isn't a problem in production because we don't expect serialized behavior from the rollout controller.
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := upstream.Get(context.Background(), client.ObjectKeyFromObject(comp), comp); err != nil {
			return err
		}
		comp.Spec.Synthesizer.MinGeneration = syn.Generation
		return upstream.Update(context.Background(), comp)
	})
	require.NoError(t, err)
}

func newSliceBuilder(t *testing.T, scheme *runtime.Scheme, test *crudTestCase) func(c *apiv1.Composition, s *apiv1.Synthesizer) []*apiv1.ResourceSlice {
	return func(c *apiv1.Composition, s *apiv1.Synthesizer) []*apiv1.ResourceSlice {
		slice := &apiv1.ResourceSlice{}
		slice.GenerateName = "test-"
		slice.Namespace = "default"
		slice.Spec.CompositionGeneration = c.Generation

		var obj client.Object
		switch s.Spec.Image {
		case "create":
			obj = test.Initial.DeepCopyObject().(client.Object)
			obj = setPhase(obj, "create")
		case "update":
			obj = test.Updated.DeepCopyObject().(client.Object)
			obj = setPhase(obj, "update")
		case "delete":
			return []*apiv1.ResourceSlice{slice}
		default:
			t.Fatalf("unknown pseudo-image: %s", s.Spec.Image)
		}

		gvks, _, err := scheme.ObjectKinds(obj)
		require.NoError(t, err)
		obj.GetObjectKind().SetGroupVersionKind(gvks[0])

		js, err := json.Marshal(obj)
		require.NoError(t, err)

		slice.Spec.Resources = []apiv1.Manifest{{Manifest: string(js)}}
		return []*apiv1.ResourceSlice{slice}
	}
}

func setPhase(obj client.Object, phase string) client.Object {
	copy := obj.DeepCopyObject().(client.Object)
	anno := copy.GetAnnotations()
	if anno == nil {
		anno = map[string]string{}
	}
	anno["test-phase"] = phase
	copy.SetAnnotations(anno)
	return copy
}

func getPhase(obj client.Object) string {
	anno := obj.GetAnnotations()
	if anno == nil {
		anno = map[string]string{}
	}
	return anno["test-phase"]
}
