package reconstitution

import (
	"context"
	"sync"
	"time"

	celtypes "github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/google/cel-go/cel"
)

// Reconciler is implemented by types that can reconcile individual, reconstituted resources.
type Reconciler interface {
	Name() string
	Reconcile(ctx context.Context, req *Request) (ctrl.Result, error)
}

// Client provides read/write access to a collection of reconstituted resources.
type Client interface {
	Get(ctx context.Context, comp *CompositionRef, res *ResourceRef) (*Resource, bool)
	PatchStatusAsync(ctx context.Context, req *ManifestRef, patchFn StatusPatchFn)
}

type StatusPatchFn func(*apiv1.ResourceState) *apiv1.ResourceState

// ManifestRef references a particular resource manifest within a resource slice.
type ManifestRef struct {
	Slice types.NamespacedName
	Index int // position of this manifest within the slice
}

// Resource is the controller's internal representation of a single resource out of a ResourceSlice.
type Resource struct {
	lastSeenMeta
	lastReconciledMeta

	Ref             ResourceRef
	Manifest        *apiv1.Manifest
	GVK             schema.GroupVersionKind
	SliceDeleted    bool
	ReadinessChecks []*ReadinessCheck
}

func (r *Resource) Deleted() bool { return r.SliceDeleted || r.Manifest.Deleted }

func (r *Resource) Parse() (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}
	return u, u.UnmarshalJSON([]byte(r.Manifest.Manifest))
}

type ReadinessCheck struct {
	Name    string
	program cel.Program
	env     *cel.Env
}

func MustReadinessCheckTest(expr string) *ReadinessCheck {
	env, err := newCelEnv()
	if err != nil {
		panic(err)
	}
	check, err := newReadinessCheck(env, expr)
	if err != nil {
		panic(err)
	}
	return check
}

func newReadinessCheck(env *cel.Env, expr string) (*ReadinessCheck, error) {
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	prgm, err := env.Program(ast) // TODO: Set InterruptCheckFrequency
	if err != nil {
		return nil, err
	}
	return &ReadinessCheck{program: prgm, env: env}, nil
}

func (r *ReadinessCheck) Eval(ctx context.Context, resource *unstructured.Unstructured) (*Readiness, bool) {
	if resource == nil {
		return nil, false
	}
	val, details, err := r.program.ContextEval(ctx, map[string]any{"self": resource.Object})
	if details != nil {
		cost := details.ActualCost()
		if cost != nil {
			celEvalCost.Add(float64(*cost))
		}
	}
	if err != nil {
		return nil, false
	}

	// Support matching on condition structs.
	// This allows us to grab the transition time instead of just using the current time.
	if list, ok := val.Value().([]ref.Val); ok {
		for _, ref := range list {
			if mp, ok := ref.Value().(map[string]any); ok {
				if mp != nil && mp["status"] == "True" && mp["type"] != "" && mp["reason"] != "" {
					ts := metav1.Now()
					if str, ok := mp["lastTransitionTime"].(string); ok {
						parsed, err := time.Parse(time.RFC3339, str)
						if err == nil {
							ts.Time = parsed
						}
					}
					return &Readiness{ReadyTime: ts, PreciseTime: true}, true
				}
			}
		}
	}

	if val == celtypes.True {
		return &Readiness{ReadyTime: metav1.Now()}, true
	}
	return nil, false
}

type Readiness struct {
	ReadyTime   metav1.Time
	PreciseTime bool // true when time came from a condition, not the controller's metav1.Now
}

// ResourceRef refers to a specific synthesized resource.
type ResourceRef struct {
	Name, Namespace, Kind string
}

// CompositionRef refers to a specific generation of a composition.
type CompositionRef struct {
	Name, Namespace string
	Generation      int64
}

func NewCompositionRef(comp *apiv1.Composition) *CompositionRef {
	c := &CompositionRef{Name: comp.Name, Namespace: comp.Namespace}
	if comp.Status.CurrentSynthesis != nil {
		c.Generation = comp.Status.CurrentSynthesis.ObservedCompositionGeneration
	}
	return c
}

// Request is like controller-runtime reconcile.Request but for reconstituted resources.
// https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/reconcile#Request
type Request struct {
	Resource    ResourceRef
	Manifest    ManifestRef
	Composition types.NamespacedName
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
