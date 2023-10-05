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

	goal, goalByName, err := g.buildOutputs(ctx, comp, outputJson)
	if err != nil {
		return err
	}

	err = g.reconcileNegative(ctx, comp, goalByName)
	if err != nil {
		return err
	}

	err = g.reconcilePositive(ctx, comp, goal)
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

func (g *Generator) buildOutputs(ctx context.Context, comp *apiv1.Composition, buffer []byte) ([]*apiv1.GeneratedResource, map[string]*apiv1.GeneratedResource, error) {
	goal := []*apiv1.GeneratedResource{}
	goalByName := map[string]*apiv1.GeneratedResource{}
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

		res := &apiv1.GeneratedResource{}
		hash := sha256.Sum256([]byte(raw.GetName() + raw.GetKind()))
		hashStr := hex.EncodeToString(hash[:])[:7]
		res.Name = fmt.Sprintf("%s-%s", comp.Name, hashStr)

		// Fetch current state
		err := g.Client.Get(ctx, client.ObjectKeyFromObject(res), res)
		if client.IgnoreNotFound(err) != nil {
			return nil, nil, fmt.Errorf("getting current generated resource state: %w", err)
		}
		if res.Spec.DerivedGeneration == comp.Generation {
			continue // already in sync
		}

		// Mutate
		res.Spec.ReconcileInterval = comp.Spec.ReconcileInterval
		res.Labels = map[string]string{"composition": comp.Name}
		if err := controllerutil.SetControllerReference(comp, res, g.Client.Scheme()); err != nil {
			return nil, nil, fmt.Errorf("setting owner reference: %w", err)
		}
		js, err := json.Marshal(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("encoding generated resource as json: %w", err)
		}
		res.Spec.Manifest = string(js)
		res.Spec.DerivedGeneration = comp.Generation

		goalByName[res.Name] = res
		goal = append(goal, res)
	}

	rand.Shuffle(len(goal), func(i, j int) { goal[i], goal[j] = goal[j], goal[i] })
	return goal, goalByName, nil
}

func (g *Generator) reconcileNegative(ctx context.Context, comp *apiv1.Composition, goalByName map[string]*apiv1.GeneratedResource) error {
	all := &apiv1.GeneratedResourceList{}
	err := g.Client.List(ctx, all, client.MatchingLabels{"composition": comp.Name})
	if err != nil {
		return fmt.Errorf("listng current generated resource state: %w", err)
	}
	rand.Shuffle(len(all.Items), func(i, j int) { all.Items[i], all.Items[j] = all.Items[j], all.Items[i] })

	for _, res := range all.Items {
		if _, ok := goalByName[res.Name]; ok {
			continue
		}
		if res.DeletionTimestamp != nil {
			continue
		}
		err = g.Client.Delete(ctx, &res)
		if err != nil {
			return fmt.Errorf("deleting orphaned resources: %w", err)
		}
		g.Logger.Info("deleted resource", "name", res.Name, "namespace", res.Namespace, "kind", res.Kind)
	}

	return nil
}

func (g *Generator) reconcilePositive(ctx context.Context, comp *apiv1.Composition, goal []*apiv1.GeneratedResource) (err error) {
	for _, res := range goal {
		if res.ResourceVersion == "" {
			err = g.Client.Create(ctx, res)
		} else {
			err = g.Client.Update(ctx, res)
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
