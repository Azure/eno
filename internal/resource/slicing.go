package resource

import (
	"fmt"

	apiv1 "github.com/Azure/eno/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Slice builds a new set of resource slices by merging a new set of resources onto an old set of slices.
// - New and updated resources are partitioned across slices per maxJsonBytes
// - Removed resources are converted into "tombstones" i.e. manifests with Deleted == true
func Slice(comp *apiv1.Composition, previous []*apiv1.ResourceSlice, outputs []*unstructured.Unstructured, maxJsonBytes int) ([]*apiv1.ResourceSlice, error) {
	refs := map[resourceRef]struct{}{}
	manifests := []apiv1.Manifest{}
	for i, output := range outputs {
		js, err := output.MarshalJSON()
		if err != nil {
			return nil, reconcile.TerminalError(fmt.Errorf("encoding output %d: %w", i, err))
		}
		manifests = append(manifests, apiv1.Manifest{
			Manifest: string(js),
		})
		refs[newResourceRef(output)] = struct{}{}
	}

	// Build tombstones by diffing the new state against the current state
	// Existing tombstones are passed down if they haven't yet been reconciled to avoid orphaning resources
	for _, slice := range previous {
		for i, res := range slice.Spec.Resources {
			res := res
			obj := &unstructured.Unstructured{}
			err := obj.UnmarshalJSON([]byte(res.Manifest))
			if err != nil {
				return nil, reconcile.TerminalError(fmt.Errorf("decoding resource %d of slice %s: %w", i, slice.Name, err))
			}

			if obj.GetObjectKind().GroupVersionKind() == patchGVK {
				// Patches can be removed without deleting the resource
				continue
			}

			// We don't need a tombstone once the deleted resource has been reconciled
			if _, ok := refs[newResourceRef(obj)]; ok || ((res.Deleted || slice.DeletionTimestamp != nil) && slice.Status.Resources != nil && slice.Status.Resources[i].Reconciled) {
				continue // still exists or has already been deleted
			}

			res.Deleted = true
			manifests = append(manifests, res)
		}
	}

	// Build the slice resources
	var (
		slices             []*apiv1.ResourceSlice
		sliceBytes         int
		slice              *apiv1.ResourceSlice
		blockOwnerDeletion = true
	)
	for _, manifest := range manifests {
		if slice == nil || sliceBytes >= maxJsonBytes {
			sliceBytes = 0
			slice = &apiv1.ResourceSlice{}
			slice.GenerateName = comp.Name + "-"
			slice.Namespace = comp.Namespace
			slice.Finalizers = []string{"eno.azure.io/cleanup"}
			slice.OwnerReferences = []metav1.OwnerReference{{
				APIVersion:         apiv1.SchemeGroupVersion.Identifier(),
				Kind:               "Composition",
				Name:               comp.Name,
				UID:                comp.UID,
				BlockOwnerDeletion: &blockOwnerDeletion, // we need the composition in order to successfully delete its resource slices
				Controller:         &blockOwnerDeletion,
			}}
			if comp.Status.CurrentSynthesis != nil {
				slice.Spec.SynthesisUUID = comp.Status.CurrentSynthesis.UUID
			}
			slices = append(slices, slice)
		}
		sliceBytes += len(manifest.Manifest)
		slice.Spec.Resources = append(slice.Spec.Resources, manifest)
	}

	return slices, nil
}

type resourceRef struct {
	Name, Namespace, Kind, Group string
}

func newResourceRef(obj *unstructured.Unstructured) resourceRef {
	if obj.GetObjectKind().GroupVersionKind() == patchGVK {
		apiVersion, _, _ := unstructured.NestedString(obj.Object, "patch", "apiVersion")
		kind, _, _ := unstructured.NestedString(obj.Object, "patch", "kind")
		gv, _ := schema.ParseGroupVersion(apiVersion)
		return resourceRef{
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
			Kind:      kind,
			Group:     gv.Group,
		}
	}

	return resourceRef{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Kind:      obj.GetKind(),
		Group:     obj.GroupVersionKind().Group,
	}
}
