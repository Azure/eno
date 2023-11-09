package testutil

import (
	"context"
	"errors"
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
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/Azure/eno/api/v1"
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
	return logr.NewContext(ctx, testr.NewWithOptions(t, testr.Options{Verbosity: 99}))
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

	mgr, err := ctrl.NewManager(cfg, manager.Options{
		Logger:      testr.New(t),
		BaseContext: func() context.Context { return NewContext(t) },
	})
	require.NoError(t, err)

	err = apiv1.SchemeBuilder.AddToScheme(mgr.GetScheme())
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

// NewPodController adds a controller to the manager that simulates the behavior of a synthesis pod.
// Useful for integration testing without kcm/kubelet.
func NewPodController(t testing.TB, mgr ctrl.Manager) {
	cli := mgr.GetClient()
	podCtrl := reconcile.Func(func(ctx context.Context, r reconcile.Request) (reconcile.Result, error) {
		pod := &corev1.Pod{}
		err := cli.Get(ctx, r.NamespacedName, pod)
		if err != nil {
			return reconcile.Result{}, client.IgnoreNotFound(err)
		}
		owners := pod.OwnerReferences
		if len(owners) == 0 {
			t.Logf("got a pod that isn't owned by anything")
			return reconcile.Result{}, nil // can't be our pod (shouldn't be possible)
		}

		// Add resource slice count - the wrapper will do this in the real world
		comp := &apiv1.Composition{}
		comp.Name = owners[0].Name
		comp.Namespace = pod.Namespace
		err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if err != nil {
			return reconcile.Result{}, err
		}
		if comp.Status.CurrentState == nil {
			return reconcile.Result{}, errors.New("state hasn't been initialized")
		}
		one := int64(1)
		comp.Status.CurrentState.ResourceSliceCount = &one
		err = cli.Status().Update(ctx, comp)
		if err != nil {
			return reconcile.Result{}, err
		}
		t.Logf("updated resource slice count for %s", pod.Name)

		// Mark the pod as terminated to signal that synthesis is complete
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{
					ExitCode: 0,
				},
			},
		}}
		err = cli.Status().Update(ctx, pod)
		if err != nil {
			return reconcile.Result{}, err
		}
		t.Logf("updated container status for %s", pod.Name)

		return reconcile.Result{}, nil
	})

	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Build(podCtrl)
	require.NoError(t, err)
}
