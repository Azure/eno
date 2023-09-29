package integration

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/composition"
	"github.com/Azure/eno/conf"
	"github.com/Azure/eno/internal/clientmgr"
	"github.com/Azure/eno/internal/controllers"
	"github.com/Azure/eno/internal/wrapper"
)

var testCases = []struct {
	Name     string
	Inputs   []client.Object
	Versions []*state
}{
	{
		Name: "basic-configmap",
		Versions: []*state{
			{
				Generate: func(i *composition.Inputs) ([]client.Object, error) {
					cm := &corev1.ConfigMap{}
					cm.Name = "test-configmap"
					cm.Data = map[string]string{"foo": "bar"}

					return []client.Object{cm}, nil
				},
				Verify: func(t *testing.T, c client.Client) {
					cm := &corev1.ConfigMap{}
					cm.Name = "test-configmap"
					err := c.Get(context.Background(), client.ObjectKeyFromObject(cm), cm)
					require.NoError(t, err)
					assert.Equal(t, map[string]string{"foo": "bar"}, cm.Data)
				},
			},
			{
				Generate: func(i *composition.Inputs) ([]client.Object, error) {
					return []client.Object{}, nil
				},
				Verify: func(t *testing.T, c client.Client) {
					cm := &corev1.ConfigMap{}
					cm.Name = "test-configmap"
					err := c.Get(context.Background(), client.ObjectKeyFromObject(cm), cm)
					assert.True(t, errors.IsNotFound(err))
				},
			},
		},
	},
}

type state struct {
	Generate composition.GenerateFn
	Verify   func(*testing.T, client.Client)
}

func TestTable(t *testing.T) {
	mgr := setup(t)

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			for i, state := range tc.Versions {
				comp := &apiv1.Composition{}
				comp.Name = tc.Name
				current := comp.DeepCopy()

				image := fmt.Sprintf("%s-%d", tc.Name, i)

				mgr.AddJobHandler(image, compose(t, mgr, comp, state.Generate))
				wait := mgr.WaitForCondition(t, comp.Name, apiv1.ReconciledConditionType, metav1.ConditionTrue)

				_, err := controllerutil.CreateOrUpdate(context.Background(), mgr.GetClient(), current, func() error {
					current.Spec.Generator = &apiv1.Generator{Image: image}
					return nil
				})
				require.NoError(t, err)

				<-wait
				state.Verify(t, mgr.GetClient())
			}

			// TODO: Delete
		})
	}
}

func setup(t *testing.T) *testManager {
	config := &conf.Config{
		WrapperImage:          "fake-wrapper-image",
		JobTimeout:            time.Second * 10,
		JobTTL:                time.Minute,
		JobNS:                 "default",
		StatusPollingInterval: time.Millisecond * 100,
	}

	mgr := &testManager{Manager: setupMgr(t)}
	setupTestControllers(t, mgr)

	cmgr := clientmgr.New(mgr.GetClient(), func(ctx context.Context, key string) (*rest.Config, error) {
		return nil, nil
	})

	err := controllers.New(mgr, cmgr, config)
	require.NoError(t, err)

	startMgr(t, mgr)
	return mgr
}

type testManager struct {
	ctrl.Manager
	*jobRunner
	*compositionWatcher
}

func setupMgr(t *testing.T) ctrl.Manager {
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

	return mgr
}

func setupTestControllers(t *testing.T, mgr *testManager) {
	mgr.jobRunner = &jobRunner{
		client:   mgr.GetClient(),
		logger:   mgr.GetLogger(),
		handlers: make(map[string]func()),
	}
	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&batchv1.Job{}).
		Build(mgr.jobRunner)
	require.NoError(t, err)

	mgr.compositionWatcher = &compositionWatcher{
		client:   mgr.GetClient(),
		handlers: make(map[string]func(*apiv1.Composition)),
	}
	_, err = ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Build(mgr.compositionWatcher)
	require.NoError(t, err)
}

func startMgr(t *testing.T, mgr *testManager) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-done
	})
	go func() {
		defer close(done)
		err := mgr.Start(ctx)
		if err != nil {
			panic(err)
		}
	}()

	ok := mgr.GetCache().WaitForCacheSync(context.Background())
	require.True(t, ok)
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

	c.logger.Info(fmt.Sprintf("generated resources for job %s with image: %s", job.Name, image))
	return ctrl.Result{}, nil
}

func compose(t *testing.T, mgr *testManager, comp *apiv1.Composition, fn composition.GenerateFn) func() {
	return func() {
		current := &apiv1.Composition{}
		err := mgr.GetClient().Get(context.Background(), client.ObjectKeyFromObject(comp), current)
		require.NoError(t, err)

		gen := &wrapper.Generator{
			Client:                mgr.GetClient(),
			Logger:                mgr.GetLogger(),
			CompositionName:       current.Name,
			CompositionGeneration: current.Generation,
			Exec: func(ctx context.Context, b []byte) ([]byte, error) {
				out := bytes.Buffer{}
				return out.Bytes(), composition.GenerateForIO(mgr.GetScheme(), bytes.NewBuffer(b), &out, fn)
			},
		}
		err = gen.Generate(context.Background())
		require.NoError(t, err)
	}
}

type compositionWatcher struct {
	client   client.Client
	handlers map[string]func(*apiv1.Composition) // TODO: Mut
}

func (c *compositionWatcher) WaitForCondition(t *testing.T, composition, condType string, condStatus metav1.ConditionStatus) <-chan struct{} {
	ctx, cancel := context.WithCancel(context.Background())
	// TODO: Timeout eventually

	c.handlers[composition] = func(comp *apiv1.Composition) {
		cond := meta.FindStatusCondition(comp.Status.Conditions, condType)
		if cond != nil && cond.Status == condStatus && cond.ObservedGeneration == comp.Generation {
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
