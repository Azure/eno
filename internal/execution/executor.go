package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TODO: Retries

// maxSliceJsonBytes is the max sum of a resource slice's manifests.
const maxSliceJsonBytes = 1024 * 512

type Env struct {
	CompositionName      string
	CompositionNamespace string
	SynthesisUUID        string
}

func LoadEnv() *Env {
	return &Env{
		CompositionName:      os.Getenv("COMPOSITION_NAME"),
		CompositionNamespace: os.Getenv("COMPOSITION_NAMESPACE"),
		SynthesisUUID:        os.Getenv("SYNTHESIS_UUID"),
	}
}

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

	input, err := e.buildPodInput(comp, syn)
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

	return e.updateComposition(ctx, comp, syn, sliceRefs)
}

func (e *Executor) buildPodInput(comp *apiv1.Composition, syn *apiv1.Synthesizer) (*krmv1.ResourceList, error) {
	bindings := map[string]*apiv1.Binding{}
	for _, b := range comp.Spec.Bindings {
		b := b
		bindings[b.Key] = &b
	}

	rl := &krmv1.ResourceList{
		Kind:       krmv1.ResourceListKind,
		APIVersion: krmv1.SchemeGroupVersion.String(),
	}
	for _, r := range syn.Spec.Refs {
		key := r.Key
		b, ok := bindings[key]
		if !ok {
			return nil, fmt.Errorf("input %q is referenced, but not bound", key)
		}
		// TODO: Get the full resource
		input := apiv1.NewInput(key, apiv1.InputResource{
			Name:      b.Resource.Name,
			Namespace: b.Resource.Namespace,
			Group:     r.Resource.Group,
			Kind:      r.Resource.Kind,
		})
		u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&input)
		if err != nil {
			return nil, fmt.Errorf("input %q could not be converted to Unstructured: %w", key, err)
		}
		rl.Items = append(rl.Items, &unstructured.Unstructured{Object: u})

	}
	return rl, nil
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

func (e *Executor) updateComposition(ctx context.Context, oldComp *apiv1.Composition, syn *apiv1.Synthesizer, refs []*apiv1.ResourceSliceRef) error {
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
		// TODO: Write results (error, etc.)
		// TODO: Write input version metadata
		err = e.Writer.Status().Update(ctx, comp)
		if err != nil {
			return err
		}

		logger.V(0).Info("composition status has been updated following successful synthesis")
		return nil
	})
}

type SynthesizerHandle func(context.Context, *apiv1.Synthesizer, *krmv1.ResourceList) (*krmv1.ResourceList, error)

func NewExecHandler() SynthesizerHandle {
	return func(ctx context.Context, s *apiv1.Synthesizer, rl *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		stdin := &bytes.Buffer{}
		stdout := &bytes.Buffer{}

		err := json.NewEncoder(stdin).Encode(rl)
		if err != nil {
			return nil, err
		}

		command := s.Spec.Command
		if len(command) == 0 {
			command = []string{"synthesize"}
		}

		cmd := exec.CommandContext(ctx, command[0], command[1:]...)
		cmd.Env = []string{}   // no env
		cmd.Stderr = os.Stdout // logger uses stderr, so use stdout to avoid race condition
		cmd.Stdout = stdout
		err = cmd.Run()
		if err != nil {
			return nil, err
		}

		output := &krmv1.ResourceList{}
		err = json.NewDecoder(stdout).Decode(output)
		if err != nil {
			return nil, err
		}

		return output, nil
	}
}
