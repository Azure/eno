package manager

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestManagerBasics(t *testing.T) {
	t.Parallel()
	_, b, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(b), "..", "..")

	testCrdDir := filepath.Join(root, "internal", "controllers", "reconciliation", "fixtures", "v1", "config", "crd")
	env := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join(root, "api", "v1", "config", "crd"),
			testCrdDir,
		},
		ErrorIfCRDPathMissing: true,
	}
	t.Cleanup(func() {
		err := env.Stop()
		if err != nil {
			panic(err)
		}
	})
	var cfg *rest.Config
	var err error
	for i := 0; i < 2; i++ {
		cfg, err = env.Start()
		if err != nil {
			t.Logf("failed to start test environment: %s", err)
			continue
		}
		break
	}
	require.NoError(t, err)

	opts := &Options{
		Rest:                    cfg,
		HealthProbeAddr:         ":0",
		MetricsAddr:             ":0",
		SynthesizerPodNamespace: "default",
		qps:                     100,
	}
	mgr, err := NewTest(testr.New(t), opts)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Start(ctx)

	// Prove it only caches synthesizer pods
	t.Run("pod cache filtering", func(t *testing.T) {
		ns := &corev1.Namespace{}
		ns.Name = "another-ns"
		err = mgr.GetClient().Create(ctx, ns)
		require.NoError(t, err)

		pod1 := &corev1.Pod{}
		pod1.Name = "test-pod-1" // wrong namespace
		pod1.Namespace = ns.Name
		pod1.Labels = map[string]string{ManagerLabelKey: ManagerLabelValue}
		pod1.Spec.Containers = []corev1.Container{{Name: "anything", Image: "anything"}}
		err = mgr.GetClient().Create(ctx, pod1)
		require.NoError(t, err)

		pod2 := &corev1.Pod{}
		pod2.Name = "test-pod-2" // missing labels
		pod2.Namespace = opts.SynthesizerPodNamespace
		pod2.Spec.Containers = []corev1.Container{{Name: "anything", Image: "anything"}}
		err = mgr.GetClient().Create(ctx, pod2)
		require.NoError(t, err)

		pod3 := &corev1.Pod{}
		pod3.Name = "test-pod-3" // should exist
		pod3.Namespace = opts.SynthesizerPodNamespace
		pod3.Labels = map[string]string{ManagerLabelKey: ManagerLabelValue}
		pod3.Spec.Containers = []corev1.Container{{Name: "anything", Image: "anything"}}
		err = mgr.GetClient().Create(ctx, pod3)
		require.NoError(t, err)

		for i := 0; true; i++ {
			// This pod should eventually exist - it's in the right namespace
			actual := &corev1.Pod{}
			mgr.GetCache().Get(ctx, client.ObjectKeyFromObject(pod3), actual)
			if actual.ResourceVersion != "" {
				break
			}

			// importing testutil would cause a cycle
			if i > 50 {
				t.Fatalf("timeout")
			}
			time.Sleep(time.Millisecond * 50)
		}

		// Because informers are ordered per-resource, this would exist in cache by now
		assert.EqualError(t, mgr.GetCache().Get(ctx, client.ObjectKeyFromObject(pod1), &corev1.Pod{}), "unable to get: another-ns/test-pod-1 because of unknown namespace for the cache")
		assert.True(t, errors.IsNotFound(mgr.GetCache().Get(ctx, client.ObjectKeyFromObject(pod2), &corev1.Pod{})))
	})

	// Prove it removes the manifest strings from resource slices
	t.Run("resuorce slice pruning", func(t *testing.T) {
		slice := &apiv1.ResourceSlice{}
		slice.Name = "test-slice"
		slice.Namespace = "default"
		slice.Spec.Resources = []apiv1.Manifest{{
			Manifest: "foo",
		}}
		err = mgr.GetClient().Create(ctx, slice)
		require.NoError(t, err)

		for i := 0; true; i++ {
			// This pod should eventually exist - it's in the right namespace
			actual := &apiv1.ResourceSlice{}
			err = mgr.GetCache().Get(ctx, client.ObjectKeyFromObject(slice), actual)
			if actual.ResourceVersion != "" {
				assert.Empty(t, actual.Spec.Resources[0].Manifest)
				break
			}

			// importing testutil would cause a cycle
			if i > 50 {
				t.Fatalf("timeout")
			}
			time.Sleep(time.Millisecond * 50)
		}
	})
}
