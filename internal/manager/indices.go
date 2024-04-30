package manager

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	IdxPodsByComposition           = ".podsByComposition"
	IdxCompositionsBySynthesizer   = ".spec.synthesizer"
	IdxCompositionsBySymphony      = ".compositionsBySymphony"
	IdxResourceSlicesByComposition = ".resourceSlicesByComposition"

	CompositionNameLabelKey      = "eno.azure.io/composition-name"
	CompositionNamespaceLabelKey = "eno.azure.io/composition-namespace"
)

func PodReferencesComposition(pod *corev1.Pod) bool {
	labels := pod.GetLabels()
	if labels == nil || labels[CompositionNameLabelKey] == "" || labels[CompositionNamespaceLabelKey] == "" {
		return false
	}
	return true
}

func PodToCompMapFunc(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		logr.FromContextOrDiscard(ctx).V(0).Info("unexpected type given to podToCompMapFunc")
		return nil
	}
	if !PodReferencesComposition(pod) {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Name:      pod.GetLabels()[CompositionNameLabelKey],
			Namespace: pod.GetLabels()[CompositionNamespaceLabelKey],
		},
	}}
}

func PodByCompIdxValueFromPod(po client.Object) string {
	labels := po.GetLabels()
	return labels[CompositionNameLabelKey] + "/" + labels[CompositionNamespaceLabelKey]
}

func PodByCompIdxValueFromComp(comp client.Object) string {
	return comp.GetName() + "/" + comp.GetNamespace()
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
