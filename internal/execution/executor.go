package execution

import (
	"context"
	"fmt"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/inputs"
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
	logger := logr.FromContextOrDiscard(ctx)

	comp := &apiv1.Composition{}
	comp.Name = env.CompositionName
	comp.Namespace = env.CompositionNamespace
	err := e.Reader.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	if err != nil {
		return fmt.Errorf("fetching composition: %w", err)
	}

	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err = e.Reader.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	if err != nil {
		return fmt.Errorf("fetching synthesizer: %w", err)
	}

	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "synthesizerName", syn.Name)
	ctx = logr.NewContext(ctx, logger)

	if reason, skip := skipSynthesis(comp, syn, env); skip {
		logger.V(0).Info("synthesis is no longer relevant - skipping", "reason", reason)
		return nil
	}

	input, revs, err := e.buildPodInput(ctx, comp, syn)
	if err != nil {
		return fmt.Errorf("building synthesizer input: %w", err)
	}

	var sliceRefs []*apiv1.ResourceSliceRef
	output, err := e.Handler(ctx, syn, input)
	if err != nil {
		logger.Error(err, "unable to execute synthesizer")

		output = &krmv1.ResourceList{Results: []*krmv1.Result{{
			Message:  "Synthesizer error: " + err.Error(),
			Severity: krmv1.ResultSeverityError,
		}}}

		if err := e.updateComposition(ctx, env, comp, syn, sliceRefs, revs, output); err != nil {
			return err
		}
		return err
	}

	err = findResultError(output)
	if err := e.preflightValidateResources(output); err != nil {
		return err
	}
	if err == nil {
		sliceRefs, err = e.writeSlices(ctx, comp, output)
		if err != nil {
			return err
		}
	}

	if err := e.updateComposition(ctx, env, comp, syn, sliceRefs, revs, output); err != nil {
		return err
	}
	return err
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
		FunctionConfig: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion":   "v1",
				"kind":         "ConfigMap",
				"optionalRefs": []string{},
			},
		},
	}
	revs := []apiv1.InputRevisions{}
	for _, r := range syn.Spec.Refs {
		key := r.Key

		// Track all optional refs in FunctionConfig
		if r.Optional {
			optRefs, _, _ := unstructured.NestedStringSlice(rl.FunctionConfig.Object, "optionalRefs")
			optRefs = append(optRefs, key)
			unstructured.SetNestedStringSlice(rl.FunctionConfig.Object, optRefs, "optionalRefs")
		}

		// Get the resource
		start := time.Now()
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(schema.GroupVersionKind{Group: r.Resource.Group, Version: r.Resource.Version, Kind: r.Resource.Kind})
		b, ok := bindings[key]
		if ok {
			obj.SetName(b.Resource.Name)
			obj.SetNamespace(b.Resource.Namespace)
		} else {
			obj.SetName(r.Resource.Name)
			obj.SetNamespace(r.Resource.Namespace)
		}

		err := e.Reader.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		if err != nil {
			// If the ref is optional and the resource is not found, skip it
			if r.Optional && errors.IsNotFound(err) {
				logger.V(1).Info("skipping optional input that was not found", "key", key)
				continue
			}
			return nil, nil, fmt.Errorf("getting resource for ref %q: %w", key, err)
		}
		anno := obj.GetAnnotations()
		if anno == nil {
			anno = map[string]string{}
		}
		anno["eno.azure.io/input-key"] = key
		obj.SetAnnotations(anno)
		rl.Items = append(rl.Items, obj)
		logger.V(0).Info("retrieved input", "key", key, "latency", time.Since(start).Abs().Milliseconds())

		// Store the revision to be written to the synthesis status later
		revs = append(revs, *apiv1.NewInputRevisions(obj, key))
	}

	return rl, revs, nil
}

func (e *Executor) preflightValidateResources(rl *krmv1.ResourceList) error {
	for i, obj := range rl.Items {
		_, err := resource.FromUnstructured(obj)
		if err != nil {
			return fmt.Errorf("parsing resource at index %d: %w", i, err)
		}
	}
	return nil
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

		logger.V(1).Info("wrote resource slice", "resourceSliceName", slice.Name, "latency", time.Since(start).Milliseconds())
		sliceRefs[i] = &apiv1.ResourceSliceRef{Name: slice.Name}
	}

	return sliceRefs, nil
}

// fetchPreviousSlices retrieves the previous slices from the composition's current synthesis status.
// This function runs before the updateComposition function, which will later swap the current synthesis
// to become the previous synthesis. Therefore, the resourceslice retrieved from the current synthesis is
// actually the "previous" resource slices after the update is complete.
func (e *Executor) fetchPreviousSlices(ctx context.Context, comp *apiv1.Composition) ([]*apiv1.ResourceSlice, error) {
	if comp.Status.CurrentSynthesis == nil {
		return nil, nil // nothing to fetch
	}
	logger := logr.FromContextOrDiscard(ctx)

	slices := []*apiv1.ResourceSlice{}
	for _, ref := range comp.Status.CurrentSynthesis.ResourceSlices {
		slice := &apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := e.Reader.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		if errors.IsNotFound(err) {
			logger.Error(nil, "resource slice referenced by composition was not found - skipping", "resourceSliceName", slice.Name)
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

func (e *Executor) updateComposition(ctx context.Context, env *Env, oldComp *apiv1.Composition, syn *apiv1.Synthesizer, refs []*apiv1.ResourceSliceRef, revs []apiv1.InputRevisions, rl *krmv1.ResourceList) error {
	logger := logr.FromContextOrDiscard(ctx)
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		comp := &apiv1.Composition{}
		err := e.Reader.Get(ctx, client.ObjectKeyFromObject(oldComp), comp)
		if err != nil {
			return err
		}

		now := metav1.Now()
		comp.Status.InFlightSynthesis.Synthesized = &now
		comp.Status.InFlightSynthesis.ResourceSlices = refs
		comp.Status.InFlightSynthesis.ObservedSynthesizerGeneration = syn.Generation
		comp.Status.InFlightSynthesis.InputRevisions = revs
		comp.Status.InFlightSynthesis.Results = nil
		for _, result := range rl.Results {
			comp.Status.InFlightSynthesis.Results = append(comp.Status.InFlightSynthesis.Results, apiv1.Result{
				Message:  result.Message,
				Severity: result.Severity,
				Tags:     result.Tags,
			})
		}

		if reason, skip := skipSynthesis(comp, syn, env); skip {
			logger.V(0).Info("synthesis is no longer relevant - discarding its output", "reason", reason)
			return nil
		}

		// Swap pending->current->previous syntheses
		if findResultError(rl) == nil {
			comp.Status.PreviousSynthesis = comp.Status.CurrentSynthesis
			comp.Status.CurrentSynthesis = comp.Status.InFlightSynthesis
			comp.Status.InFlightSynthesis = nil
		}

		err = e.Writer.Status().Update(ctx, comp)
		if err != nil {
			return err
		}

		logger.V(0).Info("composition status has been updated following successful synthesis")
		return nil
	})
}

func skipSynthesis(comp *apiv1.Composition, syn *apiv1.Synthesizer, env *Env) (string, bool) {
	synthesis := comp.Status.InFlightSynthesis
	if synthesis == nil {
		return "MissingSynthesis", true
	}
	if synthesis.UUID != env.SynthesisUUID {
		return "UUIDMismatch", true
	}
	if synthesis.Canceled != nil {
		return "SynthesisCanceled", true
	}
	if inputs.OutOfLockstep(syn, comp, synthesis.InputRevisions) {
		return "InputsOutOfLockstep", true
	}
	if env.Image != "" && env.Image != syn.Spec.Image {
		return "ImageMismatch", true
	}
	return "", false
}

func findResultError(rl *krmv1.ResourceList) error {
	if rl == nil {
		return nil
	}
	for _, res := range rl.Results {
		if res.Severity == krmv1.ResultSeverityError {
			return fmt.Errorf("result: %s", res.Message)
		}
	}
	return nil
}
