package integration

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/composition"
	"github.com/Azure/eno/conf"
	"github.com/Azure/eno/internal/clientmgr"
	"github.com/Azure/eno/internal/controllers"
	"github.com/Azure/eno/internal/wrapper"
)

func TestSimple(t *testing.T) {
	mgr := setup(t)

	comp := &apiv1.Composition{}
	comp.Name = "test-1"
	comp.Spec.Generator = &apiv1.Generator{
		Image: "test-generator-1",
	}

	mgr.AddJobHandler("test-generator-1", compose(t, mgr, comp,
		func(i *composition.Inputs) ([]client.Object, error) {
			cm := &corev1.ConfigMap{}
			cm.Name = "test-configmap"
			cm.Data = map[string]string{"foo": "bar"}

			return []client.Object{cm}, nil
		}))

	wait := mgr.WaitForCondition(t, comp.Name, apiv1.ReconciledConditionType, metav1.ConditionTrue)

	err := mgr.GetClient().Create(context.Background(), comp)
	require.NoError(t, err)

	<-wait
}

func setup(t *testing.T) *testManager {
	env := &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "api", "v1", "config", "crd")},
	}
	t.Cleanup(func() {
		err := env.Stop()
		if err != nil {
			panic(err)
		}
	})

	cfg, err := env.Start()
	require.NoError(t, err)

	zapLog, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}

	mgr, err := ctrl.NewManager(cfg, manager.Options{
		Logger: zapr.NewLogger(zapLog),
	})
	require.NoError(t, err)

	err = apiv1.SchemeBuilder.AddToScheme(mgr.GetScheme())
	require.NoError(t, err)

	config := &conf.Config{
		WrapperImage:          "fake-wrapper-image",
		JobTimeout:            time.Second * 10,
		JobTTL:                time.Minute,
		JobNS:                 "default",
		StatusPollingInterval: time.Millisecond * 100,
	}

	cmgr := clientmgr.New(mgr.GetClient(), func(ctx context.Context, key string) (*rest.Config, error) {
		return nil, nil
	})

	err = controllers.New(mgr, cmgr, config)
	require.NoError(t, err)

	jr := &jobRunner{
		client:   mgr.GetClient(),
		logger:   mgr.GetLogger(),
		handlers: make(map[string]func()),
	}
	_, err = ctrl.NewControllerManagedBy(mgr).
		For(&batchv1.Job{}).
		Build(jr)
	require.NoError(t, err)

	cw := &compositionWatcher{
		client:   mgr.GetClient(),
		handlers: make(map[string]func(*apiv1.Composition)),
	}
	_, err = ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Build(cw)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-done
	})
	go func() {
		defer close(done)
		err = mgr.Start(ctx)
		if err != nil {
			panic(err)
		}
	}()

	ok := mgr.GetCache().WaitForCacheSync(context.Background())
	require.True(t, ok)

	return &testManager{
		Manager:            mgr,
		jobRunner:          jr,
		compositionWatcher: cw,
	}
}

type testManager struct {
	ctrl.Manager
	*jobRunner
	*compositionWatcher
}

type jobRunner struct {
	client   client.Client
	logger   logr.Logger
	handlers map[string]func() // TODO: Mut
}

func (c *jobRunner) AddJobHandler(image string, fn func()) {
	c.handlers[image] = fn
}

func (c *jobRunner) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	c.logger.Info("running job")

	job := &batchv1.Job{}
	err := c.client.Get(ctx, req.NamespacedName, job)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if len(job.Spec.Template.Spec.Containers) == 0 {
		c.logger.Info("job doesn't have a container")
		return ctrl.Result{}, nil
	}
	image := job.Spec.Template.Spec.Containers[0].Image

	handler := c.handlers[image]
	if handler == nil {
		c.logger.Info("no handler found for job")
		return ctrl.Result{}, nil
	}
	handler()

	return ctrl.Result{}, nil
}

func compose(t *testing.T, mgr *testManager, comp *apiv1.Composition, fn composition.GenerateFn) func() {
	return func() {
		gen := &wrapper.Generator{
			Client:                mgr.GetClient(),
			Logger:                mgr.GetLogger(),
			CompositionName:       comp.Name,
			CompositionGeneration: comp.Generation,
			Exec: func(ctx context.Context, b []byte) ([]byte, error) {
				out := bytes.Buffer{}
				return out.Bytes(), composition.GenerateForIO(mgr.GetScheme(), bytes.NewBuffer(b), &out, fn)
			},
		}
		err := gen.Generate(context.Background())
		require.NoError(t, err)
	}
}

type compositionWatcher struct {
	client   client.Client
	handlers map[string]func(*apiv1.Composition) // TODO: Mut
}

func (c *compositionWatcher) WaitForCondition(t *testing.T, composition, condType string, condStatus metav1.ConditionStatus) <-chan struct{} {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c.handlers[composition] = func(comp *apiv1.Composition) {
		cond := meta.FindStatusCondition(comp.Status.Conditions, condType)
		if cond != nil && cond.Status == condStatus {
			cancel()
			delete(c.handlers, composition)
		}
	}

	return ctx.Done()
}

func (c *compositionWatcher) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	handler := c.handlers[comp.Name]
	if handler == nil {
		return ctrl.Result{}, nil
	}
	handler(comp)

	return ctrl.Result{}, nil
}
