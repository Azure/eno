package tombstonerecovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/Azure/eno/internal/resource"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type sliceAddition struct {
	sliceIndex int
	tombstones []apiv1.Manifest
}

type overflowSlice struct {
	tombstones []apiv1.Manifest
}

type tombstonePackingPlan struct {
	additions []sliceAddition
	overflow  []overflowSlice // Each entry becomes a new ResourceSlice within the manifest-byte limit.
}

// planTombstones fills existing slices first, then batches overflow without mutating inputs.
func planTombstones(slices []apiv1.ResourceSlice, tombstones []apiv1.Manifest) (tombstonePackingPlan, error) {
	plan := tombstonePackingPlan{additions: make([]sliceAddition, len(slices))}
	sizes := make([]int, len(slices))
	for i, slice := range slices {
		plan.additions[i].sliceIndex = i
		for _, manifest := range slice.Spec.Resources {
			sizes[i] += len(manifest.Manifest)
		}
	}
	var overflow []apiv1.Manifest
	for _, manifest := range tombstones {
		size := len(manifest.Manifest)
		if size > resource.MaxSliceJSONBytes {
			return tombstonePackingPlan{}, fmt.Errorf("recovery tombstone exceeds the ResourceSlice manifest byte limit: %d > %d", size, resource.MaxSliceJSONBytes)
		}
		placed := false
		for i := range slices {
			if sizes[i]+size <= resource.MaxSliceJSONBytes {
				plan.additions[i].tombstones = append(plan.additions[i].tombstones, manifest)
				sizes[i] += size
				placed = true
				break
			}
		}
		if !placed {
			overflow = append(overflow, manifest)
		}
	}

	// Leftover tombstones may need multiple new slices; each has the same size limit.
	for len(overflow) > 0 {
		size, count := 0, 0
		for count < len(overflow) && size+len(overflow[count].Manifest) <= resource.MaxSliceJSONBytes {
			size += len(overflow[count].Manifest)
			count++
		}
		plan.overflow = append(plan.overflow, overflowSlice{tombstones: overflow[:count]})
		overflow = overflow[count:]
	}
	return plan, nil
}

func (c *tombstoneRecoveryController) writeTombstones(ctx context.Context, comp *apiv1.Composition, slices []apiv1.ResourceSlice, tombstones []apiv1.Manifest) ([]*apiv1.ResourceSliceRef, error) {
	// Validate the entire batch before persisting any additions.
	plan, err := planTombstones(slices, tombstones)
	if err != nil {
		return nil, err
	}
	for _, addition := range plan.additions {
		if len(addition.tombstones) == 0 {
			continue
		}
		before := slices[addition.sliceIndex].DeepCopy()
		after := before.DeepCopy()
		after.Spec.Resources = append(after.Spec.Resources, addition.tombstones...)
		if _, err := c.getCurrentComposition(ctx, comp, false); err != nil {
			return nil, err
		}
		if err := c.client.Patch(ctx, after, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return nil, fmt.Errorf("appending tombstones to ResourceSlice %q: %w", before.Name, err)
		}
		logr.FromContextOrDiscard(ctx).Info("appended recovery tombstones", "resourceSliceName", after.Name, "tombstoneCount", len(addition.tombstones))
	}

	var refs []*apiv1.ResourceSliceRef
	for _, overflow := range plan.overflow {
		slice, err := recoverySlice(comp, overflow.tombstones)
		if err != nil {
			return nil, err
		}
		if _, err := c.getCurrentComposition(ctx, comp, false); err != nil {
			return nil, err
		}
		if err := c.client.Create(ctx, slice); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return nil, fmt.Errorf("creating recovery ResourceSlice %q: %w", slice.Name, err)
			}
			// A retry may find overflow persisted before its reference was published.
			existing := &apiv1.ResourceSlice{}
			if err := c.reader.Get(ctx, client.ObjectKeyFromObject(slice), existing); err != nil {
				return nil, fmt.Errorf("reading existing recovery ResourceSlice %q: %w", slice.Name, err)
			}
			owner := metav1.GetControllerOf(existing)
			if existing.DeletionTimestamp != nil || owner == nil || owner.UID != comp.UID || owner.Kind != "Composition" ||
				existing.Labels[apiv1.TombstoneRecoveryLabelKey] != "true" ||
				!reflect.DeepEqual(existing.Spec, slice.Spec) {
				return nil, fmt.Errorf("existing recovery ResourceSlice %q does not match this operation or is being deleted", slice.Name)
			}
		}
		refs = append(refs, &apiv1.ResourceSliceRef{Name: slice.Name})
		logr.FromContextOrDiscard(ctx).Info("recovery overflow slice persisted", "resourceSliceName", slice.Name, "tombstoneCount", len(overflow.tombstones))
	}
	return refs, nil
}

func recoverySlice(comp *apiv1.Composition, manifests []apiv1.Manifest) (*apiv1.ResourceSlice, error) {
	data, err := json.Marshal(manifests)
	if err != nil {
		return nil, fmt.Errorf("encoding recovery slice contents: %w", err)
	}
	opHash := sha256.Sum256([]byte(string(comp.UID) + "/" + comp.Status.CurrentSynthesis.UUID))
	contentHash := sha256.Sum256(data)
	suffix := "-" + hex.EncodeToString(opHash[:8]) + "-" + hex.EncodeToString(contentHash[:16])
	prefix := comp.Name
	if maxPrefix := validation.DNS1123SubdomainMaxLength - len(suffix); len(prefix) > maxPrefix {
		prefix = strings.TrimRight(prefix[:maxPrefix], ".-")
	}
	return &apiv1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prefix + suffix,
			Namespace: comp.Namespace,
			Labels: map[string]string{
				manager.SynthesisIDLabelKey:     comp.Status.CurrentSynthesis.UUID,
				apiv1.TombstoneRecoveryLabelKey: "true",
			},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(comp, apiv1.SchemeGroupVersion.WithKind("Composition"))},
			Finalizers:      []string{"eno.azure.io/cleanup"},
		},
		Spec: apiv1.ResourceSliceSpec{
			SynthesisUUID: comp.Status.CurrentSynthesis.UUID,
			Resources:     manifests,
		},
	}, nil
}
