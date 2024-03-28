package resource

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/readiness"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Ref refers to a specific synthesized resource.
type Ref struct {
	Name, Namespace, Kind string
}

// Resource is the controller's internal representation of a single resource out of a ResourceSlice.
type Resource struct {
	lastSeenMeta
	lastReconciledMeta

	Ref             Ref
	Manifest        *apiv1.Manifest
	GVK             schema.GroupVersionKind
	SliceDeleted    bool
	ReadinessChecks readiness.Checks
}

func (r *Resource) Deleted() bool { return r.SliceDeleted || r.Manifest.Deleted }

func (r *Resource) Parse() (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}
	return u, u.UnmarshalJSON([]byte(r.Manifest.Manifest))
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

	anno := parsed.GetAnnotations()
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
