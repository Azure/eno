package wrapper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
)

type Generator struct {
	Client client.Client
	Logger logr.Logger

	CompositionName       string
	CompositionNamespace  string
	CompositionGeneration int64
	GeneratorGeneration   int64

	Exec func(context.Context, []byte) ([]byte, error)
}

func (g *Generator) Generate(ctx context.Context) error {
	comp := &apiv1.Composition{}
	comp.Name = g.CompositionName
	comp.Namespace = g.CompositionNamespace
	err := g.Client.Get(ctx, client.ObjectKeyFromObject(comp), comp, &client.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting composition resource: %w", err)
	}
	if comp.Generation != g.CompositionGeneration {
		return fmt.Errorf("this job is no longer necessary - (%d != %d)", comp.Generation, g.CompositionGeneration)
	}

	inputJson, err := g.fetchInputResources(ctx, comp)
	if err != nil {
		return err
	}

	outputJson, err := g.Exec(ctx, inputJson)
	if err != nil {
		return err
	}

	goal, err := g.buildOutputs(ctx, comp, outputJson)
	if err != nil {
		return err
	}

	err = g.createSliceResources(ctx, goal)
	if err != nil {
		return err
	}

	return g.updateCompositionStatus(ctx, comp, goal)
}

func (g *Generator) fetchInputResources(ctx context.Context, comp *apiv1.Composition) ([]byte, error) {
	buffer := &bytes.Buffer{}
	enc := json.NewEncoder(buffer)

	for _, input := range comp.Spec.Inputs {
		if input.Resource == nil {
			continue
		}
		ref := &unstructured.Unstructured{}
		ref.SetAPIVersion(input.Resource.APIVersion)
		ref.SetKind(input.Resource.Kind)
		ref.SetName(input.Resource.Name)
		ref.SetNamespace(input.Resource.Namespace)

		if err := g.Client.Get(ctx, client.ObjectKeyFromObject(ref), ref); err != nil {
			return nil, fmt.Errorf("getting input resource: %w", err)
		}

		anno := ref.GetAnnotations()
		if anno == nil {
			anno = map[string]string{}
		}
		anno["eno.azure.io/input-name"] = input.Name
		ref.SetAnnotations(anno)

		if err := enc.Encode(ref); err != nil {
			return nil, fmt.Errorf("encoding input resource: %w", err)
		}
	}

	return buffer.Bytes(), nil
}

func (g *Generator) buildOutputs(ctx context.Context, comp *apiv1.Composition, buffer []byte) ([]*apiv1.GeneratedResourceSlice, error) {
	goal := []*apiv1.GeneratedResourceSlice{}
	dec := json.NewDecoder(bytes.NewBuffer(buffer))

	for {
		raw := &unstructured.Unstructured{}
		if err := dec.Decode(raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decoding generated resource json: %w", err)
		}
		if raw.GetName() == "" || raw.GetKind() == "" {
			continue // invalid resource
		}

		hash := sha256.Sum256([]byte(raw.GetName() + raw.GetKind()))
		hashStr := hex.EncodeToString(hash[:])[:7]

		res := &apiv1.GeneratedResourceSlice{}
		res.Name = fmt.Sprintf("%s-%s", comp.Name, hashStr)
		res.Namespace = comp.Namespace
		res.Labels = map[string]string{"composition": comp.Name}
		res.Spec.DerivedGeneration = comp.Generation

		controllerutil.AddFinalizer(res, "eno.azure.io/cleanup")
		if err := controllerutil.SetControllerReference(comp, res, g.Client.Scheme()); err != nil {
			return nil, fmt.Errorf("setting owner reference: %w", err)
		}

		js, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("encoding generated resource as json: %w", err)
		}
		res.Spec.Manifest = string(js)
		goal = append(goal, res)
	}

	rand.Shuffle(len(goal), func(i, j int) { goal[i], goal[j] = goal[j], goal[i] })
	return goal, nil
}

// TODO(jordan): Retry writes here to avoid re-spawning the generator process on conflict

func (g *Generator) createSliceResources(ctx context.Context, comp *apiv1.Composition, goal []*apiv1.GeneratedResourceSlice) (err error) {
	for _, res := range goal {
		err = g.Client.Create(ctx, res)
		if err != nil {
			return fmt.Errorf("storing generated resource: %w", err)
		}
		g.Logger.Info("wrote resource", "generatedResourceName", res.Name, "resourceKind", res.Kind)
	}
	return nil
}

func (g *Generator) updateCompositionStatus(ctx context.Context, comp *apiv1.Composition, goal []*apiv1.GeneratedResourceSlice) error {
	err := g.Client.Get(ctx, client.ObjectKeyFromObject(comp), comp, &client.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting composition resource: %w", err)
	}
	if comp.Generation != g.CompositionGeneration {
		return fmt.Errorf("this job is no longer necessary - (%d != %d)", comp.Generation, g.CompositionGeneration)
	}
	comp.Status.CompositionGeneration = comp.Generation
	comp.Status.GeneratorGeneration = g.GeneratorGeneration
	comp.Status.GeneratedResourceCount = int64(len(goal))
	return g.Client.Status().Update(ctx, comp)
}
