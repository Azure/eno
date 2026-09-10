package reconciliation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type recoveryReconstitutionHarness struct {
	t              *testing.T
	ctx            context.Context
	comp           *apiv1.Composition
	source         *reconstitutionSource
	queue          workqueue.TypedRateLimitingInterface[resource.Request]
	readCalls      int
	reads          []string
	informerErrors map[string]error
	apiErrors      map[string]error
	informerStatus map[string]apiv1.ResourceSliceStatus
}

func recoveryReconstitutionNewHarness(t *testing.T, comp *apiv1.Composition, informerSlices, apiSlices []*apiv1.ResourceSlice) *recoveryReconstitutionHarness {
	t.Helper()
	h := &recoveryReconstitutionHarness{
		t:              t,
		ctx:            context.Background(),
		comp:           comp,
		queue:          workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[resource.Request]()),
		informerErrors: map[string]error{},
		apiErrors:      map[string]error{},
		informerStatus: map[string]apiv1.ResourceSliceStatus{},
	}
	t.Cleanup(h.queue.ShutDown)
	before := comp.DeepCopy()
	t.Cleanup(func() { assert.Equal(t, before, comp, "reconstitution must not mutate the published composition") })

	newClient := func(view string, slices []*apiv1.ResourceSlice) client.Client {
		objects := []client.Object{comp.DeepCopy()}
		for _, slice := range slices {
			copy := slice.DeepCopy()
			if view == "informer" {
				for i := range copy.Spec.Resources {
					copy.Spec.Resources[i].Manifest = ""
				}
			}
			objects = append(objects, copy)
		}
		checkingStore := true
		unexpectedWrite := func() error {
			t.Errorf("unexpected write to %s client", view)
			return errors.New("reconstitution must be read-only")
		}
		cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
			Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if checkingStore {
					return cli.Get(ctx, key, obj, opts...)
				}
				h.readCalls++
				if _, ok := obj.(*apiv1.ResourceSlice); ok {
					require.NotEmpty(t, key.Name, "malformed references must not be read")
					require.Equal(t, comp.Namespace, key.Namespace)
					h.reads = append(h.reads, view+"/"+key.Name)
					readErrors := h.apiErrors
					if view == "informer" {
						readErrors = h.informerErrors
					}
					if err := readErrors[key.Name]; err != nil {
						return err
					}
				}
				err := cli.Get(ctx, key, obj, opts...)
				if slice, ok := obj.(*apiv1.ResourceSlice); err == nil && ok && view == "informer" {
					if status, ok := h.informerStatus[key.Name]; ok {
						slice.Status = *status.DeepCopy()
					}
				}
				return err
			},
			Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
				return unexpectedWrite()
			},
			Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
				return unexpectedWrite()
			},
			Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
				return unexpectedWrite()
			},
			Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
				return unexpectedWrite()
			},
			DeleteAllOf: func(context.Context, client.WithWatch, client.Object, ...client.DeleteAllOfOption) error {
				return unexpectedWrite()
			},
			SubResourceCreate: func(context.Context, client.Client, string, client.Object, client.Object, ...client.SubResourceCreateOption) error {
				return unexpectedWrite()
			},
			SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
				return unexpectedWrite()
			},
			SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
				return unexpectedWrite()
			},
		}, objects...)

		// Snapshot the stored objects, including fake-client defaults, independently of injected read views.
		for _, obj := range objects {
			require.NoError(t, cli.Get(h.ctx, client.ObjectKeyFromObject(obj), obj))
		}
		checkingStore = false
		t.Cleanup(func() {
			checkingStore = true
			for _, before := range objects {
				after := before.DeepCopyObject().(client.Object)
				require.NoError(t, cli.Get(h.ctx, client.ObjectKeyFromObject(before), after))
				assert.Equal(t, before, after, "%s store must not change", view)
			}
		})
		return cli
	}
	cache := &resource.Cache{}
	cache.SetQueue(h.queue)
	h.source = &reconstitutionSource{
		client:          newClient("informer", informerSlices),
		nonCachedReader: newClient("api", apiSlices),
		cache:           cache,
	}
	return h
}

func recoveryReconstitutionSynthesis(uuid string, names ...string) *apiv1.Synthesis {
	syn := &apiv1.Synthesis{UUID: uuid, Synthesized: recoveryReconstitutionTime()}
	for _, name := range names {
		syn.ResourceSlices = append(syn.ResourceSlices, &apiv1.ResourceSliceRef{Name: name})
	}
	return syn
}

func recoveryReconstitutionTime() *metav1.Time {
	value := metav1.NewTime(time.Unix(1700000000, 0).UTC())
	return &value
}

func recoveryReconstitutionComposition(previous, current *apiv1.Synthesis) *apiv1.Composition {
	return &apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{Name: "reconstitution", Namespace: "default", Generation: 3},
		Spec:       apiv1.CompositionSpec{Synthesizer: apiv1.SynthesizerRef{Name: "synthesizer"}},
		Status: apiv1.CompositionStatus{
			PreviousSynthesis: previous,
			CurrentSynthesis:  current,
		},
	}
}

func recoveryReconstitutionSlice(name, uuid string, resources ...string) *apiv1.ResourceSlice {
	slice := &apiv1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Generation: 1},
		Spec:       apiv1.ResourceSliceSpec{SynthesisUUID: uuid},
		Status:     apiv1.ResourceSliceStatus{Resources: make([]apiv1.ResourceState, len(resources))},
	}
	for i, name := range resources {
		slice.Spec.Resources = append(slice.Spec.Resources, apiv1.Manifest{Manifest: fmt.Sprintf(
			`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":%q,"namespace":"default","annotations":{"eno.azure.io/readiness-group":%q}},"data":{"source":"full-api","resource":%q}}`,
			name, fmt.Sprint(i), name,
		)})
	}
	return slice
}

func recoveryReconstitutionNotFound(name string) error {
	return apierrors.NewNotFound(schema.GroupResource{Group: "eno.azure.io", Resource: "resourceslices"}, name)
}

func (h *recoveryReconstitutionHarness) recoveryReconstitutionReconcile() (ctrl.Result, error) {
	h.t.Helper()
	return h.source.Reconcile(h.ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(h.comp)})
}

func (h *recoveryReconstitutionHarness) recoveryReconstitutionResource(uuid, name, slice string, index int, visible bool) *resource.Resource {
	h.t.Helper()
	res, actualVisible, found := h.source.cache.Get(h.ctx, uuid, resource.Ref{Kind: "ConfigMap", Namespace: "default", Name: name})
	require.True(h.t, found, "resource %s must be retained in %s", name, uuid)
	require.NotNil(h.t, res)
	assert.Equal(h.t, visible, actualVisible, "resource %s visibility", name)
	assert.Equal(h.t, resource.ManifestRef{Slice: client.ObjectKey{Namespace: h.comp.Namespace, Name: slice}, Index: index}, res.ManifestRef)
	return res
}

func (h *recoveryReconstitutionHarness) recoveryReconstitutionQueue(names ...string) {
	h.t.Helper()
	var actual, expected []resource.Request
	for h.queue.Len() > 0 {
		item, shutdown := h.queue.Get()
		require.False(h.t, shutdown)
		actual = append(actual, item)
		h.queue.Done(item)
		h.queue.Forget(item)
	}
	for _, name := range names {
		expected = append(expected, resource.Request{
			Composition: client.ObjectKeyFromObject(h.comp),
			Resource:    resource.Ref{Kind: "ConfigMap", Namespace: "default", Name: name},
		})
	}
	assert.ElementsMatch(h.t, expected, actual)
}

func (h *recoveryReconstitutionHarness) recoveryReconstitutionAPIReads() []string {
	var reads []string
	for _, read := range h.reads {
		if strings.HasPrefix(read, "api/") {
			reads = append(reads, read)
		}
	}
	return reads
}

func TestRecoveryReconstitutionR1IncompleteSynthesis(t *testing.T) {
	for _, previous := range []bool{false, true} {
		for _, test := range []struct {
			name      string
			synthesis *apiv1.Synthesis
		}{
			{name: "nil"},
			{name: "not synthesized", synthesis: &apiv1.Synthesis{
				UUID: "incomplete", ResourceSlices: []*apiv1.ResourceSliceRef{nil, {}, {Name: "unread"}},
			}},
		} {
			t.Run(fmt.Sprintf("previous=%t/%s", previous, test.name), func(t *testing.T) {
				comp := recoveryReconstitutionComposition(nil, nil)
				if previous {
					comp.Status.PreviousSynthesis = test.synthesis
				} else {
					comp.Status.CurrentSynthesis = test.synthesis
				}
				h := recoveryReconstitutionNewHarness(t, comp, nil, nil)
				filled, err := h.source.populateCache(h.ctx, comp, test.synthesis, previous)
				require.NoError(t, err)
				assert.False(t, filled)
				assert.Zero(t, h.readCalls)
				assert.Empty(t, h.reads)
				assert.False(t, h.source.cache.Visit(h.ctx, comp, "", nil))
				assert.False(t, h.source.cache.Visit(h.ctx, comp, "incomplete", nil))
				h.recoveryReconstitutionQueue()
			})
		}
	}
}

func TestRecoveryReconstitutionR2MalformedPreviousReferences(t *testing.T) {
	for _, test := range []struct {
		name string
		refs []*apiv1.ResourceSliceRef
	}{
		{name: "nil before valid", refs: []*apiv1.ResourceSliceRef{nil, {Name: "retained"}}},
		{name: "empty before valid", refs: []*apiv1.ResourceSliceRef{{}, {Name: "retained"}}},
		{name: "malformed around valid", refs: []*apiv1.ResourceSliceRef{nil, {}, {Name: "retained"}, nil, {}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			syn := recoveryReconstitutionSynthesis("previous")
			syn.ResourceSlices = test.refs
			comp := recoveryReconstitutionComposition(syn, nil)
			slice := recoveryReconstitutionSlice("retained", syn.UUID, "retained-resource")
			h := recoveryReconstitutionNewHarness(t, comp, []*apiv1.ResourceSlice{slice}, []*apiv1.ResourceSlice{slice})

			filled, err := h.source.populateCache(h.ctx, comp, syn, true)
			require.NoError(t, err)
			require.True(t, filled)
			assert.Equal(t, []string{"informer/retained", "api/retained"}, h.reads)
			res := h.recoveryReconstitutionResource(syn.UUID, "retained-resource", slice.Name, 0, true)
			assert.Nil(t, res.State())
			h.recoveryReconstitutionQueue()

			filled, err = h.source.populateCache(h.ctx, comp, syn, true)
			require.NoError(t, err)
			assert.False(t, filled)
			assert.Equal(t, []string{"api/retained"}, h.recoveryReconstitutionAPIReads())
			h.recoveryReconstitutionQueue("retained-resource")
		})
	}
}

func TestRecoveryReconstitutionR3MissingPreviousAllowsCurrent(t *testing.T) {
	for _, names := range [][]string{{"missing", "retained"}, {"retained", "missing"}} {
		t.Run(names[0]+" first", func(t *testing.T) {
			previous := recoveryReconstitutionSynthesis("previous", names...)
			current := recoveryReconstitutionSynthesis("current", "current-slice")
			comp := recoveryReconstitutionComposition(previous, current)
			slices := []*apiv1.ResourceSlice{
				recoveryReconstitutionSlice("retained", previous.UUID, "previous-resource"),
				recoveryReconstitutionSlice("current-slice", current.UUID, "current-resource"),
			}
			h := recoveryReconstitutionNewHarness(t, comp, slices, slices)

			result, err := h.recoveryReconstitutionReconcile()
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{Requeue: true}, result)
			h.recoveryReconstitutionResource(previous.UUID, "previous-resource", "retained", 0, true)
			assert.False(t, h.source.cache.Visit(h.ctx, comp, current.UUID, nil))
			assert.Equal(t, []string{"api/missing", "api/retained"}, h.recoveryReconstitutionAPIReads())
			assert.NotContains(t, h.reads, "informer/current-slice")
			h.recoveryReconstitutionQueue()

			result, err = h.recoveryReconstitutionReconcile()
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{Requeue: true}, result)
			h.recoveryReconstitutionResource(current.UUID, "current-resource", "current-slice", 0, true)
			h.recoveryReconstitutionQueue("previous-resource")

			result, err = h.recoveryReconstitutionReconcile()
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{}, result)
			h.recoveryReconstitutionQueue("current-resource")
		})
	}
}

func TestRecoveryReconstitutionR4FallbackDoesNotAdvanceInformerStatus(t *testing.T) {
	for _, test := range []struct {
		name  string
		state apiv1.ResourceState
	}{
		{name: "ready", state: apiv1.ResourceState{Reconciled: true, Ready: recoveryReconstitutionTime()}},
		{name: "deleted and ready", state: apiv1.ResourceState{Reconciled: true, Deleted: true, Ready: recoveryReconstitutionTime()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			syn := recoveryReconstitutionSynthesis("previous", "previous-slice")
			comp := recoveryReconstitutionComposition(syn, nil)
			informerSlice := recoveryReconstitutionSlice("previous-slice", syn.UUID, "dependency", "dependent")
			apiSlice := informerSlice.DeepCopy()
			apiSlice.Status.Resources[0] = test.state
			h := recoveryReconstitutionNewHarness(t, comp, []*apiv1.ResourceSlice{informerSlice}, []*apiv1.ResourceSlice{apiSlice})
			h.informerErrors[informerSlice.Name] = recoveryReconstitutionNotFound(informerSlice.Name)

			filled, err := h.source.populateCache(h.ctx, comp, syn, true)
			require.NoError(t, err)
			require.True(t, filled)
			assert.Equal(t, []string{"informer/previous-slice", "api/previous-slice", "api/previous-slice"}, h.reads)
			dependency := h.recoveryReconstitutionResource(syn.UUID, "dependency", apiSlice.Name, 0, true)
			dependent := h.recoveryReconstitutionResource(syn.UUID, "dependent", apiSlice.Name, 1, false)
			assert.Nil(t, dependency.State(), "Fill must not consume API status")
			assert.Nil(t, dependent.State())
			h.recoveryReconstitutionQueue()

			h.reads = nil
			filled, err = h.source.populateCache(h.ctx, comp, syn, true)
			require.NoError(t, err)
			assert.False(t, filled)
			assert.Equal(t, []string{"informer/previous-slice", "api/previous-slice"}, h.reads, "only fallback, not another full fill")
			assert.Equal(t, &apiv1.ResourceState{}, dependency.State(), "fallback status must be cleared before Visit")
			assert.Equal(t, &apiv1.ResourceState{}, dependent.State())
			assert.Same(t, dependent, h.recoveryReconstitutionResource(syn.UUID, "dependent", apiSlice.Name, 1, false))
			h.recoveryReconstitutionQueue("dependency", "dependent")

			delete(h.informerErrors, informerSlice.Name)
			h.informerStatus[informerSlice.Name] = apiSlice.Status
			h.reads = nil
			filled, err = h.source.populateCache(h.ctx, comp, syn, true)
			require.NoError(t, err)
			assert.False(t, filled)
			assert.Empty(t, h.recoveryReconstitutionAPIReads())
			assert.Equal(t, &test.state, dependency.State())
			assert.Same(t, dependent, h.recoveryReconstitutionResource(syn.UUID, "dependent", apiSlice.Name, 1, true))
			h.recoveryReconstitutionQueue("dependency", "dependent")
		})
	}
}

func TestRecoveryReconstitutionR5PreviousDisappearsDuringFullRead(t *testing.T) {
	for _, names := range [][]string{
		{"vanished", "first", "last"},
		{"first", "vanished", "last"},
		{"first", "last", "vanished"},
	} {
		t.Run(fmt.Sprint(names), func(t *testing.T) {
			syn := recoveryReconstitutionSynthesis("previous", names...)
			comp := recoveryReconstitutionComposition(syn, nil)
			first := recoveryReconstitutionSlice("first", syn.UUID, "first-resource")
			last := recoveryReconstitutionSlice("last", syn.UUID, "last-resource")
			vanished := recoveryReconstitutionSlice("vanished", syn.UUID, "vanished-resource")
			h := recoveryReconstitutionNewHarness(t, comp,
				[]*apiv1.ResourceSlice{first, last, vanished}, []*apiv1.ResourceSlice{first, last})

			filled, err := h.source.populateCache(h.ctx, comp, syn, true)
			require.NoError(t, err)
			require.True(t, filled)
			h.recoveryReconstitutionResource(syn.UUID, "first-resource", first.Name, 0, true)
			h.recoveryReconstitutionResource(syn.UUID, "last-resource", last.Name, 0, true)
			_, _, found := h.source.cache.Get(h.ctx, syn.UUID, resource.Ref{Kind: "ConfigMap", Namespace: "default", Name: "vanished-resource"})
			assert.False(t, found)
			assert.ElementsMatch(t, []string{"api/first", "api/last", "api/vanished"}, h.recoveryReconstitutionAPIReads())
			h.recoveryReconstitutionQueue()

			filled, err = h.source.populateCache(h.ctx, comp, syn, true)
			require.NoError(t, err)
			assert.False(t, filled)
			h.recoveryReconstitutionQueue("first-resource", "last-resource")
		})
	}
}

func TestRecoveryReconstitutionR6MalformedCurrentReferencesAreAtomic(t *testing.T) {
	for _, test := range []struct {
		name string
		ref  *apiv1.ResourceSliceRef
	}{
		{name: "nil"},
		{name: "empty", ref: &apiv1.ResourceSliceRef{}},
	} {
		for _, index := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/index=%d", test.name, index), func(t *testing.T) {
				syn := recoveryReconstitutionSynthesis("current", "before")
				if index == 0 {
					syn.ResourceSlices = nil
				}
				syn.ResourceSlices = append(syn.ResourceSlices, test.ref, &apiv1.ResourceSliceRef{Name: "after"})
				comp := recoveryReconstitutionComposition(nil, syn)
				slices := []*apiv1.ResourceSlice{
					recoveryReconstitutionSlice("before", syn.UUID, "before-resource"),
					recoveryReconstitutionSlice("after", syn.UUID, "after-resource"),
				}
				h := recoveryReconstitutionNewHarness(t, comp, slices, slices)

				filled, err := h.source.populateCache(h.ctx, comp, syn, false)
				require.EqualError(t, err, fmt.Sprintf("current synthesis resource slices reference %d has no name", index))
				assert.False(t, filled)
				assert.False(t, h.source.cache.Visit(h.ctx, comp, syn.UUID, nil), "no partial synthesis may be filled")
				assert.Empty(t, h.recoveryReconstitutionAPIReads())
				assert.NotContains(t, h.reads, "informer/after")
				h.recoveryReconstitutionQueue()
			})
		}
	}
}

func TestRecoveryReconstitutionR7CurrentNotFoundNeverPartiallyFills(t *testing.T) {
	for _, view := range []string{"informer", "api"} {
		for _, names := range [][]string{{"missing", "before", "after"}, {"before", "missing", "after"}} {
			t.Run(view+"/"+names[0]+" first", func(t *testing.T) {
				syn := recoveryReconstitutionSynthesis("current", names...)
				comp := recoveryReconstitutionComposition(nil, syn)
				slices := []*apiv1.ResourceSlice{
					recoveryReconstitutionSlice("before", syn.UUID, "before-resource"),
					recoveryReconstitutionSlice("missing", syn.UUID, "missing-resource"),
					recoveryReconstitutionSlice("after", syn.UUID, "after-resource"),
				}
				h := recoveryReconstitutionNewHarness(t, comp, slices, slices)
				readErrors := h.informerErrors
				if view == "api" {
					readErrors = h.apiErrors
				}
				readErrors["missing"] = recoveryReconstitutionNotFound("missing")

				for attempt := 0; attempt < 2; attempt++ {
					result, err := h.recoveryReconstitutionReconcile()
					require.NoError(t, err, "preserve the existing IgnoreNotFound return contract")
					assert.Equal(t, ctrl.Result{}, result)
					assert.False(t, h.source.cache.Visit(h.ctx, comp, syn.UUID, nil), "current synthesis must not be truncated")
					if view == "informer" {
						assert.Empty(t, h.recoveryReconstitutionAPIReads(), "current slices must not use the previous-only fallback")
					} else {
						assert.NotContains(t, h.reads, "api/after")
					}
					h.recoveryReconstitutionQueue()
				}

				delete(readErrors, "missing")
				filled, err := h.source.populateCache(h.ctx, comp, syn, false)
				require.NoError(t, err)
				require.True(t, filled)
				for _, name := range []string{"before", "missing", "after"} {
					h.recoveryReconstitutionResource(syn.UUID, name+"-resource", name, 0, true)
				}
				h.recoveryReconstitutionQueue()
			})
		}
	}
}

func TestRecoveryReconstitutionR8ReadErrorsPropagate(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "forbidden", err: apierrors.NewForbidden(
			schema.GroupResource{Group: "eno.azure.io", Resource: "resourceslices"}, "failed", errors.New("denied"))},
		{name: "timeout", err: apierrors.NewTimeoutError("slice read timed out", 1)},
		{name: "generic", err: errors.New("slice read failed")},
	} {
		for _, path := range []struct {
			name     string
			previous bool
			view     string
			fallback bool
		}{
			{name: "previous informer", previous: true, view: "informer"},
			{name: "previous full API", previous: true, view: "api"},
			{name: "previous fallback", previous: true, view: "api", fallback: true},
			{name: "current informer", view: "informer"},
			{name: "current full API", view: "api"},
		} {
			t.Run(test.name+"/"+path.name, func(t *testing.T) {
				syn := recoveryReconstitutionSynthesis("failed-synthesis", "before", "failed", "after")
				comp := recoveryReconstitutionComposition(nil, syn)
				if path.previous {
					comp.Status.PreviousSynthesis = syn
					comp.Status.CurrentSynthesis = recoveryReconstitutionSynthesis("current", "current-slice")
				}
				slices := []*apiv1.ResourceSlice{
					recoveryReconstitutionSlice("before", syn.UUID, "before-resource"),
					recoveryReconstitutionSlice("failed", syn.UUID, "failed-resource"),
					recoveryReconstitutionSlice("after", syn.UUID, "after-resource"),
					recoveryReconstitutionSlice("current-slice", "current", "current-resource"),
				}
				h := recoveryReconstitutionNewHarness(t, comp, slices, slices)
				if path.view == "informer" {
					h.informerErrors["failed"] = test.err
				} else {
					h.apiErrors["failed"] = test.err
				}
				if path.fallback {
					h.informerErrors["failed"] = recoveryReconstitutionNotFound("failed")
				}

				result, err := h.recoveryReconstitutionReconcile()
				require.ErrorIs(t, err, test.err)
				assert.ErrorContains(t, err, "failed")
				assert.Equal(t, apierrors.IsForbidden(test.err), apierrors.IsForbidden(err))
				assert.Equal(t, apierrors.IsTimeout(test.err), apierrors.IsTimeout(err))
				assert.Equal(t, ctrl.Result{}, result)
				assert.False(t, h.source.cache.Visit(h.ctx, comp, syn.UUID, nil))
				assert.NotContains(t, h.reads, "api/after")
				if path.previous {
					assert.NotContains(t, h.reads, "informer/current-slice", "fatal history errors must stop Reconcile before current reads")
					assert.NotContains(t, h.reads, "api/current-slice")
					assert.False(t, h.source.cache.Visit(h.ctx, comp, "current", nil))
				}
				if path.view == "informer" {
					assert.Empty(t, h.recoveryReconstitutionAPIReads(), "only NotFound permits fallback")
				}
				h.recoveryReconstitutionQueue()
			})
		}
	}
}

func TestRecoveryReconstitutionR9UnavailablePreviousFillsEmptyOnce(t *testing.T) {
	for _, test := range []struct {
		name string
		refs []*apiv1.ResourceSliceRef
	}{
		{name: "empty list"},
		{name: "malformed only", refs: []*apiv1.ResourceSliceRef{nil, {}}},
		{name: "missing only", refs: []*apiv1.ResourceSliceRef{{Name: "missing"}}},
		{name: "mixed unavailable", refs: []*apiv1.ResourceSliceRef{nil, {Name: "missing"}, {}, {Name: "also-missing"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			previous := recoveryReconstitutionSynthesis("previous")
			previous.ResourceSlices = test.refs
			current := recoveryReconstitutionSynthesis("current", "current-slice")
			comp := recoveryReconstitutionComposition(previous, current)
			slice := recoveryReconstitutionSlice("current-slice", current.UUID, "current-resource")
			h := recoveryReconstitutionNewHarness(t, comp, []*apiv1.ResourceSlice{slice}, []*apiv1.ResourceSlice{slice})

			result, err := h.recoveryReconstitutionReconcile()
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{Requeue: true}, result)
			assert.True(t, h.source.cache.Visit(h.ctx, comp, previous.UUID, nil), "empty previous synthesis must be cached")
			assert.False(t, h.source.cache.Visit(h.ctx, comp, current.UUID, nil))
			assert.NotContains(t, h.reads, "informer/current-slice")
			h.recoveryReconstitutionQueue()

			result, err = h.recoveryReconstitutionReconcile()
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{Requeue: true}, result)
			res := h.recoveryReconstitutionResource(current.UUID, "current-resource", slice.Name, 0, true)
			assert.Nil(t, res.State())
			h.recoveryReconstitutionQueue()

			for pass := 0; pass < 2; pass++ {
				result, err = h.recoveryReconstitutionReconcile()
				require.NoError(t, err)
				assert.Equal(t, ctrl.Result{}, result, "empty previous synthesis must not cause endless refill requeues")
				assert.True(t, h.source.cache.Visit(h.ctx, comp, previous.UUID, nil))
				assert.Same(t, res, h.recoveryReconstitutionResource(current.UUID, "current-resource", slice.Name, 0, true))
				if pass == 0 {
					h.recoveryReconstitutionQueue("current-resource")
				} else {
					h.recoveryReconstitutionQueue()
				}
			}
			var currentFullReads int
			for _, read := range h.recoveryReconstitutionAPIReads() {
				if read == "api/current-slice" {
					currentFullReads++
				}
			}
			assert.Equal(t, 1, currentFullReads)
		})
	}
}

func TestRecoveryReconstitutionR10WarmCacheVisitsInformerState(t *testing.T) {
	for _, previous := range []bool{false, true} {
		t.Run(fmt.Sprintf("previous=%t", previous), func(t *testing.T) {
			syn := recoveryReconstitutionSynthesis("warm", "warm-slice")
			comp := recoveryReconstitutionComposition(nil, syn)
			if previous {
				comp = recoveryReconstitutionComposition(syn, nil)
			}
			informerSlice := recoveryReconstitutionSlice("warm-slice", syn.UUID, "dependency", "dependent")
			apiSlice := informerSlice.DeepCopy()
			apiSlice.Status.Resources[0] = apiv1.ResourceState{Reconciled: true, Ready: recoveryReconstitutionTime()}
			h := recoveryReconstitutionNewHarness(t, comp, []*apiv1.ResourceSlice{informerSlice}, []*apiv1.ResourceSlice{apiSlice})
			h.source.cache.Fill(h.ctx, comp, syn.UUID, []apiv1.ResourceSlice{*apiSlice})
			h.apiErrors[apiSlice.Name] = errors.New("warm cache must not read full slices")
			dependency := h.recoveryReconstitutionResource(syn.UUID, "dependency", apiSlice.Name, 0, true)
			dependent := h.recoveryReconstitutionResource(syn.UUID, "dependent", apiSlice.Name, 1, false)
			assert.Nil(t, dependency.State())
			h.recoveryReconstitutionQueue()

			for _, step := range []struct {
				name    string
				ready   bool
				enqueue bool
			}{
				{name: "first informer visit", enqueue: true},
				{name: "unchanged state"},
				{name: "informer readiness transition", ready: true, enqueue: true},
				{name: "unchanged ready state", ready: true},
			} {
				t.Log(step.name)
				expectedState := apiv1.ResourceState{}
				if step.ready {
					expectedState = apiSlice.Status.Resources[0]
					h.informerStatus[informerSlice.Name] = apiSlice.Status
				}
				filled, err := h.source.populateCache(h.ctx, comp, syn, previous)
				require.NoError(t, err)
				assert.False(t, filled)
				assert.Empty(t, h.recoveryReconstitutionAPIReads())
				assert.Same(t, dependency, h.recoveryReconstitutionResource(syn.UUID, "dependency", apiSlice.Name, 0, true))
				assert.Same(t, dependent, h.recoveryReconstitutionResource(syn.UUID, "dependent", apiSlice.Name, 1, step.ready))
				assert.Equal(t, &expectedState, dependency.State())
				assert.Equal(t, &apiv1.ResourceState{}, dependent.State())
				if step.enqueue {
					h.recoveryReconstitutionQueue("dependency", "dependent")
				} else {
					h.recoveryReconstitutionQueue()
				}
			}
		})
	}
}

func TestRecoveryReconstitutionR11FullAPIManifestsAreRetained(t *testing.T) {
	for _, previous := range []bool{false, true} {
		t.Run(fmt.Sprintf("previous=%t", previous), func(t *testing.T) {
			syn := recoveryReconstitutionSynthesis("full", "first", "last")
			comp := recoveryReconstitutionComposition(nil, syn)
			if previous {
				comp = recoveryReconstitutionComposition(syn, nil)
			}
			slices := []*apiv1.ResourceSlice{
				recoveryReconstitutionSlice("first", syn.UUID, "first-resource", "second-resource"),
				recoveryReconstitutionSlice("last", syn.UUID, "last-resource"),
			}
			h := recoveryReconstitutionNewHarness(t, comp, slices, slices)
			for _, slice := range slices {
				cached, full := &apiv1.ResourceSlice{}, &apiv1.ResourceSlice{}
				require.NoError(t, h.source.client.Get(h.ctx, client.ObjectKeyFromObject(slice), cached))
				require.NoError(t, h.source.nonCachedReader.Get(h.ctx, client.ObjectKeyFromObject(slice), full))
				require.Len(t, cached.Spec.Resources, len(full.Spec.Resources))
				for i := range cached.Spec.Resources {
					assert.Empty(t, cached.Spec.Resources[i].Manifest)
					assert.Equal(t, slice.Spec.Resources[i].Manifest, full.Spec.Resources[i].Manifest)
				}
			}
			h.reads = nil

			filled, err := h.source.populateCache(h.ctx, comp, syn, previous)
			require.NoError(t, err)
			require.True(t, filled)
			assert.Equal(t, []string{"api/first", "api/last"}, h.recoveryReconstitutionAPIReads())
			for _, test := range []struct {
				name    string
				slice   string
				index   int
				visible bool
			}{
				{name: "first-resource", slice: "first", visible: true},
				{name: "second-resource", slice: "first", index: 1},
				{name: "last-resource", slice: "last", visible: true},
			} {
				res := h.recoveryReconstitutionResource(syn.UUID, test.name, test.slice, test.index, test.visible)
				snapshot, err := res.Snapshot(h.ctx, comp, nil)
				require.NoError(t, err)
				assert.Equal(t, map[string]interface{}{"source": "full-api", "resource": test.name}, snapshot.Unstructured().Object["data"])
				assert.Nil(t, res.State())
			}
			h.recoveryReconstitutionQueue()
		})
	}
}

func TestRecoveryReconstitutionR12ReconcileOrdersFillsThenEnqueues(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d slices per synthesis", count), func(t *testing.T) {
			previous := recoveryReconstitutionSynthesis("previous")
			current := recoveryReconstitutionSynthesis("current")
			var slices []*apiv1.ResourceSlice
			var previousNames, currentNames, expectedAPIReads []string
			for _, syn := range []*apiv1.Synthesis{previous, current} {
				for i := 0; i < count; i++ {
					name := fmt.Sprintf("%s-%d", syn.UUID, i)
					syn.ResourceSlices = append(syn.ResourceSlices, &apiv1.ResourceSliceRef{Name: name})
					slices = append(slices, recoveryReconstitutionSlice(name, syn.UUID, name+"-resource"))
					expectedAPIReads = append(expectedAPIReads, "api/"+name)
					if syn == previous {
						previousNames = append(previousNames, name+"-resource")
					} else {
						currentNames = append(currentNames, name+"-resource")
					}
				}
			}
			comp := recoveryReconstitutionComposition(previous, current)
			h := recoveryReconstitutionNewHarness(t, comp, slices, slices)

			result, err := h.recoveryReconstitutionReconcile()
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{Requeue: true}, result)
			assert.True(t, h.source.cache.Visit(h.ctx, comp, previous.UUID, nil))
			assert.False(t, h.source.cache.Visit(h.ctx, comp, current.UUID, nil))
			assert.Equal(t, expectedAPIReads[:count], h.recoveryReconstitutionAPIReads())
			for _, ref := range current.ResourceSlices {
				assert.NotContains(t, h.reads, "informer/"+ref.Name)
			}
			for _, ref := range previous.ResourceSlices {
				res := h.recoveryReconstitutionResource(previous.UUID, ref.Name+"-resource", ref.Name, 0, true)
				assert.Nil(t, res.State())
			}
			h.recoveryReconstitutionQueue()

			result, err = h.recoveryReconstitutionReconcile()
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{Requeue: true}, result)
			assert.Equal(t, expectedAPIReads, h.recoveryReconstitutionAPIReads())
			for _, ref := range previous.ResourceSlices {
				res := h.recoveryReconstitutionResource(previous.UUID, ref.Name+"-resource", ref.Name, 0, true)
				assert.Equal(t, &apiv1.ResourceState{}, res.State())
			}
			var currentResources []*resource.Resource
			for _, ref := range current.ResourceSlices {
				res := h.recoveryReconstitutionResource(current.UUID, ref.Name+"-resource", ref.Name, 0, true)
				assert.Nil(t, res.State(), "current Fill precedes the informer status visit")
				currentResources = append(currentResources, res)
			}
			h.recoveryReconstitutionQueue(previousNames...)

			result, err = h.recoveryReconstitutionReconcile()
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{}, result)
			assert.Equal(t, expectedAPIReads, h.recoveryReconstitutionAPIReads())
			for i, ref := range current.ResourceSlices {
				res := h.recoveryReconstitutionResource(current.UUID, ref.Name+"-resource", ref.Name, 0, true)
				assert.Same(t, currentResources[i], res)
				assert.Equal(t, &apiv1.ResourceState{}, res.State())
			}
			h.recoveryReconstitutionQueue(currentNames...)

			result, err = h.recoveryReconstitutionReconcile()
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{}, result)
			assert.Equal(t, expectedAPIReads, h.recoveryReconstitutionAPIReads())
			h.recoveryReconstitutionQueue()
		})
	}
}
