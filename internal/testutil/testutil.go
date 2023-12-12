package testutil

import (
	"context"
	"fmt"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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
	t.Parallel()
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

// NewPodController adds a controller to the manager that simulates the behavior of a synthesis pod.
// Useful for integration testing without kcm/kubelet. Slices returned from the given function will
// be associated with the composition by this function.
func NewPodController(t testing.TB, mgr ctrl.Manager, fn func(*apiv1.Composition, *apiv1.Synthesizer) []*apiv1.ResourceSlice) {
	cli := mgr.GetClient()
	podCtrl := reconcile.Func(func(ctx context.Context, r reconcile.Request) (reconcile.Result, error) {
		comp := &apiv1.Composition{}
		err := cli.Get(ctx, r.NamespacedName, comp)
		if err != nil {
			return reconcile.Result{}, err
		}
		if comp.Status.CurrentState == nil {
			return reconcile.Result{}, nil // wait for controller to write initial status
		}

		syn := &apiv1.Synthesizer{}
		syn.Name = comp.Spec.Synthesizer.Name
		err = cli.Get(ctx, client.ObjectKeyFromObject(syn), syn)
		if err != nil {
			return reconcile.Result{}, err
		}

		var slices []*apiv1.ResourceSlice
		if fn != nil {
			slices = fn(comp, syn)
			for _, slice := range slices {
				cp := slice.DeepCopy()
				cp.Spec.CompositionGeneration = comp.Generation
				if err := controllerutil.SetControllerReference(comp, cp, cli.Scheme()); err != nil {
					return reconcile.Result{}, err
				}
				if err := cli.Create(ctx, cp); err != nil {
					return reconcile.Result{}, err
				}
			}
		}

		pods := &corev1.PodList{}
		err = cli.List(ctx, pods, client.MatchingFields{
			manager.IdxPodsByComposition: comp.Name,
		})
		if err != nil {
			return reconcile.Result{}, err
		}
		if len(pods.Items) == 0 {
			return reconcile.Result{}, nil // no pods yet
		}

		for _, pod := range pods.Items {
			pod := pod

			if pod.DeletionTimestamp != nil {
				continue // pod no longer exists
			}

			// The real pod controller will ignore outdated (probably deleting) pods
			compGen, _ := strconv.ParseInt(pod.Annotations["eno.azure.io/composition-generation"], 10, 0)
			synGen, _ := strconv.ParseInt(pod.Annotations["eno.azure.io/synthesizer-generation"], 10, 0)
			if synGen < syn.Generation || compGen < comp.Generation {
				t.Logf("skipping pod %s because it's out of date (%d < %d || %d < %d)", pod.Name, synGen, syn.Generation, compGen, comp.Generation)
				continue
			}

			// nil func == 0 slices
			var slices []*apiv1.ResourceSlice
			if fn != nil {
				slices = fn(comp, syn)
			}

			// Write all of the resource slices, update the resource slice count accordingly
			// TODO: We need a controller to remove failed/outdated resource slice writes
			// TODO: Do we have immutable validation on the CRD?
			sliceRefs := []*apiv1.ResourceSliceRef{}
			if comp.Status.CurrentState.ResourceSlices == nil {
				for _, slice := range slices {
					cp := slice.DeepCopy()
					cp.Spec.CompositionGeneration = comp.Generation
					if err := controllerutil.SetControllerReference(comp, cp, cli.Scheme()); err != nil {
						return reconcile.Result{}, err
					}
					if err := cli.Create(ctx, cp); err != nil {
						return reconcile.Result{}, err // TODO: we can't recover from this
					}
					sliceRefs = append(sliceRefs, &apiv1.ResourceSliceRef{Name: cp.Name})
					t.Logf("created resource slice: %s", cp.Name)
				}
				err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
					err := cli.Get(ctx, r.NamespacedName, comp)
					if err != nil {
						return err
					}
					comp.Status.CurrentState.ResourceSlices = sliceRefs
					comp.Status.CurrentState.Synthesized = true
					err = cli.Status().Update(ctx, comp)
					if err != nil {
						return err
					}
					t.Logf("updated resource slice refs for %s (image %s)", pod.Name, pod.Spec.Containers[0].Image)
					return nil
				})
				return reconcile.Result{}, err
			}
			t.Logf("updated resource slice count for %s", pod.Name)
			return reconcile.Result{}, nil
		}

		// Mark the pod as terminated to signal that synthesis is complete
		for _, pod := range pods.Items {
			if len(pod.Status.ContainerStatuses) == 0 {
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 0,
						},
					},
				}}
				err = cli.Status().Update(ctx, &pod)
				if err != nil {
					return reconcile.Result{}, err
				}
				t.Logf("updated container status for %s", pod.Name)
				return reconcile.Result{}, nil
			}
		}

		return reconcile.Result{}, nil
	})

	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Owns(&corev1.Pod{}).
		Build(podCtrl)
	require.NoError(t, err)
}
