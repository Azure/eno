package reconciliation

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	testv1 "github.com/Azure/eno/internal/controllers/reconciliation/fixtures/v1"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/reconstitution"
	"github.com/Azure/eno/internal/testutil"
)

func init() {
	// safe for tests since they don't have any secrets
	insecureLogPatch = true
}

var defaultConf = &synthesis.Config{
	Timeout:          time.Second * 5,
	SliceCreationQPS: 20,
}

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
			require.Len(t, svc.Ports, 2)
			assert.Contains(t, svc.Ports, corev1.ServicePort{
				Name:       "third",
				Port:       3456,
				Protocol:   corev1.ProtocolTCP,
				TargetPort: intstr.FromInt(3456),
			})
			assert.Contains(t, svc.Ports, corev1.ServicePort{
				Name:       "second",
				Port:       2345,
				Protocol:   corev1.ProtocolTCP,
				TargetPort: intstr.FromInt(2345),
			})
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
			require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, defaultConf))
			require.NoError(t, synthesis.NewExecController(mgr.Manager, defaultConf, &testutil.ExecConn{Hook: newSliceBuilder(t, scheme, &test)}))

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

			t.Logf("starting creation")
			var obj client.Object
			testutil.Eventually(t, func() bool {
				obj, err = test.Get(downstream)
				return err == nil
			})
			test.AssertCreated(t, obj)
			test.WaitForPhase(t, downstream, "create")

			if test.ApplyExternalUpdate != nil {
				t.Logf("starting external update")
				err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
					obj, err := test.Get(downstream)
					require.NoError(t, err)

					updatedObj := test.ApplyExternalUpdate(t, obj.DeepCopyObject().(client.Object))
					updatedObj = setPhase(updatedObj, "external-update")
					if err := downstream.Update(ctx, updatedObj); err != nil {
						return err
					}

					return nil
				})
				require.NoError(t, err)
				test.WaitForPhase(t, downstream, "external-update")
			}

			t.Logf("starting update")
			setImage(t, upstream, syn, comp, "update")
			test.WaitForPhase(t, downstream, "update")

			obj, err = test.Get(downstream)
			require.NoError(t, err)
			test.AssertUpdated(t, obj)

			t.Logf("starting deletion")
			setImage(t, upstream, syn, comp, "delete")

			testutil.Eventually(t, func() bool {
				_, err := test.Get(downstream)
				return errors.IsNotFound(err)
			})
		})
	}
}

func (c *crudTestCase) WaitForPhase(t *testing.T, downstream client.Client, phase string) {
	var lastRV string
	testutil.Eventually(t, func() bool {
		obj, err := c.Get(downstream)
		currentPhase := getPhase(obj)
		currentRV := obj.GetResourceVersion()
		if lastRV == "" {
			t.Logf("initial resource version %s was observed in phase %s", currentRV, currentPhase)
		} else if currentRV != lastRV {
			t.Logf("resource version transitioned from %s to %s in phase %s", lastRV, currentRV, currentPhase)
		}
		lastRV = currentRV
		return err == nil && currentPhase == phase
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
}

func newSliceBuilder(t *testing.T, scheme *runtime.Scheme, test *crudTestCase) func(s *apiv1.Synthesizer) []client.Object {
	return func(s *apiv1.Synthesizer) []client.Object {
		var obj client.Object
		switch s.Spec.Image {
		case "create":
			obj = test.Initial.DeepCopyObject().(client.Object)
			obj = setPhase(obj, "create")
		case "update":
			obj = test.Updated.DeepCopyObject().(client.Object)
			obj = setPhase(obj, "update")
		case "delete":
			return []client.Object{}
		default:
			t.Fatalf("unknown pseudo-image: %s", s.Spec.Image)
		}

		gvks, _, err := scheme.ObjectKinds(obj)
		require.NoError(t, err)
		obj.GetObjectKind().SetGroupVersionKind(gvks[0])

		return []client.Object{obj}
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

func TestReconcileInterval(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	// Register supporting controllers
	rm, err := reconstitution.New(mgr.Manager, time.Millisecond)
	require.NoError(t, err)
	require.NoError(t, synthesis.NewRolloutController(mgr.Manager, time.Millisecond))
	require.NoError(t, synthesis.NewStatusController(mgr.Manager))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, defaultConf))
	require.NoError(t, synthesis.NewExecController(mgr.Manager, defaultConf, &testutil.ExecConn{Hook: func(s *apiv1.Synthesizer) []client.Object {
		obj := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-obj",
				Namespace: "default",
				Annotations: map[string]string{
					"eno.azure.io/reconcile-interval": "100ms",
				},
			},
			Data: map[string]string{"foo": "bar"},
		}

		gvks, _, err := scheme.ObjectKinds(obj)
		require.NoError(t, err)
		obj.GetObjectKind().SetGroupVersionKind(gvks[0])
		return []client.Object{obj}
	}}))

	// Test subject
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

	// Wait for resource to be created
	obj := &corev1.ConfigMap{}
	testutil.Eventually(t, func() bool {
		obj.SetName("test-obj")
		obj.SetNamespace("default")
		err = downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		return err == nil
	})

	// Update the service from outside of Eno
	obj.Data["foo"] = "baz"
	require.NoError(t, downstream.Update(ctx, obj))

	// The service should eventually converge with the desired state
	testutil.Eventually(t, func() bool {
		err = downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		return err == nil && obj.Data["foo"] == "bar"
	})
}

// TestReconcileCacheRace covers a race condition in which a work item remains in the queue after the
// corresponding (version of the) manifest has been removed from cache.
func TestReconcileCacheRace(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	// Register supporting controllers
	rm, err := reconstitution.New(mgr.Manager, time.Millisecond)
	require.NoError(t, err)
	require.NoError(t, synthesis.NewRolloutController(mgr.Manager, time.Microsecond))
	require.NoError(t, synthesis.NewStatusController(mgr.Manager))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, defaultConf))
	renderN := 0
	require.NoError(t, synthesis.NewExecController(mgr.Manager, defaultConf, &testutil.ExecConn{Hook: func(s *apiv1.Synthesizer) []client.Object {
		renderN++
		obj := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-obj",
				Namespace: "default",
				Annotations: map[string]string{
					"eno.azure.io/reconcile-interval": "50ms",
				},
			},
			Data: map[string]string{"foo": fmt.Sprintf("rendered-%d-times", renderN)},
		}

		gvks, _, err := scheme.ObjectKinds(obj)
		require.NoError(t, err)
		obj.GetObjectKind().SetGroupVersionKind(gvks[0])
		return []client.Object{obj}
	}}))

	// Test subject
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

	// Wait for resource to be created
	obj := &corev1.ConfigMap{}
	testutil.Eventually(t, func() bool {
		obj.SetName("test-obj")
		obj.SetNamespace("default")
		err = downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		return err == nil
	})

	// Update frequently, it shouldn't panic
	for i := 0; i < 20; i++ {
		err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			err = upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn)
			syn.Spec.Command = []string{fmt.Sprintf("any-unique-value-%d", i)}
			return upstream.Update(ctx, syn)
		})
		require.NoError(t, err)
		time.Sleep(time.Millisecond * 50)
	}
}

func TestReconcileStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	rm, err := reconstitution.New(mgr.Manager, time.Millisecond)
	require.NoError(t, err)

	err = New(rm, mgr.DownstreamRestConfig, 5, testutil.AtLeastVersion(t, 15))
	require.NoError(t, err)
	mgr.Start(t)

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	require.NoError(t, upstream.Create(ctx, comp))

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice"
	slice.Namespace = comp.Namespace
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, upstream.Scheme()))
	slice.Spec.Resources = []apiv1.Manifest{
		{Manifest: `{ "kind": "ConfigMap", "apiVersion": "v1", "metadata": { "name": "test", "namespace": "default" } }`},
		{Deleted: true, Manifest: `{ "kind": "ConfigMap", "apiVersion": "v1", "metadata": { "name": "test-deleted", "namespace": "default" } }`},
	}
	require.NoError(t, upstream.Create(ctx, slice))

	comp.Status.CurrentState = &apiv1.Synthesis{
		Synthesized:    true,
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
	}
	require.NoError(t, upstream.Status().Update(ctx, comp))

	// Status should eventually reflect the reconciliation state
	testutil.Eventually(t, func() bool {
		err = upstream.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		return err == nil && len(slice.Status.Resources) == 2 &&
			slice.Status.Resources[0].Reconciled && !slice.Status.Resources[0].Deleted &&
			slice.Status.Resources[1].Reconciled && slice.Status.Resources[1].Deleted
	})
}

// TestCompositionDeletionOrdering proves that compositions are not deleted until all resulting resources have been deleted.
// This covers significant surface area between reconciliation and synthesis.
func TestCompositionDeletionOrdering(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	// Register supporting controllers
	rm, err := reconstitution.New(mgr.Manager, time.Millisecond)
	require.NoError(t, err)
	require.NoError(t, synthesis.NewRolloutController(mgr.Manager, time.Millisecond))
	require.NoError(t, synthesis.NewStatusController(mgr.Manager))
	require.NoError(t, synthesis.NewSliceCleanupController(mgr.Manager))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, defaultConf))
	require.NoError(t, synthesis.NewExecController(mgr.Manager, defaultConf, &testutil.ExecConn{Hook: func(s *apiv1.Synthesizer) []client.Object {
		obj := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-obj",
				Namespace: "default",
				Annotations: map[string]string{
					"eno.azure.io/reconcile-interval": "100ms",
				},
			},
			Data: map[string]string{"foo": "bar"},
		}

		gvks, _, err := scheme.ObjectKinds(obj)
		require.NoError(t, err)
		obj.GetObjectKind().SetGroupVersionKind(gvks[0])
		return []client.Object{obj}
	}}))

	// Test subject
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

	// Wait for resource to be created
	obj := &corev1.ConfigMap{}
	testutil.Eventually(t, func() bool {
		obj.SetName("test-obj")
		obj.SetNamespace("default")
		err = downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		return err == nil
	})

	// Delete the composition
	require.NoError(t, upstream.Delete(ctx, comp))
	t.Logf("deleted composition")

	// Everything should eventually be cleaned up
	// This implicitly covers ordering, since it's impossible to delete a resource without its composition
	testutil.Eventually(t, func() bool {
		resourceGone := errors.IsNotFound(downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj))
		compGone := errors.IsNotFound(upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp))
		t.Logf("resourceGone=%t compGone=%t", resourceGone, compGone)
		return resourceGone && compGone
	})
}
