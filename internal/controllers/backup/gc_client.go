package backup

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var errGCBudget = errors.New("inventory garbage collection sweep budget exhausted")

type gcBudgetKey struct{}

type gcBudget struct {
	requests  int
	deletions int
	mu        sync.Mutex
}

func (b *gcBudget) request(ctx context.Context, method string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.requests >= gcMaxRequests || (method == http.MethodDelete && b.deletions >= gcMaxDeletions) {
		return errGCBudget
	}
	b.requests++
	if method == http.MethodDelete {
		b.deletions++
	}
	return nil
}

func (b *gcBudget) deletionCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.deletions
}

func (b *gcBudget) requestCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.requests
}

type gcTransport struct {
	base http.RoundTripper
}

func (t *gcTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	budget, ok := req.Context().Value(gcBudgetKey{}).(*gcBudget)
	if !ok {
		return nil, errors.New("inventory garbage collection request has no sweep budget")
	}
	if err := budget.request(req.Context(), req.Method); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

func newGCClient(config *rest.Config, scheme *runtime.Scheme) (client.Client, error) {
	config = rest.CopyConfig(config)
	if config.Timeout == 0 || config.Timeout > 10*time.Second {
		config.Timeout = 10 * time.Second
	}
	wrap := config.WrapTransport
	config.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
		// Count actual HTTP requests, including retries made by client-go or
		// existing transport wrappers, without limiting normal recovery clients.
		base = &gcTransport{base: base}
		if wrap != nil {
			base = wrap(base)
		}
		return base
	}
	// GC uses only these known types. Avoid unbudgeted discovery from a dynamic
	// REST mapper, whose RESTMapping API does not accept the sweep context.
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{corev1.SchemeGroupVersion, apiv1.SchemeGroupVersion})
	mapper.AddSpecific(corev1.SchemeGroupVersion.WithKind("ConfigMap"), corev1.SchemeGroupVersion.WithResource("configmaps"),
		corev1.SchemeGroupVersion.WithResource("configmap"), meta.RESTScopeNamespace)
	mapper.AddSpecific(apiv1.SchemeGroupVersion.WithKind("Composition"), apiv1.SchemeGroupVersion.WithResource("compositions"),
		apiv1.SchemeGroupVersion.WithResource("composition"), meta.RESTScopeNamespace)
	mapper.AddSpecific(apiv1.SchemeGroupVersion.WithKind("Symphony"), apiv1.SchemeGroupVersion.WithResource("symphonies"),
		apiv1.SchemeGroupVersion.WithResource("symphony"), meta.RESTScopeNamespace)
	return client.New(config, client.Options{Scheme: scheme, Mapper: mapper})
}

type gcSymphonyRead struct {
	object apiv1.Symphony
	err    error
}

type gcCompositionPage struct {
	object apiv1.CompositionList
	err    error
}

type gcPageKey struct {
	namespace, continuation string
	limit                   int64
}

// Cached observations only reject candidates early. Every delete rechecks
// eligibility using the uncached reader, never this per-sweep view.
type gcCandidateReader struct {
	client.Reader
	symphonies   map[types.NamespacedName]gcSymphonyRead
	compositions map[gcPageKey]gcCompositionPage
}

func (r *gcCandidateReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	symph, ok := obj.(*apiv1.Symphony)
	if !ok {
		return r.Reader.Get(ctx, key, obj, opts...)
	}
	read, exists := r.symphonies[key]
	if !exists {
		read.err = r.Reader.Get(ctx, key, &read.object, opts...)
		r.symphonies[key] = read
	}
	read.object.DeepCopyInto(symph)
	return read.err
}

func (r *gcCandidateReader) List(ctx context.Context, obj client.ObjectList, opts ...client.ListOption) error {
	comps, ok := obj.(*apiv1.CompositionList)
	options := (&client.ListOptions{}).ApplyOptions(opts)
	if !ok || options.LabelSelector != nil || options.FieldSelector != nil || options.Raw != nil {
		return r.Reader.List(ctx, obj, opts...)
	}
	key := gcPageKey{namespace: options.Namespace, continuation: options.Continue, limit: options.Limit}
	read, exists := r.compositions[key]
	if !exists {
		read.err = r.Reader.List(ctx, &read.object, opts...)
		r.compositions[key] = read
	}
	read.object.DeepCopyInto(comps)
	return read.err
}
