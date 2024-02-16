package synthesis

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/pkg/inputs"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func (c *execController) buildInputsJson(ctx context.Context, comp *apiv1.Composition) ([]byte, error) {
	logger := logr.FromContextOrDiscard(ctx)

	var inputs []*unstructured.Unstructured
	for _, ref := range comp.Spec.Inputs {
		if ref.Resource == nil {
			continue // not a k8s resource
		}
		input := &unstructured.Unstructured{}
		input.SetName(ref.Resource.Name)
		input.SetNamespace(ref.Resource.Namespace)
		input.SetKind(ref.Resource.Kind)
		input.SetAPIVersion(ref.Resource.APIVersion)

		start := time.Now()
		err := c.client.Get(ctx, client.ObjectKeyFromObject(input), input)
		if err != nil {
			// Ideally we could stop retrying eventually here in cases where the resource doesn't exist,
			// but it isn't safe to _never_ retry (informers across types aren't ordered), and controller-runtime
			// doesn't expose the retry count.
			return nil, fmt.Errorf("getting resource %s/%s: %w", input.GetKind(), input.GetName(), err)
		}
		appendInputNameAnnotation(&ref, input)

		logger.V(1).Info("retrieved input resource", "resourceName", input.GetName(), "resourceNamespace", input.GetNamespace(), "resourceKind", input.GetKind(), "latency", time.Since(start).Milliseconds())
		inputs = append(inputs, input)
	}

	js, err := serializeInputs(inputs)
	if err != nil {
		return nil, reconcile.TerminalError(err)
	}
	return js, nil
}

func appendInputNameAnnotation(ref *apiv1.InputRef, input *unstructured.Unstructured) {
	anno := input.GetAnnotations()
	if anno == nil {
		anno = map[string]string{}
	}
	anno[inputs.InputNameAnnotationKey] = ref.Name
	input.SetAnnotations(anno)
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

		logger.V(1).Info("wrote resource slice", "resourceSliceName", slice.Name, "latency", time.Since(start).Milliseconds())
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
