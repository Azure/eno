package resource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/readiness"
	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var patchGVK = schema.GroupVersionKind{
	Group:   "eno.azure.io",
	Version: "v1",
	Kind:    "Patch",
}

// Ref refers to a specific synthesized resource.
type Ref struct {
	Name, Namespace, Group, Kind string
}

// ManifestRef references a particular resource manifest within a resource slice.
type ManifestRef struct {
	Slice types.NamespacedName
	Index int // position of this manifest within the slice
}

// Resource is the controller's internal representation of a single resource out of a ResourceSlice.
type Resource struct {
	lastSeenMeta
	lastReconciledMeta

	Ref               Ref
	Manifest          *apiv1.Manifest
	ManifestRef       ManifestRef
	ReconcileInterval *metav1.Duration
	GVK               schema.GroupVersionKind
	SliceDeleted      bool
	ReadinessChecks   readiness.Checks
	Patch             jsonpatch.Patch
	DisableUpdates    bool
	ReadinessGroup    int

	// DefinedGroupKind is set on CRDs to represent the resource type they define.
	DefinedGroupKind *schema.GroupKind
}

func (r *Resource) Deleted() bool {
	return r.SliceDeleted || r.Manifest.Deleted || (r.Patch != nil && r.patchSetsDeletionTimestamp())
}

func (r *Resource) Parse() (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}
	return u, u.UnmarshalJSON([]byte(r.Manifest.Manifest))
}

// Finalize converts the resource to its struct representation and returns that value encoded as json.
// If the resource doesn't correspond to a built in type supported by the kubectl scheme the literal manifest is returned.
//
// Note that this means Eno is not completely opaque - it has some "understanding" of the built in types.
// Hopefully we can replace this with the a different approach backed by the openapi spec at some point,
// like github.com/kubernetes-sigs/structured-merge-diff. But I don't think it works for our purposes at the moment.
func (r *Resource) Finalize() ([]byte, error) {
	if r == nil {
		return nil, nil
	}

	typed, _, err := scheme.Codecs.UniversalDeserializer().Decode([]byte(r.Manifest.Manifest), &r.GVK, nil)
	if err != nil {
		// fall back to unstructured
		return []byte(r.Manifest.Manifest), nil
	}

	buf := &bytes.Buffer{}
	err = unstructured.UnstructuredJSONScheme.Encode(typed, buf)
	return buf.Bytes(), err
}

func (r *Resource) FindStatus(slice *apiv1.ResourceSlice) *apiv1.ResourceState {
	if len(slice.Status.Resources) <= r.ManifestRef.Index {
		return nil
	}
	state := slice.Status.Resources[r.ManifestRef.Index]
	return &state
}

func (r *Resource) NeedsToBePatched(current *unstructured.Unstructured) bool {
	if r.Patch == nil || current == nil {
		return false
	}

	curjson, err := current.MarshalJSON()
	if err != nil {
		return false
	}

	patchedjson, err := r.Patch.Apply(curjson)
	if err != nil {
		return false
	}

	patched := &unstructured.Unstructured{}
	err = patched.UnmarshalJSON(patchedjson)
	if err != nil {
		return false
	}

	return !equality.Semantic.DeepEqual(current, patched)
}

func (r *Resource) patchSetsDeletionTimestamp() bool {
	if r.Patch == nil {
		return false
	}

	// Apply the patch to a minimally-viable unstructured resource.
	// This is needed to satisfy the validation logic of the unstructured json parser, which requires a kind/apiVersion.
	patchedjson, err := r.Patch.Apply([]byte(`{"apiVersion": "eno.azure.io/v1", "kind":"PatchPlaceholder", "metadata":{}}`))
	if err != nil {
		return false
	}

	patched := map[string]any{}
	err = json.Unmarshal(patchedjson, &patched)
	if err != nil {
		return false
	}

	dt, _, _ := unstructured.NestedString(patched, "metadata", "deletionTimestamp")
	return dt != ""
}

func NewResource(ctx context.Context, renv *readiness.Env, slice *apiv1.ResourceSlice, index int) (*Resource, error) {
	logger := logr.FromContextOrDiscard(ctx)
	resource := slice.Spec.Resources[index]
	res := &Resource{
		Manifest:     &resource,
		SliceDeleted: slice.DeletionTimestamp != nil,
		ManifestRef: ManifestRef{
			Slice: types.NamespacedName{
				Namespace: slice.Namespace,
				Name:      slice.Name,
			},
			Index: index,
		},
	}

	parsed, err := res.Parse()
	if err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}
	gvk := parsed.GroupVersionKind()
	res.GVK = gvk
	res.Ref.Name = parsed.GetName()
	res.Ref.Namespace = parsed.GetNamespace()
	res.Ref.Group = parsed.GroupVersionKind().Group
	res.Ref.Kind = parsed.GetKind()
	logger = logger.WithValues("resourceKind", parsed.GetKind(), "resourceName", parsed.GetName(), "resourceNamespace", parsed.GetNamespace())

	if res.Ref.Name == "" || res.Ref.Kind == "" || parsed.GetAPIVersion() == "" {
		return nil, fmt.Errorf("missing name, kind, or apiVersion")
	}

	if res.GVK == patchGVK {
		obj := struct {
			Patch patchMeta `json:"patch"`
		}{}
		err = json.Unmarshal([]byte(resource.Manifest), &obj)
		if err != nil {
			return nil, fmt.Errorf("parsing patch json: %w", err)
		}
		gv, err := schema.ParseGroupVersion(obj.Patch.APIVersion)
		if err != nil {
			return nil, fmt.Errorf("parsing patch apiVersion: %w", err)
		}
		res.GVK.Group = gv.Group
		res.GVK.Version = gv.Version
		res.GVK.Kind = obj.Patch.Kind
		res.Patch = obj.Patch.Ops
	}

	if res.GVK.Group == "apiextensions.k8s.io" && res.GVK.Kind == "CustomResourceDefinition" {
		res.DefinedGroupKind = &schema.GroupKind{}
		res.DefinedGroupKind.Group, _, _ = unstructured.NestedString(parsed.Object, "spec", "group")
		res.DefinedGroupKind.Kind, _, _ = unstructured.NestedString(parsed.Object, "spec", "names", "kind")
	}

	anno := parsed.GetAnnotations()
	if anno == nil {
		return res, nil
	}

	const reconcileIntervalKey = "eno.azure.io/reconcile-interval"
	reconcileInterval, err := time.ParseDuration(anno[reconcileIntervalKey])
	if anno[reconcileIntervalKey] != "" && err != nil {
		logger.V(0).Info("invalid reconcile interval - ignoring")
	}
	res.ReconcileInterval = &metav1.Duration{Duration: reconcileInterval}
	delete(anno, reconcileIntervalKey)

	const disableUpdatesKey = "eno.azure.io/disable-updates"
	res.DisableUpdates = anno[disableUpdatesKey] == "true"
	delete(anno, disableUpdatesKey)

	const readinessGroupKey = "eno.azure.io/readiness-group"
	rg, err := strconv.ParseInt(anno[readinessGroupKey], 10, 64)
	if anno[readinessGroupKey] != "" && err != nil {
		logger.V(0).Info("invalid readiness group - ignoring")
	}
	res.ReadinessGroup = int(rg)
	delete(anno, readinessGroupKey)

	for key, value := range anno {
		if !strings.HasPrefix(key, "eno.azure.io/readiness") {
			continue
		}
		delete(anno, key)

		name := strings.TrimPrefix(key, "eno.azure.io/readiness-")
		if name == "eno.azure.io/readiness" {
			name = "default"
		}

		check, err := readiness.ParseCheck(renv, value)
		if err != nil {
			logger.Error(err, "invalid cel expression")
			continue
		}
		check.Name = name
		res.ReadinessChecks = append(res.ReadinessChecks, check)
	}
	parsed.SetAnnotations(anno)
	sort.Slice(res.ReadinessChecks, func(i, j int) bool { return res.ReadinessChecks[i].Name < res.ReadinessChecks[j].Name })

	return res, nil
}

type patchMeta struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Ops        jsonpatch.Patch `json:"ops"`
}

type lastSeenMeta struct {
	lock            sync.Mutex
	resourceVersion string
}

func (l *lastSeenMeta) ObserveVersion(rv string) {
	l.lock.Lock()
	defer l.lock.Unlock()
	l.resourceVersion = rv
}

func (l *lastSeenMeta) HasBeenSeen() bool {
	l.lock.Lock()
	defer l.lock.Unlock()
	return l.resourceVersion != ""
}

func (l *lastSeenMeta) MatchesLastSeen(rv string) bool {
	l.lock.Lock()
	defer l.lock.Unlock()
	return l.resourceVersion == rv
}

type lastReconciledMeta struct {
	lock           sync.Mutex
	lastReconciled *time.Time
}

func (l *lastReconciledMeta) ObserveReconciliation() time.Duration {
	now := time.Now()

	l.lock.Lock()
	defer l.lock.Unlock()

	var latency time.Duration
	if l.lastReconciled != nil {
		latency = now.Sub(*l.lastReconciled)
	}

	l.lastReconciled = &now
	return time.Duration(latency.Abs().Milliseconds())
}

func NewInputRevisions(obj client.Object, refKey string) *apiv1.InputRevisions {
	ir := apiv1.InputRevisions{
		Key:             refKey,
		ResourceVersion: obj.GetResourceVersion(),
	}
	if rev, _ := strconv.Atoi(obj.GetAnnotations()["eno.azure.io/revision"]); rev != 0 {
		ir.Revision = &rev
	}
	if rev, _ := strconv.ParseInt(obj.GetAnnotations()["eno.azure.io/synthesizer-generation"], 10, 64); rev != 0 {
		ir.SynthesizerGeneration = &rev
	}
	return &ir
}
