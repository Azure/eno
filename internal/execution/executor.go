package execution

import (
	"context"
	"fmt"
	"strconv"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// maxSliceJsonBytes is the max sum of a resource slice's manifests.
const maxSliceJsonBytes = 1024 * 512

type Executor struct {
	Reader  client.Reader
	Writer  client.Client
	Handler SynthesizerHandle
}

func (e *Executor) Synthesize(ctx context.Context, env *Env) error {
	comp := &apiv1.Composition{}
	comp.Name = env.CompositionName
	comp.Namespace = env.CompositionNamespace
	err := e.Reader.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	if err != nil {
		return fmt.Errorf("fetching composition: %w", err)
	}
	if comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.UUID != env.SynthesisUUID {
		// This pod is no longer needed, wait for the controller to clean it up
		return nil
	}

	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err = e.Reader.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	if err != nil {
		return fmt.Errorf("fetching synthesizer: %w", err)
	}

	input, revs, err := e.buildPodInput(ctx, comp, syn)
	if err != nil {
		return fmt.Errorf("building synthesizer input: %w", err)
	}

	output, err := e.Handler(ctx, syn, input)
	if err != nil {
		return fmt.Errorf("executing synthesizer: %w", err)
	}

	sliceRefs, err := e.writeSlices(ctx, comp, output)
	if err != nil {
		return err
	}

	return e.updateComposition(ctx, comp, syn, sliceRefs, revs, output)
}

func (e *Executor) buildPodInput(ctx context.Context, comp *apiv1.Composition, syn *apiv1.Synthesizer) (*krmv1.ResourceList, []apiv1.InputRevisions, error) {
	logger := logr.FromContextOrDiscard(ctx)
	bindings := map[string]*apiv1.Binding{}
	for _, b := range comp.Spec.Bindings {
		b := b
		bindings[b.Key] = &b
	}

	rl := &krmv1.ResourceList{
		Kind:       krmv1.ResourceListKind,
		APIVersion: krmv1.SchemeGroupVersion.String(),
	}
	revs := []apiv1.InputRevisions{}
	for _, r := range syn.Spec.Refs {
		key := r.Key
		b, ok := bindings[key]
		if !ok {
			return nil, nil, fmt.Errorf("input %q is referenced, but not bound", key)
		}

		// Get the resource
		start := time.Now()
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(schema.GroupVersionKind{Group: r.Resource.Group, Version: r.Resource.Version, Kind: r.Resource.Kind})
		obj.SetName(b.Resource.Name)
		obj.SetNamespace(b.Resource.Namespace)
		err := e.Reader.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		if err != nil {
			return nil, nil, fmt.Errorf("getting resource for ref %q: %w", key, err)
		}
		rl.Items = append(rl.Items, obj)
		logger.V(0).Info("retrieved input", "key", key, "latency", time.Since(start).Milliseconds())

		// Store the revision to be written to the synthesis status later
		ir := apiv1.InputRevisions{
			Key:             key,
			ResourceVersion: obj.GetResourceVersion(),
		}
		if rev, _ := strconv.Atoi(obj.GetAnnotations()["eno.azure.io/revision"]); rev != 0 {
			ir.Revision = &rev
		}
		revs = append(revs, ir)
	}

	return rl, revs, nil
}

func (e *Executor) writeSlices(ctx context.Context, comp *apiv1.Composition, rl *krmv1.ResourceList) ([]*apiv1.ResourceSliceRef, error) {
	logger := logr.FromContextOrDiscard(ctx)

	previous, err := e.fetchPreviousSlices(ctx, comp)
	if err != nil {
		return nil, err
	}

	slices, err := resource.Slice(comp, previous, rl.Items, maxSliceJsonBytes)
	if err != nil {
		return nil, err
	}

	sliceRefs := make([]*apiv1.ResourceSliceRef, len(slices))
	for i, slice := range slices {
		start := time.Now()

		err = e.writeResourceSlice(ctx, slice)
		if err != nil {
			return nil, fmt.Errorf("creating resource slice %d: %w", i, err)
		}

		logger.V(0).Info("wrote resource slice", "resourceSliceName", slice.Name, "latency", time.Since(start).Milliseconds())
		sliceRefs[i] = &apiv1.ResourceSliceRef{Name: slice.Name}
	}

	return sliceRefs, nil
}

func (e *Executor) fetchPreviousSlices(ctx context.Context, comp *apiv1.Composition) ([]*apiv1.ResourceSlice, error) {
	if comp.Status.PreviousSynthesis == nil {
		return nil, nil // nothing to fetch
	}
	logger := logr.FromContextOrDiscard(ctx)

	slices := []*apiv1.ResourceSlice{}
	for _, ref := range comp.Status.PreviousSynthesis.ResourceSlices {
		slice := &apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := e.Reader.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		if errors.IsNotFound(err) {
			logger.V(0).Info("resource slice referenced by composition was not found - skipping", "resourceSliceName", slice.Name)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("fetching current resource slice %q: %w", slice.Name, err)
		}
		slices = append(slices, slice)
	}

	return slices, nil
}

func (e *Executor) writeResourceSlice(ctx context.Context, slice *apiv1.ResourceSlice) error {
	var bytes int
	for _, res := range slice.Spec.Resources {
		bytes += len(res.Manifest)
	}

	// We retry on request timeouts to avoid the overhead of re-synthesizing in cases where we're sometimes unable to reach apiserver
	return retry.OnError(retry.DefaultRetry, errors.IsServerTimeout, func() error {
		err := e.Writer.Create(ctx, slice)
		if err != nil {
			logr.FromContextOrDiscard(ctx).Error(err, "error while creating resource slice - will retry later")
			return err
		}
		return nil
	})
}

func (e *Executor) updateComposition(ctx context.Context, oldComp *apiv1.Composition, syn *apiv1.Synthesizer, refs []*apiv1.ResourceSliceRef, revs []apiv1.InputRevisions, rl *krmv1.ResourceList) error {
	logger := logr.FromContextOrDiscard(ctx)
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		comp := &apiv1.Composition{}
		err := e.Reader.Get(ctx, client.ObjectKeyFromObject(oldComp), comp)
		if err != nil {
			return err
		}

		synthesis := comp.Status.CurrentSynthesis
		if synthesis == nil || synthesis.Synthesized != nil || synthesis.ObservedCompositionGeneration != oldComp.Generation {
			logger.V(0).Info("synthesis is no longer relevant - discarding its output")
			return nil
		}

		now := metav1.Now()
		comp.Status.CurrentSynthesis.Synthesized = &now
		comp.Status.CurrentSynthesis.ResourceSlices = refs
		comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration = syn.Generation
		comp.Status.CurrentSynthesis.InputRevisions = revs
		for _, result := range rl.Results {
			comp.Status.CurrentSynthesis.Results = append(comp.Status.CurrentSynthesis.Results, apiv1.Result{
				Message:  result.Message,
				Severity: result.Severity,
				Tags:     result.Tags,
			})
		}

		err = e.Writer.Status().Update(ctx, comp)
		if err != nil {
			return err
		}

		logger.V(0).Info("composition status has been updated following successful synthesis")
		return nil
	})
}
