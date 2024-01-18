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

	// TODO: Evaluate if the passed-in label selector should be merged with the Pod label selector.
	// It probably should not because we're only watching Pods created by Eno itself,
	// but this is not the case if we by some reason start watching Pods not belonging to Eno.
	podLabelSelector := labels.SelectorFromSet(labels.Set{ManagerLabelKey: ManagerLabelValue})
	if opts.Namespace == "" {
		mgrOpts.Cache.ByObject = map[client.Object]cache.ByObject{
			&corev1.Pod{}: {Label: podLabelSelector},
		}
	} else {
		mgrOpts.Cache.ByObject = map[client.Object]cache.ByObject{
			&corev1.Pod{}: {
				Namespaces: map[string]cache.Config{
					opts.Namespace: {
						LabelSelector: podLabelSelector,
					},
				},
			},
		}

		mgrOpts.Cache.DefaultNamespaces = map[string]cache.Config{
			opts.Namespace: {},
		}
	}

	mgr, err := ctrl.NewManager(opts.Rest, mgrOpts)
	if err != nil {
		return nil, err
	}

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
