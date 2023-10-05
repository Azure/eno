// Integration contains simple integration tests to cover the various components (controllers, wrapper process, generation framework, etc.)
// This is accomplished by replacing the wrapper and generator processes with in-process shims to the relevant code.
// Don't add too many tests here — most functionality can be covered in controller-scoped tests.
package integration

import (
	"bytes"
	"context"
	_ "embed"
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
	"github.com/Azure/eno/generation"
	"github.com/Azure/eno/internal/clientmgr"
	"github.com/Azure/eno/internal/conf"
	"github.com/Azure/eno/internal/controllers"
	testapi "github.com/Azure/eno/internal/integration/api"
	"github.com/Azure/eno/internal/wrapper"
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

func setup(t *testing.T) *testManager {
	config := &conf.Config{
		WrapperImage:          "fake-wrapper-image",
		JobTimeout:            time.Second * 10,
		JobTTL:                time.Minute,
		JobNS:                 "default",
		StatusPollingInterval: time.Millisecond * 100,
	}

	mgr := setupMgr(t)
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

	wrapperClient client.Client
}

func setupMgr(t *testing.T) *testManager {
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

	// Use a separate client/scheme for the wrapper process to better approximate real life
	wrapperClient, err := client.New(cfg, client.Options{})
	require.NoError(t, err)

	err = testapi.SchemeBuilder.AddToScheme(wrapperClient.Scheme())
	require.NoError(t, err)

	return &testManager{Manager: mgr, wrapperClient: wrapperClient}
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

type compositionWatcher struct {
	client   client.Client
	handlers map[string]func(*apiv1.Composition) // TODO: Mut
}

func (c *compositionWatcher) WaitForCondition(t *testing.T, composition, condType string, condStatus metav1.ConditionStatus) <-chan struct{} {
	ctx, cancel := context.WithCancel(context.Background())
	// TODO: Timeout eventually

	c.handlers[composition] = func(comp *apiv1.Composition) {
		if comp == nil {
			return
		}
		cond := meta.FindStatusCondition(comp.Status.Conditions, condType)
		if cond != nil && cond.Status == condStatus && cond.ObservedGeneration == comp.Generation {
			cancel()
			delete(c.handlers, composition)
		}
	}

	return ctx.Done()
}

func (c *compositionWatcher) WaitForDeletion(t *testing.T, composition string) <-chan struct{} {
	ctx, cancel := context.WithCancel(context.Background())
	// TODO: Timeout eventually

	c.handlers[composition] = func(comp *apiv1.Composition) {
		if comp == nil {
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
		if errors.IsNotFound(err) {
			comp = nil
		} else {
			return ctrl.Result{}, err
		}
	}

	return c.invokeHandler(req.Name, comp)
}

func (c *compositionWatcher) invokeHandler(name string, comp *apiv1.Composition) (ctrl.Result, error) {
	handler := c.handlers[name]
	if handler == nil {
		return ctrl.Result{}, nil
	}

	handler(comp)
	return ctrl.Result{}, nil
}

func compose(t *testing.T, mgr *testManager, comp string, fn generation.GenerateFn) func() {
	return func() {
		current := &apiv1.Composition{}
		current.Name = comp
		err := mgr.GetClient().Get(context.Background(), client.ObjectKeyFromObject(current), current)
		require.NoError(t, err)

		gen := &wrapper.Generator{
			Client:                mgr.wrapperClient,
			Logger:                mgr.GetLogger(),
			CompositionName:       current.Name,
			CompositionGeneration: current.Generation,
			Exec: func(ctx context.Context, b []byte) ([]byte, error) {
				out := bytes.Buffer{}
				err := generation.GenerateForIO(mgr.GetScheme(), bytes.NewBuffer(b), &out, fn)
				return out.Bytes(), err
			},
		}
		err = gen.Generate(context.Background())
		require.NoError(t, err)
	}
}

func syncTestComposition(t *testing.T, mgr ctrl.Manager, name, generatorImage string) {
	comp := &apiv1.Composition{}
	comp.Name = name
	_, err := controllerutil.CreateOrUpdate(context.Background(), mgr.GetClient(), comp, func() error {
		comp.Spec.Generator = &apiv1.Generator{Image: generatorImage}
		return nil
	})
	require.NoError(t, err)
}

func deleteTestComposition(t *testing.T, mgr ctrl.Manager, name string) {
	comp := &apiv1.Composition{}
	comp.Name = name

	err := mgr.GetClient().Get(context.Background(), client.ObjectKeyFromObject(comp), comp)
	require.NoError(t, err)

	err = mgr.GetClient().Delete(context.Background(), comp)
	require.NoError(t, err)
}
