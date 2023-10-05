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

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/generation"
	testapi "github.com/Azure/eno/internal/integration/api"
)

//go:embed api/config/crd/example.azure.io_examples.yaml
var crdYaml []byte

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
					assert.Equal(t, map[string]string{"bar": "baz"}, cm.Data)
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
}

type state struct {
	Generate generation.GenerateFn
	Verify   func(*testing.T, client.Client)
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
