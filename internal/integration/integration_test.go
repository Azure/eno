// Integration contains simple integration tests to cover the various components (controllers, wrapper process, generation framework, etc.)
// This is accomplished by replacing the wrapper and generator processes with in-process shims to the relevant code.
// Don't add too many tests here — most functionality can be covered in controller-scoped tests.
package integration

import (
	"context"
	_ "embed"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/generation"
	testapi "github.com/Azure/eno/internal/integration/api"
)

//go:embed api/config/crd/example.azure.io_examples.yaml
var crdYaml []byte

type state struct {
	Generate generation.GenerateFn
	Verify   func(*testing.T, client.Client)
}

var testCases = []struct {
	Name   string
	Inputs []client.Object
	States []*state
}{
	{
		Name: "crud-single-configmap",
		States: []*state{
			{
				Generate: func(i *generation.Inputs) ([]client.Object, error) {
					cm := &corev1.ConfigMap{}
					cm.Name = "test-configmap"
					cm.Namespace = "default"
					cm.Data = map[string]string{"foo": "bar"}

					return []client.Object{cm}, nil
				},
				Verify: func(t *testing.T, c client.Client) {
					cm := &corev1.ConfigMap{}
					cm.Name = "test-configmap"
					cm.Namespace = "default"
					err := c.Get(context.Background(), client.ObjectKeyFromObject(cm), cm)
					require.NoError(t, err)
					assert.Equal(t, map[string]string{"foo": "bar"}, cm.Data)
				},
			},
			{
				Generate: func(i *generation.Inputs) ([]client.Object, error) {
					cm := &corev1.ConfigMap{}
					cm.Name = "test-configmap"
					cm.Namespace = "default"
					cm.Data = map[string]string{"bar": "baz"}

					return []client.Object{cm}, nil
				},
				Verify: func(t *testing.T, c client.Client) {
					cm := &corev1.ConfigMap{}
					cm.Name = "test-configmap"
					cm.Namespace = "default"
					err := c.Get(context.Background(), client.ObjectKeyFromObject(cm), cm)
					require.NoError(t, err)
					assert.Equal(t, map[string]string{"foo": "bar", "bar": "baz"}, cm.Data)
				},
			},
			{
				Generate: func(i *generation.Inputs) ([]client.Object, error) {
					return []client.Object{}, nil
				},
				Verify: func(t *testing.T, c client.Client) {
					cm := &corev1.ConfigMap{}
					cm.Name = "test-configmap"
					cm.Namespace = "default"
					err := c.Get(context.Background(), client.ObjectKeyFromObject(cm), cm)
					assert.True(t, errors.IsNotFound(err) || (cm != nil && cm.DeletionTimestamp != nil))
				},
			},
		},
	},
	{
		Name: "delete-when-resource-exists",
		States: []*state{
			{
				Generate: func(i *generation.Inputs) ([]client.Object, error) {
					cm := &corev1.ConfigMap{}
					cm.Name = "test-configmap"
					cm.Namespace = "default"
					cm.Data = map[string]string{"foo": "bar"}

					return []client.Object{cm}, nil
				},
			},
			{
				Generate: func(i *generation.Inputs) ([]client.Object, error) {
					return []client.Object{}, nil
				},
				Verify: func(t *testing.T, c client.Client) {
					cm := &corev1.ConfigMap{}
					cm.Name = "test-configmap"
					cm.Namespace = "default"
					err := c.Get(context.Background(), client.ObjectKeyFromObject(cm), cm)
					assert.True(t, errors.IsNotFound(err) || (cm != nil && cm.DeletionTimestamp != nil))
				},
			},
		},
	},
	{
		Name: "crud-crd-and-cr",
		States: []*state{
			{
				Generate: generation.WithStaticManifest(crdYaml,
					func(i *generation.Inputs) ([]client.Object, error) {
						cr := &testapi.Example{}
						cr.Name = "test-cr"
						cr.Spec.Value = 123
						return []client.Object{cr}, nil
					}),
				Verify: func(t *testing.T, c client.Client) {
					cr := &testapi.Example{}
					cr.Name = "test-cr"
					err := c.Get(context.Background(), client.ObjectKeyFromObject(cr), cr)
					require.NoError(t, err)
					assert.Equal(t, 123, cr.Spec.Value)
				},
			},
			{
				Generate: generation.WithStaticManifest(crdYaml,
					func(i *generation.Inputs) ([]client.Object, error) {
						cr := &testapi.Example{}
						cr.Name = "test-cr"
						cr.Spec.Value = 234
						return []client.Object{cr}, nil
					}),
				Verify: func(t *testing.T, c client.Client) {
					cr := &testapi.Example{}
					cr.Name = "test-cr"
					err := c.Get(context.Background(), client.ObjectKeyFromObject(cr), cr)
					require.NoError(t, err)
					assert.Equal(t, 234, cr.Spec.Value)
				},
			},
		},
	},
	{
		Name: "unmanaged-property-configmap",
		States: (&mergeTest[*corev1.ConfigMap]{
			New: func() *corev1.ConfigMap {
				return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "test-configmap", Namespace: "default"}}
			},
			SetPropertyUnderTest: func(i int, obj *corev1.ConfigMap) {
				obj.Data = map[string]string{"testval": fmt.Sprintf("value-%d", i)}
			},
			GetPropertyUnderTest: func(obj *corev1.ConfigMap) any { return obj.Data },
		}).Build(),
	},
	{
		Name: "unmanaged-label-configmap",
		States: (&mergeTest[*corev1.ConfigMap]{
			New: func() *corev1.ConfigMap {
				return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "test-configmap", Namespace: "default"}}
			},
			SetPropertyUnderTest: func(i int, obj *corev1.ConfigMap) {
				if obj.Labels == nil {
					obj.Labels = map[string]string{}
				}
				obj.Labels["val"] = fmt.Sprintf("value-%d", i)
			},
			GetPropertyUnderTest: func(obj *corev1.ConfigMap) any { return obj.Labels["val"] },
		}).Build(),
	},
	{
		Name: "unmanaged-annotation-configmap",
		States: (&mergeTest[*corev1.ConfigMap]{
			New: func() *corev1.ConfigMap {
				return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "test-configmap", Namespace: "default"}}
			},
			SetPropertyUnderTest: func(i int, obj *corev1.ConfigMap) {
				if obj.Annotations == nil {
					obj.Annotations = map[string]string{}
				}
				obj.Annotations["val"] = fmt.Sprintf("value-%d", i)
			},
			GetPropertyUnderTest: func(obj *corev1.ConfigMap) any { return obj.Annotations["val"] },
		}).Build(),
	},
	{
		Name: "unmanaged-property-cr",
		States: (&mergeTest[*testapi.Example]{
			New: func() *testapi.Example {
				return &testapi.Example{ObjectMeta: metav1.ObjectMeta{Name: "test-cr"}}
			},
			SetPropertyUnderTest: func(i int, obj *testapi.Example) {
				obj.Spec.Value = i
			},
			GetPropertyUnderTest: func(obj *testapi.Example) any { return obj.Spec.Value },
		}).Build(),
	},
}

type mergeTest[T client.Object] struct {
	New                  func() T
	SetPropertyUnderTest func(i int, obj T)
	GetPropertyUnderTest func(obj T) any
}

func (m *mergeTest[T]) Build() []*state {
	return []*state{
		{
			Generate: generation.WithStaticManifest(crdYaml,
				func(i *generation.Inputs) ([]client.Object, error) {
					obj := m.New()
					return []client.Object{obj}, nil
				}),
			Verify: func(t *testing.T, c client.Client) {
				obj := m.New()

				_, err := controllerutil.CreateOrUpdate(context.Background(), c, obj, func() error {
					m.SetPropertyUnderTest(1, obj)
					return nil
				})
				require.NoError(t, err)
			},
		},
		{
			Generate: generation.WithStaticManifest(crdYaml,
				func(i *generation.Inputs) ([]client.Object, error) {
					obj := m.New()
					return []client.Object{obj}, nil
				}),
			Verify: func(t *testing.T, c client.Client) {
				obj := m.New()
				err := c.Get(context.Background(), client.ObjectKeyFromObject(obj), obj)
				require.NoError(t, err)

				expected := m.New()
				m.SetPropertyUnderTest(1, expected)
				assert.Equal(t, m.GetPropertyUnderTest(expected), m.GetPropertyUnderTest(obj))
			},
		},
		{
			Generate: generation.WithStaticManifest(crdYaml,
				func(i *generation.Inputs) ([]client.Object, error) {
					obj := m.New()
					m.SetPropertyUnderTest(2, obj)
					return []client.Object{obj}, nil
				}),
			Verify: func(t *testing.T, c client.Client) {
				obj := m.New()
				err := c.Get(context.Background(), client.ObjectKeyFromObject(obj), obj)
				require.NoError(t, err)

				expected := m.New()
				m.SetPropertyUnderTest(2, expected)
				assert.Equal(t, m.GetPropertyUnderTest(expected), m.GetPropertyUnderTest(obj))
			},
		},
	}
}

func TestTable(t *testing.T) {
	mgr := setup(t)

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			for i, state := range tc.States {
				mgr.GetLogger().Info("starting table test segment", "name", tc.Name, "state", i)

				image := fmt.Sprintf("%s-%d", tc.Name, i)
				mgr.AddJobHandler(image, compose(t, mgr, tc.Name, state.Generate))

				wait := mgr.WaitForCondition(t, tc.Name, apiv1.ReconciledConditionType, metav1.ConditionTrue)
				syncTestComposition(t, mgr, tc.Name, image)
				<-wait

				if state.Verify != nil {
					state.Verify(t, mgr.GetClient())
				}
			}

			mgr.GetLogger().Info("cleaning up table test segment", "name", tc.Name)
			wait := mgr.WaitForDeletion(t, tc.Name)
			deleteTestComposition(t, mgr, tc.Name)
			<-wait
		})
	}
}
