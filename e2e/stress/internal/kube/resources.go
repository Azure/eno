// Dynamic resource operations used by the stress runner.
package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/Azure/eno/e2e/stress/internal/render"
)

type Resource struct {
	Interface dynamic.ResourceInterface
	GVR       schema.GroupVersionResource
	Scope     string
}

func (c *Client) ResourceFor(object *unstructured.Unstructured) (*Resource, error) {
	gvk := object.GroupVersionKind()
	mapping, err := c.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, fmt.Errorf("mapping %s: %w", gvk.String(), err)
	}
	resource := c.Dynamic.Resource(mapping.Resource)
	if mapping.Scope.Name() == "namespace" {
		return &Resource{Interface: resource.Namespace(object.GetNamespace()), GVR: mapping.Resource, Scope: "namespace"}, nil
	}
	return &Resource{Interface: resource, GVR: mapping.Resource, Scope: "cluster"}, nil
}

func (c *Client) CreateOwned(ctx context.Context, object *unstructured.Unstructured, runID string, dryRun bool) (*unstructured.Unstructured, *Resource, error) {
	return c.CreateSetup(ctx, object, runID, dryRun, false)
}

func (c *Client) CreateSetup(ctx context.Context, object *unstructured.Unstructured, runID string, dryRun, reuse bool) (*unstructured.Unstructured, *Resource, error) {
	resource, err := c.ResourceFor(object)
	if err != nil {
		return nil, nil, err
	}
	options := metav1.CreateOptions{FieldManager: "eno-stress"}
	if dryRun {
		options.DryRun = []string{metav1.DryRunAll}
	}
	created, err := resource.Interface.Create(ctx, object, options)
	if err == nil {
		return created, resource, nil
	}
	if !apierrors.IsAlreadyExists(err) || dryRun {
		return nil, nil, err
	}
	existing, getErr := resource.Interface.Get(ctx, object.GetName(), metav1.GetOptions{})
	if getErr != nil {
		return nil, nil, fmt.Errorf("getting existing resource after create conflict: %w", getErr)
	}
	if existing.GetLabels()[render.RunIDLabel] == runID {
		return existing, resource, nil
	}
	if !reuse {
		return nil, nil, fmt.Errorf("refusing to adopt unowned %s %s/%s", object.GetKind(), object.GetNamespace(), object.GetName())
	}
	candidate := object.DeepCopy()
	candidate.SetResourceVersion(existing.GetResourceVersion())
	validated, updateErr := resource.Interface.Update(ctx, candidate, metav1.UpdateOptions{
		DryRun:       []string{metav1.DryRunAll},
		FieldManager: "eno-stress-reuse-check",
	})
	if updateErr != nil {
		return nil, nil, fmt.Errorf("validating reusable %s %s/%s: %w", object.GetKind(), object.GetNamespace(), object.GetName(), updateErr)
	}
	existingSpec, _, _ := unstructured.NestedFieldNoCopy(existing.Object, "spec")
	validatedSpec, _, _ := unstructured.NestedFieldNoCopy(validated.Object, "spec")
	if !reflect.DeepEqual(existingSpec, validatedSpec) {
		return nil, nil, fmt.Errorf("refusing to reuse changed %s %s/%s", object.GetKind(), object.GetNamespace(), object.GetName())
	}
	return existing, resource, nil
}

func (c *Client) ApplyOwned(ctx context.Context, object *unstructured.Unstructured, runID string) (*unstructured.Unstructured, *Resource, error) {
	resource, err := c.ResourceFor(object)
	if err != nil {
		return nil, nil, err
	}
	existing, err := resource.Interface.Get(ctx, object.GetName(), metav1.GetOptions{})
	if err == nil && existing.GetLabels()[render.RunIDLabel] != runID {
		return nil, nil, fmt.Errorf("refusing to update unowned %s %s/%s", object.GetKind(), object.GetNamespace(), object.GetName())
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, nil, err
	}
	raw, err := json.Marshal(object.Object)
	if err != nil {
		return nil, nil, err
	}
	force := true
	applied, err := resource.Interface.Patch(ctx, object.GetName(), types.ApplyPatchType, raw, metav1.PatchOptions{
		FieldManager: "eno-stress",
		Force:        &force,
	})
	return applied, resource, err
}

func (c *Client) DeleteOwned(ctx context.Context, gvr schema.GroupVersionResource, namespace, name, uid, runID string) error {
	resource := c.Dynamic.Resource(gvr)
	var resourceInterface dynamic.ResourceInterface = resource
	if namespace != "" {
		resourceInterface = resource.Namespace(namespace)
	}
	existing, err := resourceInterface.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if existing.GetLabels()[render.RunIDLabel] != runID || string(existing.GetUID()) != uid {
		return fmt.Errorf("refusing to delete %s/%s because ownership or UID changed", namespace, name)
	}
	uidValue := types.UID(uid)
	return resourceInterface.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uidValue}})
}
