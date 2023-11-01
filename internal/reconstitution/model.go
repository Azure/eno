package reconstitution

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type GeneratedResourceMeta struct {
	Name, Namespace, Kind string
}

// GeneratedResource is the controller's internal representation of a single generated resource out of a GeneratedResourceSlice.
type GeneratedResource struct {
	Meta   *GeneratedResourceMeta
	Spec   *GeneratedResourceSpec
	Status *GeneratedResourceStatus
}

type GeneratedResourceSpec struct {
	Manifest string
	Object   *unstructured.Unstructured

	ReconcileInterval time.Duration
}

type GeneratedResourceStatus struct {
	Synced                  bool
	ObservedResourceVersion string
}
