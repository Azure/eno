package manager

import (
	"context"
	"fmt"
	"os"

	"net/http"
	_ "net/http/pprof"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
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
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/Azure/eno/api/v1"
)

func init() {
	log.SetLogger(zap.New(zap.WriteTo(os.Stdout)))
}

// IMPORTANT: There are several things to know about how controller-runtime is configured:
// - Resource slices are only watched by the reconciler process to avoid the cost of watching all of them in the controller
// - Resource slices are not deep copied when reading from the informer - do not mutate them
// - The resource slices cached by the informer do not have the configured manifests since they are held by the reconstitution cache anyway

const (
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
	return newMgr(logger, opts, true, false)
}

func NewReconciler(logger logr.Logger, opts *Options) (ctrl.Manager, error) {
	return newMgr(logger, opts, false, true)
}

func NewTest(logger logr.Logger, opts *Options) (ctrl.Manager, error) {
	return newMgr(logger, opts, true, true)
}

func newMgr(logger logr.Logger, opts *Options, isController, isReconciler bool) (ctrl.Manager, error) {
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
		Cache: cache.Options{
			ByObject: make(map[client.Object]cache.ByObject),
		},
		LeaderElection:                opts.LeaderElection,
		LeaderElectionNamespace:       opts.LeaderElectionNamespace,
		LeaderElectionResourceLock:    opts.LeaderElectionResourceLock,
		LeaderElectionID:              opts.LeaderElectionID,
		LeaseDuration:                 &opts.ElectionLeaseDuration,
		RenewDeadline:                 &opts.ElectionLeaseRenewDeadline,
		LeaderElectionReleaseOnCancel: true,
	}

	if isController {
		// Only cache pods in the synthesizer pod namespace and owned by this controller
		mgrOpts.Cache.ByObject[&corev1.Pod{}] = cache.ByObject{
			Namespaces: map[string]cache.Config{
				opts.SynthesizerPodNamespace: {
					LabelSelector: labels.SelectorFromSet(labels.Set{ManagerLabelKey: ManagerLabelValue}),
				},
			},
		}
	}

	if isReconciler {
		// TODO: Dedicated test for composition namespace isolation.
		if opts.CompositionNamespace != cache.AllNamespaces {
			mgrOpts.Cache.ByObject[&apiv1.Composition{}] = cache.ByObject{
				Namespaces: map[string]cache.Config{
					opts.CompositionNamespace: {
						LabelSelector: opts.CompositionSelector,
					},
				},
			}
		} else {
			mgrOpts.Cache.ByObject[&apiv1.Composition{}] = cache.ByObject{
				Label: opts.CompositionSelector,
			}
		}

		yespls := true
		mgrOpts.Cache.ByObject[&apiv1.ResourceSlice{}] = cache.ByObject{
			UnsafeDisableDeepCopy: &yespls,
			Transform: func(obj any) (any, error) {
				slice, ok := obj.(*apiv1.ResourceSlice)
				if !ok {
					return obj, nil
				}
				for i := range slice.Spec.Resources {
					slice.Spec.Resources[i].Manifest = "" // remove big manifest that we don't need
				}
				return slice, nil
			},
		}
	}

	mgr, err := ctrl.NewManager(opts.Rest, mgrOpts)
	if err != nil {
		return nil, err
	}

	if isController {
		err = mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, IdxPodsByComposition, func(o client.Object) []string {
			return []string{PodByCompIdxValueFromPod(o)}
		})
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

		err = mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1.Composition{}, IdxCompositionsBySymphony, indexController())
		if err != nil {
			return nil, err
		}

		err = mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1.Composition{}, IdxCompositionsByBinding, indexResourceBindings())
		if err != nil {
			return nil, err
		}

		err = mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1.Synthesizer{}, IdxSynthesizersByRef, indexSynthRefs())
		if err != nil {
			return nil, err
		}
	}

	if isReconciler {
		err = mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1.ResourceSlice{}, IdxResourceSlicesByComposition, indexController())
		if err != nil {
			return nil, err
		}
	}

	mgr.AddHealthzCheck("ping", healthz.Ping)
	mgr.AddReadyzCheck("ping", healthz.Ping)
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
		err := cli.List(ctx, list, client.InNamespace(obj.GetNamespace()), client.MatchingFields{
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

func SingleEventHandler() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(handler.MapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		return []reconcile.Request{{}}
	}))
}
