package manager

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
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
	IdxPodsByComposition         = ".metadata.ownerReferences.composition"
	IdxCompositionsBySynthesizer = ".spec.synthesizer"
)

// TODO: Filter informers on pod label

type Options struct {
	Rest            *rest.Config
	HealthProbeAddr string
	MetricsAddr     string
}

func New(logger logr.Logger, opts *Options) (ctrl.Manager, error) {
	mgr, err := ctrl.NewManager(opts.Rest, manager.Options{
		Logger:                 logger,
		HealthProbeBindAddress: opts.HealthProbeAddr,
		Metrics: server.Options{
			BindAddress: opts.MetricsAddr,
		},
	})
	if err != nil {
		return nil, err
	}

	err = apiv1.SchemeBuilder.AddToScheme(mgr.GetScheme())
	if err != nil {
		return nil, err
	}

	err = mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, IdxPodsByComposition, func(o client.Object) []string {
		pod := o.(*corev1.Pod)
		owner := metav1.GetControllerOf(pod)
		if owner == nil || owner.Kind != "Composition" {
			return nil
		}
		return []string{owner.Name}
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
