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
	CompositionGeneration int64

	Exec func(context.Context, []byte) ([]byte, error)
}

func (g *Generator) Generate(ctx context.Context) error {
	comp := &apiv1.Composition{}
	comp.Name = g.CompositionName
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

	goal, names, err := g.buildOutputs(ctx, comp, outputJson)
	if err != nil {
		return err
	}

	// TODO(jordan): Pagination for this list to support high resource count
	current := &apiv1.GeneratedResourceList{}
	err = g.Client.List(ctx, current, client.MatchingLabels{"composition": comp.Name})
	if err != nil {
		return fmt.Errorf("listng current generated resource state: %w", err)
	}
	rand.Shuffle(len(current.Items), func(i, j int) { current.Items[i], current.Items[j] = current.Items[j], current.Items[i] })

	currentByName, err := g.reconcileNegative(ctx, comp, current, names)
	if err != nil {
		return err
	}

	err = g.reconcilePositive(ctx, comp, goal, currentByName)
	if err != nil {
		return err
	}

	return g.updateCompositionStatus(ctx, comp, goal)
}

func (g *Generator) fetchInputResources(ctx context.Context, comp *apiv1.Composition) ([]byte, error) {
	buffer := &bytes.Buffer{}
	enc := json.NewEncoder(buffer)

	for _, input := range comp.Spec.Inputs {
		ref := &unstructured.Unstructured{}
		ref.SetAPIVersion(input.APIVersion)
		ref.SetKind(input.Kind)
		ref.SetName(input.Name)
		ref.SetNamespace(input.Namespace)

		if err := g.Client.Get(ctx, client.ObjectKeyFromObject(ref), ref); err != nil {
			return nil, fmt.Errorf("getting input resource: %w", err)
		}
		if err := enc.Encode(ref); err != nil {
			return nil, fmt.Errorf("encoding input resource: %w", err)
		}
	}

	return buffer.Bytes(), nil
}

func (g *Generator) buildOutputs(ctx context.Context, comp *apiv1.Composition, buffer []byte) ([]*apiv1.GeneratedResource, map[string]struct{}, error) {
	goal := []*apiv1.GeneratedResource{}
	names := map[string]struct{}{}
	dec := json.NewDecoder(bytes.NewBuffer(buffer))

	for {
		raw := &unstructured.Unstructured{}
		if err := dec.Decode(raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, nil, fmt.Errorf("decoding generated resource json: %w", err)
		}
		if raw.GetName() == "" || raw.GetKind() == "" {
			continue // invalid resource
		}

		hash := sha256.Sum256([]byte(raw.GetName() + raw.GetKind()))
		hashStr := hex.EncodeToString(hash[:])[:7]

		res := &apiv1.GeneratedResource{}
		res.Name = fmt.Sprintf("%s-%s", comp.Name, hashStr)
		res.Labels = map[string]string{"composition": comp.Name}
		res.Spec.ReconcileInterval = comp.Spec.ReconcileInterval
		res.Spec.DerivedGeneration = comp.Generation

		if err := controllerutil.SetControllerReference(comp, res, g.Client.Scheme()); err != nil {
			return nil, nil, fmt.Errorf("setting owner reference: %w", err)
		}

		js, err := json.Marshal(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("encoding generated resource as json: %w", err)
		}
		res.Spec.Manifest = string(js)

		names[res.Name] = struct{}{}
		goal = append(goal, res)
	}

	rand.Shuffle(len(goal), func(i, j int) { goal[i], goal[j] = goal[j], goal[i] })
	return goal, names, nil
}

// TODO(jordan): Retry writes here to avoid re-spawning the generator process on conflict

func (g *Generator) reconcileNegative(ctx context.Context, comp *apiv1.Composition, current *apiv1.GeneratedResourceList, names map[string]struct{}) (map[string]*apiv1.GeneratedResource, error) {
	byName := map[string]*apiv1.GeneratedResource{}

	for _, res := range current.Items {
		res := res
		byName[res.Name] = &res

		if _, ok := names[res.Name]; ok {
			continue
		}
		if res.DeletionTimestamp != nil {
			continue
		}

		err := g.Client.Delete(ctx, &res)
		if err != nil {
			return nil, fmt.Errorf("deleting orphaned resources: %w", err)
		}
		g.Logger.Info("deleted resource", "name", res.Name, "namespace", res.Namespace, "kind", res.Kind)
	}

	return byName, nil
}

func (g *Generator) reconcilePositive(ctx context.Context, comp *apiv1.Composition, goal []*apiv1.GeneratedResource, currentByName map[string]*apiv1.GeneratedResource) (err error) {
	for _, res := range goal {
		current, ok := currentByName[res.Name]

		if ok && current.Spec.DerivedGeneration == comp.Generation {
			continue // already in sync
		}

		if !ok {
			err = g.Client.Create(ctx, res)
		} else {
			next := current.DeepCopy()
			next.Spec = res.Spec // replace the entire spec — we manage it and nothing else should write to it
			err = g.Client.Update(ctx, next)
		}
		if err != nil {
			return fmt.Errorf("storing generated resource: %w", err)
		}
		g.Logger.Info("wrote resource", "name", res.Name, "namespace", res.Namespace, "kind", res.Kind)
	}
	return nil
}

func (g *Generator) updateCompositionStatus(ctx context.Context, comp *apiv1.Composition, goal []*apiv1.GeneratedResource) error {
	err := g.Client.Get(ctx, client.ObjectKeyFromObject(comp), comp, &client.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting composition resource: %w", err)
	}
	if comp.Generation != g.CompositionGeneration {
		return fmt.Errorf("this job is no longer necessary - (%d != %d)", comp.Generation, g.CompositionGeneration)
	}
	comp.Status.ObservedGeneration = comp.Generation
	comp.Status.GeneratedResourceCount = int64(len(goal))
	return g.Client.Status().Update(ctx, comp)
}
