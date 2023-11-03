package reconstitution

import (
	"k8s.io/apimachinery/pkg/types"

	apiv1 "github.com/Azure/eno/api/v1"
)

type StatusPatchFn func(*apiv1.ResourceState) bool

// ManifestRef references a particular resource manifest within a resource slice.
type ManifestRef struct {
	Slice types.NamespacedName
	Index int // position of this manifest within the slice
}
