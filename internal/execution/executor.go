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
	logger.V(1).Info("starting synthesis execution",
		"compositionName", env.CompositionName,
		"compositionNamespace", env.CompositionNamespace,
		"synthesisUUID", env.SynthesisUUID,
		"image", env.Image)

	comp := &apiv1.Composition{}
	comp.Name = env.CompositionName
	comp.Namespace = env.CompositionNamespace

	logger.V(1).Info("fetching composition from API server", "compositionKey", client.ObjectKeyFromObject(comp))
	err := e.Reader.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	if err != nil {
		logger.Error(err, "failed to fetch composition from API server", "compositionKey", client.ObjectKeyFromObject(comp))
		return fmt.Errorf("fetching composition: %w", err)
	}
	logger.V(1).Info("successfully fetched composition", "generation", comp.Generation, "resourceVersion", comp.ResourceVersion)

	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name

	logger.V(1).Info("fetching synthesizer from API server", "synthesizerName", syn.Name)
	err = e.Reader.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	if err != nil {
		logger.Error(err, "failed to fetch synthesizer from API server", "synthesizerName", syn.Name)
		return fmt.Errorf("fetching synthesizer: %w", err)
	}
	logger.V(1).Info("successfully fetched synthesizer", "synthesizerName", syn.Name, "generation", syn.Generation, "image", syn.Spec.Image)

	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "synthesizerName", syn.Name)
	ctx = logr.NewContext(ctx, logger)

	if reason, skip := skipSynthesis(comp, syn, env); skip {
		logger.V(0).Info("synthesis is no longer relevant - skipping", "reason", reason, "skipReason", reason)
		return nil
	}
	logger.V(1).Info("synthesis validation passed - proceeding with synthesis")

	logger.V(1).Info("building pod input for synthesizer execution")
	input, revs, err := e.buildPodInput(ctx, comp, syn)
	if err != nil {
		logger.Error(err, "failed to build synthesizer input", "numRefs", len(syn.Spec.Refs))
		return fmt.Errorf("building synthesizer input: %w", err)
	}
	logger.V(1).Info("successfully built pod input", "inputItemCount", len(input.Items), "inputRevisionCount", len(revs))

	var sliceRefs []*apiv1.ResourceSliceRef
	logger.V(1).Info("executing synthesizer handler")
	output, err := e.Handler(ctx, syn, input)
	if err != nil {
		logger.Error(err, "synthesizer execution failed", "inputItemCount", len(input.Items))

		output = &krmv1.ResourceList{Results: []*krmv1.Result{{
			Message:  "Synthesizer error: " + err.Error(),
			Severity: krmv1.ResultSeverityError,
		}}}
		logger.V(1).Info("created error output for failed synthesis", "errorMessage", err.Error())

		if err := e.updateComposition(ctx, env, comp, syn, sliceRefs, revs, output); err != nil {
			logger.Error(err, "failed to update composition after synthesizer error")
			return err
		}
		return err
	}
	logger.V(1).Info("synthesizer execution completed successfully", "outputItemCount", len(output.Items), "outputResultCount", len(output.Results))

	err = findResultError(output)
	logger.V(1).Info("checking for result errors in output", "hasError", err != nil)

	logger.V(1).Info("performing preflight validation on output resources")
	if err := e.preflightValidateResources(output); err != nil {
		logger.Error(err, "preflight validation failed", "outputItemCount", len(output.Items))
		return err
	}
	logger.V(1).Info("preflight validation passed")

	if err == nil {
		logger.V(1).Info("writing resource slices", "outputItemCount", len(output.Items))
		sliceRefs, err = e.writeSlices(ctx, comp, output)
		if err != nil {
			logger.Error(err, "failed to write resource slices", "outputItemCount", len(output.Items))
			return err
		}
		logger.V(1).Info("successfully wrote resource slices", "sliceCount", len(sliceRefs))
	} else {
		logger.V(1).Info("skipping slice writing due to result error", "error", err)
	}

	logger.V(1).Info("updating composition status with synthesis results", "hasSliceRefs", len(sliceRefs) > 0, "hasResultError", err != nil)
	if err := e.updateComposition(ctx, env, comp, syn, sliceRefs, revs, output); err != nil {
		logger.Error(err, "failed to update composition status")
		return err
	}
	logger.V(1).Info("synthesis execution completed", "success", err == nil)
	return err
}

func (e *Executor) buildPodInput(ctx context.Context, comp *apiv1.Composition, syn *apiv1.Synthesizer) (*krmv1.ResourceList, []apiv1.InputRevisions, error) {
	logger := logr.FromContextOrDiscard(ctx)
	logger.V(1).Info("building pod input for synthesis", "synthesizerRefCount", len(syn.Spec.Refs), "compositionBindingCount", len(comp.Spec.Bindings))

	bindings := map[string]*apiv1.Binding{}
	for _, b := range comp.Spec.Bindings {
		b := b
		bindings[b.Key] = &b
		logger.V(3).Info("indexed binding", "key", b.Key, "resourceName", b.Resource.Name, "resourceNamespace", b.Resource.Namespace)
	}
	logger.V(1).Info("indexed all bindings", "bindingCount", len(bindings))

	rl := &krmv1.ResourceList{
		Kind:       krmv1.ResourceListKind,
		APIVersion: krmv1.SchemeGroupVersion.String(),
	}
	revs := []apiv1.InputRevisions{}
	logger.V(1).Info("processing synthesizer refs", "refCount", len(syn.Spec.Refs))

	for i, r := range syn.Spec.Refs {
		key := r.Key
		logger.V(3).Info("processing synthesizer ref", "index", i, "key", key, "resourceGVK", fmt.Sprintf("%s/%s/%s", r.Resource.Group, r.Resource.Version, r.Resource.Kind))

		// Get the resource
		start := time.Now()
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(schema.GroupVersionKind{Group: r.Resource.Group, Version: r.Resource.Version, Kind: r.Resource.Kind})

		b, ok := bindings[key]
		if ok {
			obj.SetName(b.Resource.Name)
			obj.SetNamespace(b.Resource.Namespace)
			logger.V(3).Info("using binding for ref", "key", key, "bindingName", b.Resource.Name, "bindingNamespace", b.Resource.Namespace)
		} else {
			obj.SetName(r.Resource.Name)
			obj.SetNamespace(r.Resource.Namespace)
			logger.V(3).Info("using direct ref (no binding)", "key", key, "refName", r.Resource.Name, "refNamespace", r.Resource.Namespace)
		}

		logger.V(3).Info("fetching input resource", "key", key, "objectKey", client.ObjectKeyFromObject(obj))
		err := e.Reader.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		if err != nil {
			logger.Error(err, "failed to get input resource", "key", key, "objectKey", client.ObjectKeyFromObject(obj))
			return nil, nil, fmt.Errorf("getting resource for ref %q: %w", key, err)
		}

		fetchLatency := time.Since(start)
		logger.V(3).Info("successfully fetched input resource", "key", key, "resourceVersion", obj.GetResourceVersion(), "fetchLatencyMs", fetchLatency.Milliseconds())

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
		logger.V(3).Info("stored input revision", "key", key, "uid", obj.GetUID(), "resourceVersion", obj.GetResourceVersion())
	}

	logger.V(1).Info("completed building pod input", "totalItems", len(rl.Items), "totalRevisions", len(revs))
	return rl, revs, nil
}

func (e *Executor) preflightValidateResources(rl *krmv1.ResourceList) error {
	logger := logr.FromContextOrDiscard(context.Background())
	logger.V(1).Info("starting preflight validation of resources", "resourceCount", len(rl.Items))

	for i, obj := range rl.Items {
		logger.V(3).Info("validating resource", "index", i, "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace())
		_, err := resource.FromUnstructured(obj)
		if err != nil {
			logger.Error(err, "preflight validation failed for resource", "index", i, "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace())
			return fmt.Errorf("parsing resource at index %d: %w", i, err)
		}
		logger.V(3).Info("resource validation passed", "index", i, "kind", obj.GetKind(), "name", obj.GetName())
	}

	logger.V(1).Info("preflight validation completed successfully", "validatedResourceCount", len(rl.Items))
	return nil
}

func (e *Executor) writeSlices(ctx context.Context, comp *apiv1.Composition, rl *krmv1.ResourceList) ([]*apiv1.ResourceSliceRef, error) {
	logger := logr.FromContextOrDiscard(ctx)
	logger.V(1).Info("starting to write resource slices", "inputResourceCount", len(rl.Items), "maxSliceBytes", maxSliceJsonBytes)

	logger.V(1).Info("fetching previous resource slices")
	previous, err := e.fetchPreviousSlices(ctx, comp)
	if err != nil {
		logger.Error(err, "failed to fetch previous resource slices")
		return nil, err
	}
	logger.V(1).Info("fetched previous resource slices", "previousSliceCount", len(previous))

	logger.V(1).Info("slicing resources", "inputResourceCount", len(rl.Items), "previousSliceCount", len(previous))
	slices, err := resource.Slice(comp, previous, rl.Items, maxSliceJsonBytes)
	if err != nil {
		logger.Error(err, "failed to slice resources", "inputResourceCount", len(rl.Items))
		return nil, err
	}
	logger.V(1).Info("resources sliced successfully", "resultingSliceCount", len(slices))

	sliceRefs := make([]*apiv1.ResourceSliceRef, len(slices))
	for i, slice := range slices {
		start := time.Now()
		logger.V(3).Info("writing resource slice", "sliceIndex", i, "sliceName", slice.Name, "resourceCount", len(slice.Spec.Resources))

		err = e.writeResourceSlice(ctx, slice)
		if err != nil {
			logger.Error(err, "failed to write resource slice", "sliceIndex", i, "sliceName", slice.Name)
			return nil, fmt.Errorf("creating resource slice %d: %w", i, err)
		}

		writeLatency := time.Since(start)
		logger.V(1).Info("wrote resource slice", "resourceSliceName", slice.Name, "latency", writeLatency.Milliseconds())
		logger.V(3).Info("resource slice written successfully", "sliceIndex", i, "sliceName", slice.Name, "writeLatencyMs", writeLatency.Milliseconds())
		sliceRefs[i] = &apiv1.ResourceSliceRef{Name: slice.Name}
	}

	logger.V(1).Info("completed writing all resource slices", "totalSlicesWritten", len(sliceRefs))
	return sliceRefs, nil
}

// fetchPreviousSlices retrieves the previous slices from the composition's current synthesis status.
// This function runs before the updateComposition function, which will later swap the current synthesis
// to become the previous synthesis. Therefore, the resourceslice retrieved from the current synthesis is
// actually the "previous" resource slices after the update is complete.
func (e *Executor) fetchPreviousSlices(ctx context.Context, comp *apiv1.Composition) ([]*apiv1.ResourceSlice, error) {
	logger := logr.FromContextOrDiscard(ctx)

	if comp.Status.CurrentSynthesis == nil {
		logger.V(1).Info("no current synthesis found - no previous slices to fetch")
		return nil, nil // nothing to fetch
	}

	currentSynthesis := comp.Status.CurrentSynthesis
	logger.V(1).Info("fetching previous slices from current synthesis",
		"currentSynthesisUUID", currentSynthesis.UUID,
		"currentSliceRefCount", len(currentSynthesis.ResourceSlices))

	slices := []*apiv1.ResourceSlice{}
	for i, ref := range currentSynthesis.ResourceSlices {
		logger.V(3).Info("fetching previous slice", "index", i, "sliceName", ref.Name)

		slice := &apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace

		err := e.Reader.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		if errors.IsNotFound(err) {
			logger.Error(nil, "resource slice referenced by composition was not found - skipping",
				"resourceSliceName", slice.Name,
				"compositionName", comp.Name,
				"compositionNamespace", comp.Namespace)
			continue
		}
		if err != nil {
			logger.Error(err, "failed to fetch previous resource slice",
				"resourceSliceName", slice.Name,
				"sliceIndex", i)
			return nil, fmt.Errorf("fetching current resource slice %q: %w", slice.Name, err)
		}

		logger.V(3).Info("successfully fetched previous slice",
			"index", i,
			"sliceName", slice.Name,
			"resourceCount", len(slice.Spec.Resources))
		slices = append(slices, slice)
	}

	logger.V(1).Info("completed fetching previous slices",
		"requestedSliceCount", len(currentSynthesis.ResourceSlices),
		"fetchedSliceCount", len(slices))
	return slices, nil
}

func (e *Executor) writeResourceSlice(ctx context.Context, slice *apiv1.ResourceSlice) error {
	logger := logr.FromContextOrDiscard(ctx)

	var bytes int
	for _, res := range slice.Spec.Resources {
		bytes += len(res.Manifest)
	}

	logger.V(3).Info("writing resource slice to API server",
		"sliceName", slice.Name,
		"resourceCount", len(slice.Spec.Resources),
		"totalBytes", bytes)

	// We retry on request timeouts to avoid the overhead of re-synthesizing in cases where we're sometimes unable to reach apiserver
	return retry.OnError(retry.DefaultRetry, errors.IsServerTimeout, func() error {
		logger.V(3).Info("attempting to create resource slice", "sliceName", slice.Name)
		err := e.Writer.Create(ctx, slice)
		if err != nil {
			logger.Error(err, "error while creating resource slice - will retry later",
				"sliceName", slice.Name,
				"resourceCount", len(slice.Spec.Resources),
				"totalBytes", bytes)
			return err
		}
		logger.V(3).Info("successfully created resource slice",
			"sliceName", slice.Name,
			"resourceVersion", slice.ResourceVersion)
		return nil
	})
}

func (e *Executor) updateComposition(ctx context.Context, env *Env, oldComp *apiv1.Composition, syn *apiv1.Synthesizer, refs []*apiv1.ResourceSliceRef, revs []apiv1.InputRevisions, rl *krmv1.ResourceList) error {
	logger := logr.FromContextOrDiscard(ctx)
	logger.V(1).Info("starting composition status update",
		"sliceRefCount", len(refs),
		"inputRevisionCount", len(revs),
		"resultCount", len(rl.Results))

	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		logger.V(3).Info("fetching latest composition for status update")
		comp := &apiv1.Composition{}
		err := e.Reader.Get(ctx, client.ObjectKeyFromObject(oldComp), comp)
		if err != nil {
			logger.Error(err, "failed to fetch composition for status update")
			return err
		}
		logger.V(3).Info("fetched latest composition",
			"generation", comp.Generation,
			"resourceVersion", comp.ResourceVersion)

		now := metav1.Now()
		logger.V(3).Info("updating in-flight synthesis status", "synthesizedTime", now)
		comp.Status.InFlightSynthesis.Synthesized = &now
		comp.Status.InFlightSynthesis.ResourceSlices = refs
		comp.Status.InFlightSynthesis.ObservedSynthesizerGeneration = syn.Generation
		comp.Status.InFlightSynthesis.InputRevisions = revs
		comp.Status.InFlightSynthesis.Results = nil

		logger.V(3).Info("processing synthesis results", "resultCount", len(rl.Results))
		for i, result := range rl.Results {
			logger.V(4).Info("processing result", "index", i, "severity", result.Severity, "message", result.Message)
			comp.Status.InFlightSynthesis.Results = append(comp.Status.InFlightSynthesis.Results, apiv1.Result{
				Message:  result.Message,
				Severity: result.Severity,
				Tags:     result.Tags,
			})
		}

		if reason, skip := skipSynthesis(comp, syn, env); skip {
			logger.V(0).Info("synthesis is no longer relevant - discarding its output", "reason", reason, "skipReason", reason)
			return nil
		}

		// Swap pending->current->previous syntheses
		resultErr := findResultError(rl)
		if resultErr == nil {
			logger.V(1).Info("no result errors found - swapping synthesis states")
			comp.Status.PreviousSynthesis = comp.Status.CurrentSynthesis
			comp.Status.CurrentSynthesis = comp.Status.InFlightSynthesis
			comp.Status.InFlightSynthesis = nil
			logger.V(3).Info("synthesis states swapped successfully",
				"hasPreviousSynthesis", comp.Status.PreviousSynthesis != nil,
				"hasCurrentSynthesis", comp.Status.CurrentSynthesis != nil,
				"hasInFlightSynthesis", comp.Status.InFlightSynthesis != nil)
		} else {
			logger.V(1).Info("result errors found - keeping synthesis in-flight", "resultError", resultErr)
		}

		logger.V(3).Info("updating composition status")
		err = e.Writer.Status().Update(ctx, comp)
		if err != nil {
			logger.Error(err, "failed to update composition status")
			return err
		}

		logger.V(0).Info("composition status has been updated following successful synthesis")
		logger.V(1).Info("composition status update completed successfully",
			"newResourceVersion", comp.ResourceVersion,
			"synthesisSwapped", resultErr == nil)
		return nil
	})
}

func skipSynthesis(comp *apiv1.Composition, syn *apiv1.Synthesizer, env *Env) (string, bool) {
	logger := logr.FromContextOrDiscard(context.Background())

	synthesis := comp.Status.InFlightSynthesis
	if synthesis == nil {
		logger.V(3).Info("skipping synthesis - no in-flight synthesis found")
		return "MissingSynthesis", true
	}

	if synthesis.UUID != env.SynthesisUUID {
		logger.V(3).Info("skipping synthesis - UUID mismatch",
			"expectedUUID", env.SynthesisUUID,
			"actualUUID", synthesis.UUID)
		return "UUIDMismatch", true
	}

	if synthesis.Canceled != nil {
		logger.V(3).Info("skipping synthesis - synthesis was canceled",
			"canceledAt", synthesis.Canceled,
			"synthesisUUID", synthesis.UUID)
		return "SynthesisCanceled", true
	}

	if inputs.OutOfLockstep(syn, comp, synthesis.InputRevisions) {
		logger.V(3).Info("skipping synthesis - inputs out of lockstep",
			"synthesizerGeneration", syn.Generation,
			"inputRevisionCount", len(synthesis.InputRevisions))
		return "InputsOutOfLockstep", true
	}

	if env.Image != "" && env.Image != syn.Spec.Image {
		logger.V(3).Info("skipping synthesis - image mismatch",
			"expectedImage", env.Image,
			"synthesizerImage", syn.Spec.Image)
		return "ImageMismatch", true
	}

	logger.V(3).Info("synthesis validation passed - not skipping")
	return "", false
}

func findResultError(rl *krmv1.ResourceList) error {
	logger := logr.FromContextOrDiscard(context.Background())

	if rl == nil {
		logger.V(3).Info("no resource list provided - no result errors")
		return nil
	}

	logger.V(3).Info("checking for result errors", "resultCount", len(rl.Results))
	for i, res := range rl.Results {
		logger.V(4).Info("checking result", "index", i, "severity", res.Severity, "message", res.Message)
		if res.Severity == krmv1.ResultSeverityError {
			logger.V(3).Info("found result error", "index", i, "message", res.Message)
			return fmt.Errorf("result: %s", res.Message)
		}
	}

	logger.V(3).Info("no result errors found")
	return nil
}
