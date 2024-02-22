package manager

import (
	"context"
	"fmt"
	"os"

	"net/http"
	_ "net/http/pprof"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/Azure/eno/api/v1"
)

const (
	IdxPodsByComposition           = ".metadata.ownerReferences.composition"
	IdxCompositionsBySynthesizer   = ".spec.synthesizer"
	IdxResourceSlicesByComposition = ".resourceSlicesByComposition"

	ManagerLabelKey   = "app.kubernetes.io/managed-by"
	ManagerLabelValue = "eno"
)

func init() {
	go func() {
		if addr := os.Getenv("PPROF_ADDR"); addr != "" {
			err := http.ListenAndServe(addr, nil)
			panic(fmt.Sprintf("unable to serve pprof listener: %s", err))
		}
	}()
}

func New(logger logr.Logger, opts *Options) (ctrl.Manager, error) {
	return newMgr(logger, opts, true)
}

func NewReconciler(logger logr.Logger, opts *Options) (ctrl.Manager, error) {
	return newMgr(logger, opts, false)
}

func newMgr(logger logr.Logger, opts *Options, isReconciler bool) (ctrl.Manager, error) {
	opts.Rest.QPS = float32(opts.qps)

	scheme := runtime.NewScheme()
	err := apiv1.SchemeBuilder.AddToScheme(scheme)
	if err != nil {
		return nil, err
	}
	err = corev1.SchemeBuilder.AddToScheme(scheme)
	if err != nil {
		return nil, err
	}

	mgrOpts := manager.Options{
		Logger:                 logger,
		HealthProbeBindAddress: opts.HealthProbeAddr,
		Scheme:                 scheme,
		Metrics: server.Options{
			BindAddress: opts.MetricsAddr,
		},
		BaseContext: func() context.Context {
			return logr.NewContext(context.Background(), logger)
		},
	}

	labelSelector, err := opts.getDefaultLabelSelector()
	if err != nil {
		return nil, err
	}
	mgrOpts.Cache.DefaultLabelSelector = labelSelector
	fieldSelector, err := opts.getDefaultFieldSelector()
	if err != nil {
		return nil, err
	}
	mgrOpts.Cache.DefaultFieldSelector = fieldSelector

	podLabelSelector := labels.SelectorFromSet(labels.Set{ManagerLabelKey: ManagerLabelValue})
	mgrOpts.Cache.ByObject = map[client.Object]cache.ByObject{
		// We do not honor the configured label selector, because these pods will only ever have labels set by Eno.
		// But we _do_ honor the field selector since it may reduce the namespace scope, etc.
		&corev1.Pod{}: {Label: podLabelSelector, Field: fieldSelector},
	}

	if !isReconciler {
		yespls := true
		mgrOpts.Cache.ByObject[&apiv1.ResourceSlice{}] = cache.ByObject{
			UnsafeDisableDeepCopy: &yespls,
		}
	}

	mgr, err := ctrl.NewManager(opts.Rest, mgrOpts)
	if err != nil {
		return nil, err
	}

	if isReconciler {
		err = mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, IdxPodsByComposition, indexController())
		if err != nil {
			return nil, err
		}

		err = mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1.ResourceSlice{}, IdxResourceSlicesByComposition, indexController())
		if err != nil {
			return nil, err
		}

		err = mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1.Composition{}, IdxCompositionsBySynthesizer, func(o client.Object) []string {
			comp := o.(*apiv1.Composition)
			return []string{comp.Spec.Synthesizer.Name}
		})
		if err != nil {
			return nil, err
		}
	}

	mgr.AddHealthzCheck("ping", healthz.Ping)
	return mgr, nil
}

func NewLogConstructor(mgr ctrl.Manager, controllerName string) func(*reconcile.Request) logr.Logger {
	return func(req *reconcile.Request) logr.Logger {
		l := mgr.GetLogger().WithValues("controller", controllerName)
		if req != nil {
			l.WithValues("requestName", req.Name, "requestNamespace", req.Namespace)
		}
		return l
	}
}

func NewCompositionToResourceSliceHandler(cli client.Client) handler.EventHandler {
	apply := func(ctx context.Context, rli workqueue.RateLimitingInterface, obj client.Object) {
		list := &apiv1.ResourceSliceList{}
		err := cli.List(ctx, list, client.MatchingFields{
			IdxResourceSlicesByComposition: obj.GetName(),
		})
		if err != nil {
			logr.FromContextOrDiscard(ctx).Error(err, "listing resource slices by composition")
			return
		}
		for _, item := range list.Items {
			rli.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: item.Name, Namespace: item.Namespace}})
		}
	}
	return &handler.Funcs{
		CreateFunc: func(ctx context.Context, ce event.CreateEvent, rli workqueue.RateLimitingInterface) {
			apply(ctx, rli, ce.Object)
		},
		UpdateFunc: func(ctx context.Context, ue event.UpdateEvent, rli workqueue.RateLimitingInterface) {
			apply(ctx, rli, ue.ObjectNew)
		},
		DeleteFunc: func(ctx context.Context, de event.DeleteEvent, rli workqueue.RateLimitingInterface) {
			apply(ctx, rli, de.Object)
		},
	}
}

func NewCompositionToSynthesizerHandler(cli client.Client) handler.EventHandler {
	return &handler.Funcs{
		CreateFunc: func(ctx context.Context, ce event.CreateEvent, rli workqueue.RateLimitingInterface) {
			comp, ok := ce.Object.(*apiv1.Composition)
			if !ok {
				logr.FromContextOrDiscard(ctx).V(0).Info("unexpected type given to NewCompositionToSynthesizerHandler")
				return
			}
			rli.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: comp.Spec.Synthesizer.Name}})
		},
		UpdateFunc: func(ctx context.Context, ue event.UpdateEvent, rli workqueue.RateLimitingInterface) {
			comp, ok := ue.ObjectNew.(*apiv1.Composition)
			if !ok {
				logr.FromContextOrDiscard(ctx).V(0).Info("unexpected type given to NewCompositionToSynthesizerHandler")
				return
			}
			rli.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: comp.Spec.Synthesizer.Name}})
		},
		DeleteFunc: func(ctx context.Context, de event.DeleteEvent, rli workqueue.RateLimitingInterface) {
			comp, ok := de.Object.(*apiv1.Composition)
			if !ok {
				logr.FromContextOrDiscard(ctx).V(0).Info("unexpected type given to NewCompositionToSynthesizerHandler")
				return
			}
			rli.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: comp.Spec.Synthesizer.Name}})
		},
	}
}

func indexController() client.IndexerFunc {
	return func(o client.Object) []string {
		owner := metav1.GetControllerOf(o)
		if owner == nil {
			return nil
		}
		return []string{owner.Name}
	}
}
