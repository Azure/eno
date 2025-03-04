package reconciliation

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	v1 "github.com/Azure/eno/api/v1"
	testv1 "github.com/Azure/eno/internal/controllers/reconciliation/fixtures/v1"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/execution"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
)

func init() {
	// safe for tests since they don't have any secrets
	insecureLogPatch = true
}

var defaultConf = &synthesis.Config{
	PodNamespace:  "default",
	ExecutorImage: "test-image",
}

type crudTestCase struct {
	Name                         string
	ShouldSkipDelete             bool
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
			Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{}}},
		},
		AssertCreated: func(t *testing.T, obj client.Object) {
			svc := obj.(*corev1.Service)
			assert.Equal(t, []corev1.ServicePort{{
				Name:       "first",
				Port:       1234,
				Protocol:   corev1.ProtocolTCP,
				TargetPort: intstr.FromInt(1234),
			}}, svc.Spec.Ports)
			assert.Nil(t, svc.Status.LoadBalancer.Ingress)
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
	{
		Name:             "patch",
		ShouldSkipDelete: true,
		Empty:            &corev1.Service{},
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
		Updated: &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "eno.azure.io/v1",
				"kind":       "Patch",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
				},
				"patch": map[string]any{
					"apiVersion": "v1",
					"kind":       "Service",
					"ops": []map[string]any{
						{"op": "replace", "path": "/spec/ports/0/port", "value": 2345},
						{"op": "add", "path": "/metadata/annotations/test-phase", "value": "update"},
					},
				},
			},
		},
		AssertUpdated: func(t *testing.T, obj client.Object) {
			svc := obj.(*corev1.Service).Spec
			require.Len(t, svc.Ports, 1)
			assert.Contains(t, svc.Ports, corev1.ServicePort{
				Name:       "first",
				Port:       2345,
				Protocol:   corev1.ProtocolTCP,
				TargetPort: intstr.FromInt(1234),
			})
		},
	},
}

// TestCRUD covers the entire synthesis and reconciliation flow for a set of resources,
// covering creation, reads, updates, and deletion.
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

			registerControllers(t, mgr)
			testutil.WithFakeExecutor(t, mgr, newSliceBuilder(t, scheme, &test))

			// Test subject
			// Only enable rediscoverWhenNotFound on k8s versions that can support it.
			setupTestSubject(t, mgr)
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
			var err error
			testutil.Eventually(t, func() bool {
				obj, err = test.Get(downstream)
				return err == nil
			})
			test.AssertCreated(t, obj)
			test.WaitForPhase(t, downstream, "create")

			if test.ApplyExternalUpdate != nil {
				t.Logf("starting external update")
				err := retry.RetryOnConflict(testutil.Backoff, func() error {
					obj, err := test.Get(downstream)
					require.NoError(t, err)

					updatedObj := test.ApplyExternalUpdate(t, obj.DeepCopyObject().(client.Object))
					setPhase(updatedObj, "external-update")
					if err := downstream.Update(ctx, updatedObj); err != nil {
						return err
					}

					return nil
				})
				require.NoError(t, err)
				test.WaitForPhase(t, downstream, "external-update")
			}

			t.Logf("starting update")
			setImage(t, upstream, syn, "update")
			test.WaitForPhase(t, downstream, "update")

			obj, err = test.Get(downstream)
			require.NoError(t, err)
			test.AssertUpdated(t, obj)

			t.Logf("starting deletion")
			setImage(t, upstream, syn, "delete")

			if test.ShouldSkipDelete {
				return
			}

			testutil.Eventually(t, func() bool {
				_, err := test.Get(downstream)
				return errors.IsNotFound(err)
			})
		})
	}
}

func (c *crudTestCase) WaitForPhase(t *testing.T, downstream client.Client, phase string) {
	t.Logf("waiting for phase %q", phase)
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

func setImage(t *testing.T, upstream client.Client, syn *apiv1.Synthesizer, image string) {
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		if err := upstream.Get(context.Background(), client.ObjectKeyFromObject(syn), syn); err != nil {
			return err
		}
		syn.Spec.Image = image
		return upstream.Update(context.Background(), syn)
	})
	require.NoError(t, err)
}

func newSliceBuilder(t *testing.T, scheme *runtime.Scheme, test *crudTestCase) execution.SynthesizerHandle {
	return func(ctx context.Context, s *apiv1.Synthesizer, rl *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}

		var obj client.Object
		switch s.Spec.Image {
		case "create":
			obj = test.Initial
			setPhase(obj, "create")
		case "update":
			obj = test.Updated
			setPhase(obj, "update")
		case "delete":
			return output, nil
		default:
			t.Fatalf("unknown pseudo-image: %s", s.Spec.Image)
		}

		gvks, _, err := scheme.ObjectKinds(obj)
		require.NoError(t, err)
		obj.GetObjectKind().SetGroupVersionKind(gvks[0])

		uobj := &unstructured.Unstructured{}
		raw, _ := json.Marshal(obj)
		json.Unmarshal(raw, uobj)

		output.Items = append(output.Items, uobj)
		return output, nil
	}
}

func setPhase(obj client.Object, phase string) {
	anno := obj.GetAnnotations()
	if anno == nil {
		anno = map[string]string{}
	}
	anno["test-phase"] = phase
	obj.SetAnnotations(anno)
}

func getPhase(obj client.Object) string {
	anno := obj.GetAnnotations()
	if anno == nil {
		anno = map[string]string{}
	}
	return anno["test-phase"]
}

// TestReconcileInterval proves that resources that specify a reconcile interval eventually converge
// when modified from outside of Eno.
func TestReconcileInterval(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
					"annotations": map[string]string{
						"eno.azure.io/reconcile-interval": "100ms",
					},
				},
				"data": map[string]string{"foo": "bar"},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	writeGenericComposition(t, upstream)

	// Wait for resource to be created
	obj := &corev1.ConfigMap{}
	testutil.Eventually(t, func() bool {
		obj.SetName("test-obj")
		obj.SetNamespace("default")
		err := downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		return err == nil
	})

	// Update the object from outside of Eno
	obj.Data["foo"] = "baz"
	require.NoError(t, downstream.Update(ctx, obj))

	// The object should eventually converge with the desired state
	testutil.Eventually(t, func() bool {
		err := downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
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

	registerControllers(t, mgr)
	renderN := 0
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
					"annotations": map[string]string{
						"eno.azure.io/reconcile-interval": "50ms",
					},
				},
				"data": map[string]string{"foo": fmt.Sprintf("rendered-%d-times", renderN)},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, _ := writeGenericComposition(t, upstream)

	// Wait for resource to be created
	obj := &corev1.ConfigMap{}
	testutil.Eventually(t, func() bool {
		obj.SetName("test-obj")
		obj.SetNamespace("default")
		err := downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		return err == nil
	})

	// Update frequently, it shouldn't panic
	for i := 0; i < 20; i++ {
		err := retry.RetryOnConflict(testutil.Backoff, func() error {
			upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn)
			syn.Spec.Command = []string{fmt.Sprintf("any-unique-value-%d", i)}
			return upstream.Update(ctx, syn)
		})
		require.NoError(t, err)
		time.Sleep(time.Millisecond * 50)
	}
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

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
					"annotations": map[string]string{
						"eno.azure.io/reconcile-interval": "100ms",
					},
				},
				"data": map[string]string{"foo": "bar"},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)

	// Wait for resource to be created
	obj := &corev1.ConfigMap{}
	testutil.Eventually(t, func() bool {
		obj.SetName("test-obj")
		obj.SetNamespace("default")
		err := downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
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

// TestMidSynthesisDeletion proves that compositions can be deleted while they are being synthesized.
func TestMidSynthesisDeletion(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	registerControllers(t, mgr)

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "test"
	require.NoError(t, upstream.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, upstream.Create(ctx, comp))
	comp.ResourceVersion = "1" // forget this version so the next write guarantees sync'd informer

	rs := &apiv1.ResourceSlice{}
	rs.GenerateName = "test-"
	rs.Namespace = "default"
	rs.Finalizers = []string{"eno.azure.io/cleanup"}
	rs.Spec.CompositionGeneration = comp.Generation
	rs.Spec.Resources = []apiv1.Manifest{{
		Manifest: `{ "apiVersion": "v1", "kind": "ConfigMap", "metadata": { "name": "test-obj", "namespace": "default" }}`,
	}}
	controllerutil.SetControllerReference(comp, rs, mgr.GetScheme())
	require.NoError(t, upstream.Create(ctx, rs))

	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		now := metav1.Now()
		comp.Status.CurrentSynthesis = &apiv1.Synthesis{
			UUID:                          uuid.NewString(),
			ObservedCompositionGeneration: comp.Generation,
			ObservedSynthesizerGeneration: syn.Generation,
			Synthesized:                   &now,
			ResourceSlices:                []*apiv1.ResourceSliceRef{{Name: rs.Name}},
		}
		return upstream.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	// Wait for resource to be created
	obj := &corev1.ConfigMap{}
	testutil.Eventually(t, func() bool {
		obj.SetName("test-obj")
		obj.SetNamespace("default")
		err = downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		return err == nil
	})

	// Start re-synthesizing
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Spec.Synthesizer.Name = "anything"
		return upstream.Update(ctx, comp)
	})
	require.NoError(t, err)

	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Spec.Synthesizer.Name = syn.Name
		return upstream.Update(ctx, comp)
	})
	require.NoError(t, err)

	// Wait for the state to be swapped
	testutil.Eventually(t, func() bool {
		err = upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.PendingSynthesis != nil
	})

	// Delete the composition
	require.NoError(t, upstream.Delete(ctx, comp))
	t.Logf("deleted composition")

	// Everything should eventually be cleaned up
	testutil.Eventually(t, func() bool {
		resourceGone := errors.IsNotFound(downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj))
		compGone := errors.IsNotFound(upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp))
		t.Logf("resourceGone=%t compGone=%t", resourceGone, compGone)
		return resourceGone && compGone
	})
}

// TestDisableUpdates proves that resources which set the disable-updates annotation are not updated.
func TestDisableUpdates(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	// Register supporting controllers
	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
					"annotations": map[string]string{
						"eno.azure.io/reconcile-interval": "10ms",
						"eno.azure.io/disable-updates":    "true",
					},
				},
				"data": map[string]string{"foo": "bar"},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	writeGenericComposition(t, upstream)

	// Wait for resource to be created
	obj := &corev1.ConfigMap{}
	testutil.Eventually(t, func() bool {
		obj.SetName("test-obj")
		obj.SetNamespace("default")
		err := downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		return err == nil
	})

	// Update the service from outside of Eno
	obj.Data["foo"] = "baz"
	require.NoError(t, downstream.Update(ctx, obj))

	// The service should not be updated
	time.Sleep(time.Millisecond * 100)
	err := downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
	require.NoError(t, err)
	assert.Equal(t, "baz", obj.Data["foo"])
}

func TestOrphanedResources(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
					"annotations": map[string]string{
						"eno.azure.io/reconcile-interval": "100ms",
					},
				},
				"data": map[string]string{"foo": "bar"},
			},
		}}
		return output, nil
	})

	setupTestSubject(t, mgr)
	mgr.Start(t)
	_, comp := writeComposition(t, upstream, true)

	// Wait for resource to be created.
	obj := &corev1.ConfigMap{}
	testutil.Eventually(t, func() bool {
		obj.SetName("test-obj")
		obj.SetNamespace("default")
		err := downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		return err == nil
	})

	// Delete the composition.
	require.NoError(t, upstream.Delete(ctx, comp))
	t.Logf("deleted composition")
	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	})

	// Ensure that the slice was deleted before checking the actual resource.
	testutil.Eventually(t, func() bool {
		rll := &v1.ResourceSliceList{}
		err := upstream.List(ctx, rll, client.InNamespace(metav1.NamespaceAll))
		if err != nil {
			return false
		}
		return len(rll.Items) == 0
	})

	// The resource should be orphaned after the composition is gone.
	err := downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
	require.NoError(t, err)

}

// TestResourceDefaulting proves that resources which define properties equal to the field's default will eventually converge.
func TestResourceDefaulting(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
				},
				"spec": map[string]any{
					"paused": false, // will fail the test if defaulting isn't handled correctly
					"selector": map[string]any{
						"matchLabels": map[string]any{
							"foo": "bar",
						},
					},
					"template": map[string]any{
						"metadata": map[string]any{
							"labels": map[string]any{
								"foo": "bar",
							},
						},
						"spec": map[string]any{
							"containers": []any{
								map[string]any{
									"name":  "foo",
									"image": "bar",
									"resources": map[string]any{
										"limits": map[string]any{
											"memory": "1024Mi", // apiserver will return this as "1Gi"
										},
									},
								},
							},
						},
					},
				},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)

	// It should be able to become ready
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedCompositionGeneration == comp.Generation
	})
}
