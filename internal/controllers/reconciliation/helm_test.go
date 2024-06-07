package reconciliation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/aggregation"
	testv1 "github.com/Azure/eno/internal/controllers/reconciliation/fixtures/v1"
	"github.com/Azure/eno/internal/controllers/rollout"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestHelmOwnershipTransfer proves that Helm resource ownership can be transferred to Eno, and back.
// This is accomplished by setting the "helm.sh/resource-policy: keep" annotation on Helm resources.
// Sadly this has to be done out-of-band to Eno since Helm reads the annotation from its release state, not the resource itself.
func TestHelmOwnershipTransfer(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	// Get a kubeconfig for the Helm CLI
	u, err := mgr.DownstreamEnv.AddUser(envtest.User{Name: "helm", Groups: []string{"system:masters"}}, nil)
	require.NoError(t, err)
	kc, err := u.KubeConfig()
	require.NoError(t, err)
	kubeconfigPath := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfigPath, kc, 0600))

	// Register supporting controllers
	require.NoError(t, rollout.NewController(mgr.Manager, time.Millisecond))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, defaultConf))
	require.NoError(t, aggregation.NewSliceController(mgr.Manager))
	require.NoError(t, synthesis.NewSliceCleanupController(mgr.Manager))
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
				},
				"data": map[string]string{"foo": "bar"},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)

	// Install Helm release to initially create the resource
	cmd := exec.Command("helm", "--kubeconfig", kubeconfigPath, "install", "foo", "./fixtures/helmchart")
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	require.NoError(t, cmd.Run())

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "bar"
	require.NoError(t, upstream.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	comp.Annotations = map[string]string{"eno.azure.io/deletion-strategy": "orphan"}
	require.NoError(t, upstream.Create(ctx, comp))

	// Wait for Eno to reconcile the resource (should be a no-op)
	obj := &corev1.ConfigMap{}
	var initialCreateTime time.Time
	testutil.Eventually(t, func() bool {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.ObservedCompositionGeneration != comp.Generation || comp.Status.CurrentSynthesis.Reconciled == nil {
			return false
		}

		obj.SetName("test-obj")
		obj.SetNamespace("default")
		err = downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		initialCreateTime = obj.CreationTimestamp.Time
		return err == nil
	})

	// Uninstall Helm release
	cmd = exec.Command("helm", "--kubeconfig", kubeconfigPath, "uninstall", "foo")
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	require.NoError(t, cmd.Run())

	// The resource shouldn't have been deleted by Helm
	obj.SetName("test-obj")
	obj.SetNamespace("default")
	err = downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
	require.NoError(t, err)

	// Delete the composition
	t.Log("deleting composition")
	require.NoError(t, upstream.Delete(ctx, comp))

	// Wait for the composition to be sync'd - it shouldn't delete the resource
	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	})
	obj.SetName("test-obj")
	obj.SetNamespace("default")
	err = downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
	require.NoError(t, err)

	// Re-install the Helm release and confirm the resource was never deleted
	cmd = exec.Command("helm", "--kubeconfig", kubeconfigPath, "install", "foo", "./fixtures/helmchart")
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	require.NoError(t, cmd.Run())

	obj.SetName("test-obj")
	obj.SetNamespace("default")
	err = downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
	require.NoError(t, err)

	assert.True(t, obj.CreationTimestamp.Time.Round(time.Second).Equal(initialCreateTime.Round(time.Second)))
	assert.Equal(t, map[string]string{
		"helm.sh/resource-policy":        "keep",
		"meta.helm.sh/release-name":      "foo",
		"meta.helm.sh/release-namespace": "default",
	}, obj.GetAnnotations())
	assert.Equal(t, map[string]string{
		"app.kubernetes.io/managed-by": "Helm",
	}, obj.GetLabels())
}
