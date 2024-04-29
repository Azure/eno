package resource

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
)

var patchGVK = schema.GroupVersionKind{
	Group:   "eno.azure.io",
	Version: "v1",
	Kind:    "Patch",
}

// Ref refers to a specific synthesized resource.
type Ref struct {
	Name, Namespace, Kind string
}

// Resource is the controller's internal representation of a single resource out of a ResourceSlice.
type Resource struct {
	lastSeenMeta
	lastReconciledMeta

	Ref               Ref
	Manifest          *apiv1.Manifest
	ReconcileInterval *metav1.Duration
	GVK               schema.GroupVersionKind
	SliceDeleted      bool
	ReadinessChecks   readiness.Checks
	Patch             jsonpatch.Patch
}

func (r *Resource) Deleted() bool {
	return r.SliceDeleted || r.Manifest.Deleted || (r.Patch != nil && r.patchSetsDeletionTimestamp())
}

func (r *Resource) Parse() (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}
	return u, u.UnmarshalJSON([]byte(r.Manifest.Manifest))
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

func NewResource(ctx context.Context, renv *readiness.Env, slice *apiv1.ResourceSlice, resource *apiv1.Manifest) (*Resource, error) {
	logger := logr.FromContextOrDiscard(ctx)
	res := &Resource{
		Manifest:     resource,
		SliceDeleted: slice.DeletionTimestamp != nil,
	}

	parsed, err := res.Parse()
	if err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}
	gvk := parsed.GroupVersionKind()
	res.GVK = gvk
	res.Ref.Name = parsed.GetName()
	res.Ref.Namespace = parsed.GetNamespace()
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

	anno := parsed.GetAnnotations()
	if anno == nil {
		return res, nil
	}

	const reconcileIntervalKey = "eno.azure.io/reconcile-interval"
	reconcileInterval, _ := time.ParseDuration(anno[reconcileIntervalKey])
	res.ReconcileInterval = &metav1.Duration{Duration: reconcileInterval}
	delete(anno, reconcileIntervalKey)

	for key, value := range parsed.GetAnnotations() {
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
	return latency
}
