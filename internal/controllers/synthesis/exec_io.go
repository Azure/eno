package synthesis

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func buildPodInput(comp *apiv1.Composition, syn *apiv1.Synthesizer) ([]byte, error) {
	bindings := map[string]*apiv1.Binding{}
	for _, b := range comp.Spec.Bindings {
		bindings[b.Key] = &b
	}
	refs := map[string]*apiv1.Ref{}
	for _, r := range syn.Spec.Refs {
		refs[r.Key] = &r
	}

	inputs := []*unstructured.Unstructured{}
	for key, r := range refs {
		b, ok := bindings[key]
		if !ok {
			return nil, fmt.Errorf("input %q is referenced, but not bound", key)
		}
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
		inputs = append(inputs, &unstructured.Unstructured{Object: u})

	}
	return serializeInputs(inputs)
}

func serializeInputs(inputs []*unstructured.Unstructured) ([]byte, error) {
	rl := &krmv1.ResourceList{
		Kind:       krmv1.ResourceListKind,
		APIVersion: krmv1.SchemeGroupVersion.String(),
		Items:      inputs,
	}
	b, err := json.Marshal(rl)
	if err != nil {
		return nil, err
	}
	return b, err
}

func (c *execController) writeOutputToSlices(ctx context.Context, comp *apiv1.Composition, stdout io.Reader) ([]*apiv1.ResourceSliceRef, error) {
	logger := logr.FromContextOrDiscard(ctx)

	outputs, err := deserializeOutputs(stdout)
	if err != nil {
		return nil, reconcile.TerminalError(fmt.Errorf("parsing outputs: %w", err))
	}

	previous, err := c.fetchPreviousSlices(ctx, comp)
	if err != nil {
		return nil, err
	}

	slices, err := buildResourceSlices(comp, previous, outputs, maxSliceJsonBytes)
	if err != nil {
		return nil, err
	}

	sliceRefs := make([]*apiv1.ResourceSliceRef, len(slices))
	for i, slice := range slices {
		start := time.Now()

		err = c.writeResourceSlice(ctx, slice)
		if err != nil {
			return nil, fmt.Errorf("creating resource slice %d: %w", i, err)
		}

		logger.V(0).Info("wrote resource slice", "resourceSliceName", slice.Name, "latency", time.Since(start).Milliseconds())
		sliceRefs[i] = &apiv1.ResourceSliceRef{Name: slice.Name}
	}

	return sliceRefs, nil
}

func deserializeOutputs(r io.Reader) ([]*unstructured.Unstructured, error) {
	rl := &krmv1.ResourceList{}
	err := json.NewDecoder(r).Decode(&rl)
	if err != nil {
		return nil, reconcile.TerminalError(fmt.Errorf("parsing outputs: %w", err))
	}
	return rl.Items, nil
}
