package resourceslice

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-logr/logr"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/Azure/eno/internal/resource"
)

// TODO: Keep using finalizer I think

type controller struct {
	client        client.Client
	noCacheReader client.Reader
	cache         *resource.Cache
}

func NewController(mgr ctrl.Manager, cache *resource.Cache) error {
	r := &controller{
		client:        mgr.GetClient(),
		noCacheReader: mgr.GetAPIReader(),
		cache:         cache,
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("resourceSliceController").
		For(&apiv1.Composition{}).
		Owns(&apiv1.ResourceSlice{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "resourceSliceController")).
		Complete(r)
}

func (c *controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx).WithValues("compositionName", req.Name, "compositionNamespace", req.Namespace)
	ctx = logr.NewContext(ctx, logger)

	list := &apiv1.ResourceSliceList{}
	err := c.client.List(ctx, list, client.InNamespace(req.Namespace), client.MatchingFields{
		manager.IdxResourceSlicesByComposition: req.Name,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing resource slices: %w", err)
	}
	slicesByName := map[string]apiv1.ResourceSlice{}
	for _, slice := range list.Items {
		slicesByName[slice.Name] = slice
	}

	comp := &apiv1.Composition{}
	err = c.client.Get(ctx, req.NamespacedName, comp)
	if k8serrors.IsNotFound(err) {
		err = nil
		comp = nil
		c.cache.Purge(ctx, req.NamespacedName, nil)
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting resource: %w", err)
	}

	// Remove the old finalizer - we don't need it any more.
	// This can be removed in the future.
	for _, slice := range list.Items {
		if !controllerutil.RemoveFinalizer(&slice, "eno.azure.io/cleanup") {
			continue
		}
		err := c.client.Update(ctx, &slice)
		if err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("deleting resource slice %q: %w", slice.Name, err))
		}
		logger.V(0).Info("cleaned up finalizer", "resourceSliceName", slice.Name)
		return ctrl.Result{}, nil
	}

	// Clean up dangling slices
	for _, slice := range list.Items {
		if !c.shouldDeleteSlice(&slice, comp) {
			continue
		}
		err := c.client.Delete(ctx, &slice)
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("deleting resource slice %q: %w", slice.Name, err)
		}
		logger.V(0).Info("deleted unused resource slice", "resourceSliceName", slice.Name)
		return ctrl.Result{}, nil
	}

	// Sync the composition status
	patch := c.propagateStatus(ctx, comp, slicesByName)
	if len(patch) > 0 {
		patchJs, err := json.Marshal(&patch)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("encoding patch: %w", err)
		}
		err = c.client.Status().Patch(ctx, comp, client.RawPatch(types.JSONPatchType, patchJs))
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("patching composition status: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// Fill the cache
	if comp == nil || comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil {
		return ctrl.Result{}, nil
	}
	if modified, err := c.fillCache(ctx, comp, slicesByName, comp.Status.PreviousSynthesis); err != nil || modified {
		return ctrl.Result{}, err
	}
	if modified, err := c.fillCache(ctx, comp, slicesByName, comp.Status.CurrentSynthesis); err != nil || modified {
		return ctrl.Result{}, err
	}
	c.cache.Purge(ctx, req.NamespacedName, comp)
	return ctrl.Result{}, nil
}

func (c *controller) shouldDeleteSlice(slice *apiv1.ResourceSlice, comp *apiv1.Composition) bool {
	if comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil {
		return false // synthesis isn't done yet
	}
	if len(slice.Status.Resources) == 0 && len(slice.Spec.Resources) > 0 {
		return false // status is lagging behind
	}
	shouldOrphan := comp.Annotations != nil && comp.Annotations["eno.azure.io/deletion-strategy"] == "orphan"
	for _, state := range slice.Status.Resources {
		if !state.Deleted && !shouldOrphan {
			return false // resource hasn't been deleted yet
		}
	}
	return !comp.Status.CurrentSynthesis.ReferencesSlice(slice) &&
		!comp.Status.PreviousSynthesis.ReferencesSlice(slice)
}

func (c *controller) propagateStatus(ctx context.Context, comp *apiv1.Composition, byName map[string]apiv1.ResourceSlice) []jsonPatch {
	if comp == nil {
		return nil
	}
	syn := comp.Status.CurrentSynthesis
	if syn == nil || (syn.Synthesized != nil && syn.Reconciled != nil) {
		return nil
	}

	logger := logr.FromContextOrDiscard(ctx).WithValues("synthesisID", syn.UUID)
	reconciled, ready := aggregateSynthesisStatus(comp, byName)

	var ops []jsonPatch
	if reconciled && syn.Synthesized == nil {
		now := metav1.Now()
		logger.V(0).Info("composition was reconciled", "latency", max(0, now.Sub(syn.Synthesized.Time).Milliseconds()))
		ops = append(ops, jsonPatch{Op: "add", Path: "/status/currentSynthesis/reconciled", Value: now})
	}
	if ready != nil && syn.Ready == nil {
		logger.V(0).Info("composition was reconciled", "latency", max(0, ready.Sub(syn.Synthesized.Time).Milliseconds()))
		ops = append(ops, jsonPatch{Op: "add", Path: "/status/currentSynthesis/ready", Value: ready})
	}
	if len(ops) > 0 {
		ops = append(ops, jsonPatch{Op: "test", Path: "/status/currentSynthesis/uuid", Value: syn.UUID})
	}
	return ops
}

func aggregateSynthesisStatus(comp *apiv1.Composition, byName map[string]apiv1.ResourceSlice) (reconciled bool, ready *metav1.Time) {
	reconciled = true
	shouldOrphan := comp.Annotations != nil && comp.Annotations["eno.azure.io/deletion-strategy"] == "orphan"
	for _, ref := range comp.Status.CurrentSynthesis.ResourceSlices {
		slice, ok := byName[ref.Name]
		if !ok {
			continue // TODO: I think this is wrong
		}
		if len(slice.Status.Resources) == 0 && len(slice.Spec.Resources) > 0 {
			reconciled = false
			break
		}
		for _, state := range slice.Status.Resources {
			state := state
			if !state.Reconciled || (!state.Deleted && !shouldOrphan && comp.DeletionTimestamp != nil) {
				reconciled = false
			}
			if state.Ready != nil && (ready == nil || ready.Before(state.Ready)) {
				ready = state.Ready
			}
		}
	}
	return
}

type jsonPatch struct { // TODO: Move to shared pkg
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

func (c *controller) fillCache(ctx context.Context, comp *apiv1.Composition, slicesByName map[string]apiv1.ResourceSlice, syn *apiv1.Synthesis) (bool, error) {
	if syn == nil {
		return false, nil
	}
	logger := logr.FromContextOrDiscard(ctx).WithValues("synthesisID", syn.UUID)
	ctx = logr.NewContext(ctx, logger)

	// Get slices for this synthesis
	var slices []apiv1.ResourceSlice
	for _, ref := range syn.ResourceSlices {
		slice, ok := slicesByName[ref.Name]
		if !ok {
			return false, nil // wait for it to hit the informer
		}
		slices = append(slices, slice)
	}

	if c.cache.Visit(comp, syn.UUID, slices) {
		return false, nil // already filled
	}

	for i, partial := range slices {
		// Grab the full resource slice spec since the informers are filtered to reduce memory consumption
		slice := &apiv1.ResourceSlice{}
		err := c.client.Get(ctx,
			types.NamespacedName{Name: partial.Name, Namespace: comp.Namespace}, slice,
			&client.GetOptions{Raw: &metav1.GetOptions{ResourceVersion: partial.ResourceVersion}}) // TODO: Can this result in 404?
		if err == nil {
			// Keep the status from the informer to avoid possible backtracking on the next tick of the loop
			copy := slices[i].DeepCopy()
			copy.Spec.Resources = slice.Spec.Resources
			slices[i] = *copy
			continue
		}

		// Resynthesize to recover missing slices
		if k8serrors.IsNotFound(err) && !comp.ShouldForceResynthesis() && !comp.ShouldIgnoreSideEffects() {
			comp.ForceResynthesis()
			err = c.client.Update(ctx, comp)
			if err != nil {
				return false, fmt.Errorf("forcing resynthesis: %w", err)
			}
			logger.V(0).Info("forcing resynthesis because slice is missing", "resourceSliceName", partial.Name)
			return true, nil
		}

		return false, fmt.Errorf("getting resource slice %q: %w", partial.Name, err)
	}

	c.cache.Fill(ctx, comp, syn.UUID, slices)
	return false, nil
}
