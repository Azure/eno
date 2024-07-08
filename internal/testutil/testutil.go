package testutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/Azure/eno/api/v1"
	testv1 "github.com/Azure/eno/internal/controllers/reconciliation/fixtures/v1"
	"github.com/Azure/eno/internal/execution"
	"github.com/Azure/eno/internal/manager"
)

func NewClient(t testing.TB) client.Client {
	return NewClientWithInterceptors(t, nil)
}

func NewClientWithInterceptors(t testing.TB, ict *interceptor.Funcs) client.Client {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.SchemeBuilder.AddToScheme(scheme))
	require.NoError(t, corev1.SchemeBuilder.AddToScheme(scheme))

	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&apiv1.ResourceSlice{}, &apiv1.Composition{})

	if ict != nil {
		builder.WithInterceptorFuncs(*ict)
	}

	return builder.Build()
}

func NewReadOnlyClient(t testing.TB, objs ...runtime.Object) client.Client {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.SchemeBuilder.AddToScheme(scheme))
	require.NoError(t, corev1.SchemeBuilder.AddToScheme(scheme))

	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(&apiv1.ResourceSlice{}, &apiv1.Composition{})

	builder.WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			return errors.New("no writes allowed")
		},
		Update: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			return errors.New("no writes allowed")
		},
		Patch: func(ctx context.Context, client client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			return errors.New("no writes allowed")
		},
		Delete: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			return errors.New("no writes allowed")
		},
	})

	return builder.Build()
}

func NewContext(t *testing.T) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
	})
	return logr.NewContext(ctx, testr.NewWithOptions(t, testr.Options{Verbosity: 2}))
}

type TestManagerOption func(*manager.Options)

func (o TestManagerOption) apply(opts *manager.Options) {
	o(opts)
}

func WithPodNamespace(ns string) TestManagerOption {
	return TestManagerOption(func(o *manager.Options) {
		o.SynthesizerPodNamespace = ns
	})
}

func WithCompositionNamespace(ns string) TestManagerOption {
	return TestManagerOption(func(o *manager.Options) {
		o.CompositionNamespace = ns
	})
}

// NewManager starts one or two envtest environments depending on the env.
// This should work seamlessly when run locally assuming binaries have been fetched with setup-envtest.
// In CI the second environment is used to compatibility test against a matrix of k8s versions.
// This compatibility testing is tightly coupled to the github action and not expected to work locally.
func NewManager(t *testing.T, testOpts ...TestManagerOption) *Manager {
	t.Parallel()
	_, b, _, _ := goruntime.Caller(0)
	root := filepath.Join(filepath.Dir(b), "..", "..")

	testCrdDir := filepath.Join(root, "internal", "controllers", "reconciliation", "fixtures", "v1", "config", "crd")
	env := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join(root, "api", "v1", "config", "crd"),
			testCrdDir,
		},
		ErrorIfCRDPathMissing:    true,
		AttachControlPlaneOutput: os.Getenv("ACTIONS_RUNNER_DEBUG") != "" || os.Getenv("ACTIONS_STEP_DEBUG") != "",

		// We can't use KUBEBUILDER_ASSETS when also setting DOWNSTREAM_KUBEBUILDER_ASSETS
		// because the envvar overrides BinaryAssetsDirectory
		BinaryAssetsDirectory: os.Getenv("UPSTREAM_KUBEBUILDER_ASSETS"),
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
	options := &manager.Options{
		Rest:                    cfg,
		HealthProbeAddr:         "127.0.0.1:0",
		MetricsAddr:             "127.0.0.1:0",
		SynthesizerPodNamespace: "default",
		CompositionNamespace:    "default",
		CompositionSelector:     labels.Everything(),
	}
	for _, o := range testOpts {
		o.apply(options)
	}
	mgr, err := manager.NewTest(logr.FromContextOrDiscard(NewContext(t)), options)
	require.NoError(t, err)
	require.NoError(t, testv1.SchemeBuilder.AddToScheme(mgr.GetScheme())) // test-specific CRDs

	m := &Manager{
		Manager:              mgr,
		RestConfig:           cfg,
		DownstreamRestConfig: cfg, // possible override below
		DownstreamClient:     mgr.GetClient(),
		DownstreamEnv:        env,
	}

	dir := os.Getenv("DOWNSTREAM_KUBEBUILDER_ASSETS")
	if dir == "" {
		return m // only one env needed
	}
	version, _ := strconv.Atoi(os.Getenv("DOWNSTREAM_VERSION_MINOR"))

	downstreamEnv := &envtest.Environment{
		BinaryAssetsDirectory:    dir,
		ErrorIfCRDPathMissing:    true,
		AttachControlPlaneOutput: os.Getenv("ACTIONS_RUNNER_DEBUG ") != "" || os.Getenv("ACTIONS_STEP_DEBUG ") != "",
	}

	// Only newer clusters can use envtest to install CRDs
	if version >= 21 {
		t.Logf("managing downstream cluster CRD with envtest because version >= 21")
		downstreamEnv.CRDDirectoryPaths = append(downstreamEnv.CRDDirectoryPaths, testCrdDir)
	}

	// k8s <1.13 will not start if these flags are set
	if version < 13 {
		conf := downstreamEnv.ControlPlane.GetAPIServer().Configure()
		conf.Disable("service-account-signing-key-file")
		conf.Disable("service-account-issuer")
	}

	t.Cleanup(func() {
		err := downstreamEnv.Stop()
		if err != nil {
			panic(err)
		}
	})
	for i := 0; i < 2; i++ {
		m.DownstreamRestConfig, err = downstreamEnv.Start()
		if err != nil {
			t.Logf("failed to start downstream test environment: %s", err)
			continue
		}
		break
	}
	require.NoError(t, err)
	m.DownstreamEnv = downstreamEnv

	m.DownstreamClient, err = client.New(m.DownstreamRestConfig, client.Options{Scheme: mgr.GetScheme()})
	require.NoError(t, err)

	// Log apiserver version
	disc, err := discovery.NewDiscoveryClientForConfig(m.DownstreamRestConfig)
	if err == nil {
		version, err := disc.ServerVersion()
		if err == nil {
			t.Logf("downstream control plane version: %s", version.String())
		}
	}

	// We install old (v1beta1) CRDs ourselves because envtest assumes v1
	if version < 21 {
		t.Logf("managing downstream cluster CRD ourselves (not with envtest) because version < 21")
		raw, err := os.ReadFile(filepath.Join(root, "internal", "controllers", "reconciliation", "fixtures", "v1", "config", "enotest.azure.io_testresources-old.yaml"))
		require.NoError(t, err)

		res := &unstructured.Unstructured{}
		require.NoError(t, yaml.Unmarshal(raw, res))

		cli, err := client.New(m.DownstreamRestConfig, client.Options{})
		require.NoError(t, err)
		require.NoError(t, cli.Create(context.Background(), res))
	}

	return m
}

type Manager struct {
	ctrl.Manager
	RestConfig           *rest.Config
	DownstreamRestConfig *rest.Config  // may or may not == RestConfig
	DownstreamClient     client.Client // may or may not == Manager.GetClient()
	DownstreamEnv        *envtest.Environment
}

func (m *Manager) Start(t *testing.T) {
	go func() {
		err := m.Manager.Start(NewContext(t))
		if err != nil {
			// can't t.Fail here since we're in a different goroutine
			panic(fmt.Sprintf("error while starting manager: %s", err))
		}
	}()
	t.Logf("warming caches")
	m.Manager.GetCache().WaitForCacheSync(context.Background())
	t.Logf("warmed caches")
}

func (m *Manager) GetCurrentResourceSlices(ctx context.Context) ([]*apiv1.ResourceSlice, error) {
	cli := m.Manager.GetAPIReader()

	comps := &apiv1.CompositionList{}
	err := cli.List(ctx, comps)
	if err != nil {
		return nil, err
	}
	if l := len(comps.Items); l != 1 {
		return nil, fmt.Errorf("expected one composition, found %d", l)
	}
	if comps.Items[0].Status.CurrentSynthesis.Synthesized == nil {
		return nil, fmt.Errorf("composition is still being synthesized")
	}

	synthesis := comps.Items[0].Status.CurrentSynthesis
	if synthesis == nil {
		return nil, fmt.Errorf("synthesis hasn't completed yet")
	}
	returns := make([]*apiv1.ResourceSlice, len(synthesis.ResourceSlices))
	for i, ref := range synthesis.ResourceSlices {
		slice := &apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comps.Items[0].Namespace
		returns[i] = slice

		err = cli.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		if err != nil {
			return nil, err
		}
	}

	return returns, nil
}

var Backoff = wait.Backoff{
	Steps:    10,
	Duration: 10 * time.Millisecond,
	Factor:   2.0,
	Jitter:   0.1,
	Cap:      time.Minute,
}

func Eventually(t testing.TB, fn func() bool) {
	t.Helper()
	SomewhatEventually(t, time.Second*15, fn)
}

func SomewhatEventually(t testing.TB, dur time.Duration, fn func() bool) {
	t.Helper()
	start := time.Now()
	for {
		if time.Since(start) > dur {
			t.Fatalf("timeout while waiting for condition")
			return
		}
		if fn() {
			return
		}
		time.Sleep(time.Millisecond * 100)
	}
}

func AtLeastVersion(t *testing.T, minor int) bool {
	versionStr := os.Getenv("DOWNSTREAM_VERSION_MINOR")
	if versionStr == "" {
		return true // fail open for local dev
	}

	version, _ := strconv.Atoi(versionStr)
	return version >= minor
}

func WithFakeExecutor(t *testing.T, mgr *Manager, sh execution.SynthesizerHandle) {
	cli := mgr.GetAPIReader()
	podCtrl := reconcile.Func(func(ctx context.Context, r reconcile.Request) (reconcile.Result, error) {
		pod := &corev1.Pod{}
		err := cli.Get(ctx, r.NamespacedName, pod)
		if err != nil {
			return reconcile.Result{}, client.IgnoreNotFound(err)
		}
		if pod.DeletionTimestamp != nil {
			return reconcile.Result{}, nil
		}

		env := &execution.Env{}
		for _, e := range pod.Spec.Containers[0].Env {
			switch e.Name {
			case "COMPOSITION_NAME":
				env.CompositionName = e.Value
			case "COMPOSITION_NAMESPACE":
				env.CompositionNamespace = e.Value
			case "SYNTHESIS_UUID":
				env.SynthesisUUID = e.Value
			case "SYNTHESIS_ATTEMPT":
				val, _ := strconv.Atoi(e.Value)
				env.SynthesisAttempt = val
			}
		}

		e := &execution.Executor{
			Reader:  cli,
			Writer:  mgr.GetClient(),
			Handler: sh,
		}
		err = e.Synthesize(ctx, env)
		if err != nil {
			// Returning an error from the synth would eventually result in a timeout.
			// To avoid waiting that long in the tests we can just delete the pod after the first try.
			return reconcile.Result{}, mgr.GetClient().Delete(ctx, pod)
		}

		err = mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(pod), pod)
		if err != nil {
			return reconcile.Result{}, nil
		}
		pod.Status.Phase = corev1.PodSucceeded
		err = mgr.GetClient().Status().Update(ctx, pod)
		if err != nil {
			return reconcile.Result{}, nil
		}

		return reconcile.Result{}, nil
	})

	_, err := ctrl.NewControllerManagedBy(mgr.Manager).
		For(&corev1.Pod{}).
		Build(podCtrl)
	require.NoError(t, err)
}
