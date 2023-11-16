package testutil

import (
	"context"
	"fmt"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	apiv1 "github.com/Azure/eno/api/v1"
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
		WithStatusSubresource(&apiv1.ResourceSlice{})

	if ict != nil {
		builder.WithInterceptorFuncs(*ict)
	}

	return builder.Build()
}

func NewContext(t *testing.T) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
	})
	return logr.NewContext(ctx, testr.NewWithOptions(t, testr.Options{Verbosity: 2}))
}

func NewManager(t *testing.T) *Manager {
	_, b, _, _ := goruntime.Caller(0)
	root := filepath.Join(filepath.Dir(b), "..", "..")

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(root, "api", "v1", "config", "crd")},
		ErrorIfCRDPathMissing: true,
	}
	t.Cleanup(func() {
		err := env.Stop()
		if err != nil {
			panic(err)
		}
	})

	cfg, err := env.Start()
	require.NoError(t, err)

	mgr, err := manager.New(logr.FromContextOrDiscard(NewContext(t)), &manager.Options{
		Rest:            cfg,
		HealthProbeAddr: "127.0.0.1:0",
		MetricsAddr:     "127.0.0.1:0",
	})
	require.NoError(t, err)

	return &Manager{
		Manager: mgr,
	}
}

type Manager struct {
	ctrl.Manager
}

func (m *Manager) Start(t *testing.T) {
	go func() {
		err := m.Manager.Start(NewContext(t))
		if err != nil {
			// can't t.Fail here since we're in a different goroutine
			panic(fmt.Sprintf("error while starting manager: %s", err))
		}
	}()
}

func Eventually(t testing.TB, fn func() bool) {
	t.Helper()
	start := time.Now()
	for {
		if time.Since(start) > time.Second*2 {
			t.Fatalf("timeout while waiting for condition")
			return
		}
		if fn() {
			return
		}
		time.Sleep(time.Millisecond * 10)
	}
}
