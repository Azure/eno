package resourceslice

import (
	"context"
	"errors"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func recoverySliceComposition(refs []*apiv1.ResourceSliceRef) *apiv1.Composition {
	return &apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{Name: "comp", Namespace: "default", UID: "comp-uid"},
		Status: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{
			UUID: "current", Synthesized: ptr.To(metav1.NewTime(time.Now().Add(-time.Minute))),
			ResourceSlices: refs, TombstoneRecoveryRequired: true,
		}},
	}
}

func TestRecoverySliceRequestsResynthesis(t *testing.T) {
	for _, test := range []struct {
		name       string
		ref        *apiv1.ResourceSliceRef
		writeError error
	}{
		{name: "nil"},
		{name: "empty", ref: &apiv1.ResourceSliceRef{}},
		{name: "missing", ref: &apiv1.ResourceSliceRef{Name: "missing"}},
		{name: "ignore side effects"},
		{name: "in flight"},
		{name: "already requested"},
		{name: "conflict", writeError: apierrors.NewConflict(schema.GroupResource{Group: apiv1.SchemeGroupVersion.Group, Resource: "compositions"}, "comp", errors.New("conflict"))},
		{name: "forbidden", writeError: apierrors.NewForbidden(schema.GroupResource{Group: apiv1.SchemeGroupVersion.Group, Resource: "compositions"}, "comp", errors.New("forbidden"))},
		{name: "other", writeError: errors.New("write unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := testutil.NewContext(t)
			comp := recoverySliceComposition([]*apiv1.ResourceSliceRef{test.ref})
			wantRequested, wantUpdates := true, 1
			switch test.name {
			case "ignore side effects":
				comp.EnableIgnoreSideEffects()
				wantRequested, wantUpdates = false, 0
			case "in flight":
				comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "inflight"}
				wantRequested, wantUpdates = false, 0
			case "already requested":
				comp.ForceResynthesis()
				wantUpdates = 0
			}
			updates, sliceReads := 0, 0
			cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
				Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					require.NotEmpty(t, key.Name)
					if _, ok := obj.(*apiv1.ResourceSlice); ok {
						sliceReads++
					}
					return cli.Get(ctx, key, obj, opts...)
				},
				Update: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					updates++
					if updates == 1 && test.writeError != nil {
						return test.writeError
					}
					return cli.Update(ctx, obj, opts...)
				},
			}, comp)
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
			before := comp.DeepCopy()
			c := &sliceController{client: cli, apiReader: cli}
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)}
			if test.writeError != nil {
				_, err := c.Reconcile(ctx, req)
				require.ErrorIs(t, err, test.writeError)
				require.NoError(t, cli.Get(ctx, req.NamespacedName, comp))
				assert.False(t, comp.ShouldForceResynthesis())
				assert.Equal(t, before.Status, comp.Status)
				wantUpdates++
			}
			for i := 0; i < 2; i++ {
				result, err := c.Reconcile(ctx, req)
				require.NoError(t, err)
				assert.Zero(t, result)
			}
			require.NoError(t, cli.Get(ctx, req.NamespacedName, comp))
			assert.Equal(t, wantRequested, comp.ShouldForceResynthesis())
			assert.Equal(t, wantUpdates, updates)
			assert.Equal(t, before.Status, comp.Status)
			if test.name != "missing" {
				assert.Zero(t, sliceReads)
			}
		})
	}
}

func TestRecoverySliceConfirmsMissingViaAPI(t *testing.T) {
	for _, apiCase := range []struct {
		name   string
		exists bool
		err    error
	}{
		{name: "missing"},
		{name: "exists", exists: true},
		{name: "forbidden", err: apierrors.NewForbidden(
			schema.GroupResource{Group: apiv1.SchemeGroupVersion.Group, Resource: "resourceslices"},
			"missing", errors.New("access denied"))},
		{name: "unavailable", err: errors.New("API unavailable")},
	} {
		for _, cacheCase := range []struct {
			name   string
			exists bool
		}{
			{name: "metadata cached", exists: true},
			{name: "metadata not cached"},
		} {
			t.Run(apiCase.name+"/"+cacheCase.name, func(t *testing.T) {
				ctx := testutil.NewContext(t)
				comp := recoverySliceComposition([]*apiv1.ResourceSliceRef{{Name: "missing"}})
				sliceKey := client.ObjectKey{Namespace: comp.Namespace, Name: "missing"}
				objects := []client.Object{comp}
				if apiCase.exists {
					objects = append(objects, &apiv1.ResourceSlice{ObjectMeta: metav1.ObjectMeta{
						Name: sliceKey.Name, Namespace: sliceKey.Namespace,
					}})
				}

				apiReads, updates := 0, 0
				api := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
					Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*metav1.PartialObjectMetadata); ok {
							apiReads++
							require.Equal(t, sliceKey, key)
							require.Equal(t, apiv1.SchemeGroupVersion.WithKind("ResourceSlice"), obj.GetObjectKind().GroupVersionKind())
							if apiCase.err != nil {
								return apiCase.err
							}
						}
						return cli.Get(ctx, key, obj, opts...)
					},
					Update: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						updates++
						return cli.Update(ctx, obj, opts...)
					},
				}, objects...)
				require.NoError(t, api.Get(ctx, client.ObjectKeyFromObject(comp), comp))
				before := comp.DeepCopy()

				sliceReads, metadataCacheReads := 0, 0
				cached := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
					Get: func(ctx context.Context, _ client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						switch obj.(type) {
						case *apiv1.ResourceSlice:
							sliceReads++
							require.Equal(t, sliceKey, key)
							return apierrors.NewNotFound(apiv1.SchemeGroupVersion.WithResource("resourceslices").GroupResource(), key.Name)
						case *metav1.PartialObjectMetadata:
							metadataCacheReads++
							if !cacheCase.exists {
								return apierrors.NewNotFound(apiv1.SchemeGroupVersion.WithResource("resourceslices").GroupResource(), key.Name)
							}
							obj.SetName(key.Name)
							obj.SetNamespace(key.Namespace)
							return nil
						default:
							return api.Get(ctx, key, obj, opts...)
						}
					},
					Update: func(ctx context.Context, _ client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						return api.Update(ctx, obj, opts...)
					},
				})

				c := &sliceController{client: cached, apiReader: api}
				result, err := c.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)})
				if apiCase.err != nil {
					require.ErrorIs(t, err, apiCase.err)
				} else {
					require.NoError(t, err)
				}
				assert.Zero(t, result)
				require.NoError(t, api.Get(ctx, client.ObjectKeyFromObject(comp), comp))
				wantRecovery := !apiCase.exists && apiCase.err == nil
				assert.Equal(t, wantRecovery, comp.ShouldForceResynthesis())
				assert.Equal(t, before.Status, comp.Status)
				wantUpdates := 0
				if wantRecovery {
					wantUpdates = 1
				}
				assert.Equal(t, wantUpdates, updates)
				assert.Equal(t, 1, sliceReads)
				assert.Equal(t, 1, apiReads)
				assert.Zero(t, metadataCacheReads, "metadata confirmation must bypass the informer cache")
			})
		}
	}
}

func TestRecoverySliceDeletingMalformedReferences(t *testing.T) {
	now := metav1.NewTime(time.Now().Truncate(time.Second))
	for _, test := range []struct {
		name       string
		states     []apiv1.ResourceState
		reconciled bool
		ready      bool
	}{
		{name: "status not yet written"},
		{name: "not deleted", states: []apiv1.ResourceState{{Reconciled: true}}},
		{name: "deleted", states: []apiv1.ResourceState{{Reconciled: true, Deleted: true}}, reconciled: true},
		{name: "deleted and ready", states: []apiv1.ResourceState{{Reconciled: true, Deleted: true, Ready: &now}}, reconciled: true, ready: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := testutil.NewContext(t)
			comp := recoverySliceComposition([]*apiv1.ResourceSliceRef{nil, {}, {Name: "available"}, nil})
			comp.Finalizers = []string{"eno.azure.io/cleanup"}
			comp.DeletionTimestamp = &now
			slice := &apiv1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "available", Namespace: comp.Namespace},
				Spec:       apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{{Manifest: "{}"}}},
				Status:     apiv1.ResourceSliceStatus{Resources: test.states},
			}
			cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
				Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					require.NotEmpty(t, key.Name)
					return cli.Get(ctx, key, obj, opts...)
				},
				Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
					t.Fatal("deleting composition must not request resynthesis")
					return nil
				},
			}, comp, slice)
			c := &sliceController{client: cli, apiReader: cli}
			_, err := c.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)})
			require.NoError(t, err)
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
			assert.False(t, comp.ShouldForceResynthesis())
			assert.Equal(t, test.reconciled, comp.Status.CurrentSynthesis.Reconciled != nil)
			assert.Equal(t, test.ready, comp.Status.CurrentSynthesis.Ready != nil)
			assert.True(t, comp.Status.CurrentSynthesis.TombstoneRecoveryRequired)
		})
	}
}

func TestRecoverySliceReadiness(t *testing.T) {
	for _, flag := range []bool{false, true} {
		name := "unflagged"
		if flag {
			name = "flagged"
		}
		t.Run(name, func(t *testing.T) {
			ctx := testutil.NewContext(t)
			comp := recoverySliceComposition([]*apiv1.ResourceSliceRef{{Name: "available"}})
			comp.Status.CurrentSynthesis.TombstoneRecoveryRequired = flag
			comp.Status.PreviousSynthesis = &apiv1.Synthesis{UUID: "previous", TombstoneRecoveryRequired: flag}
			previous := comp.Status.PreviousSynthesis.DeepCopy()
			slice := &apiv1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "available", Namespace: comp.Namespace},
				Spec:       apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{{Manifest: "{}"}}},
			}
			cli := testutil.NewClient(t, comp, slice)
			c := &sliceController{client: cli, apiReader: cli}
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)}
			_, err := c.Reconcile(ctx, req)
			require.NoError(t, err)
			require.NoError(t, cli.Get(ctx, req.NamespacedName, comp))
			assert.Nil(t, comp.Status.CurrentSynthesis.Reconciled)
			assert.Nil(t, comp.Status.CurrentSynthesis.Ready)

			slice.Status.Resources = []apiv1.ResourceState{{Reconciled: true}}
			require.NoError(t, cli.Status().Update(ctx, slice))
			_, err = c.Reconcile(ctx, req)
			require.NoError(t, err)
			require.NoError(t, cli.Get(ctx, req.NamespacedName, comp))
			assert.NotNil(t, comp.Status.CurrentSynthesis.Reconciled)
			assert.Nil(t, comp.Status.CurrentSynthesis.Ready)

			now := metav1.NewTime(time.Now().Truncate(time.Second))
			slice.Status.Resources[0].Ready = &now
			require.NoError(t, cli.Status().Update(ctx, slice))
			_, err = c.Reconcile(ctx, req)
			require.NoError(t, err)
			require.NoError(t, cli.Get(ctx, req.NamespacedName, comp))
			assert.NotNil(t, comp.Status.CurrentSynthesis.Reconciled)
			require.NotNil(t, comp.Status.CurrentSynthesis.Ready)
			assert.True(t, now.Equal(comp.Status.CurrentSynthesis.Ready))
			assert.Equal(t, flag, comp.Status.CurrentSynthesis.TombstoneRecoveryRequired)
			assert.Equal(t, previous, comp.Status.PreviousSynthesis)
			assert.False(t, comp.ShouldForceResynthesis())
		})
	}
}

func TestRecoveryCleanupEvents(t *testing.T) {
	makeComp := func(suffix string) *apiv1.Composition {
		synthesis := func(name string) *apiv1.Synthesis {
			return &apiv1.Synthesis{ResourceSlices: []*apiv1.ResourceSliceRef{nil, {}, {Name: name + suffix}, {Name: "shared"}}}
		}
		return &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{Name: "comp", Namespace: "default"},
			Status: apiv1.CompositionStatus{
				InFlightSynthesis: synthesis("inflight"), CurrentSynthesis: synthesis("current"), PreviousSynthesis: synthesis("previous"),
			},
		}
	}
	for _, name := range []string{"create", "update", "delete", "unknown delete", "nil syntheses"} {
		t.Run(name, func(t *testing.T) {
			ctx := testutil.NewContext(t)
			queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			t.Cleanup(queue.ShutDown)
			handler := (&cleanupController{}).newCompEventHandler()
			old, next := makeComp("-old"), makeComp("-new")
			before := next.DeepCopy()
			var want []string
			switch name {
			case "create":
				handler.Create(ctx, event.TypedCreateEvent[*apiv1.Composition]{Object: next}, queue)
				want = []string{"inflight-new", "current-new", "previous-new", "shared"}
			case "update":
				handler.Update(ctx, event.TypedUpdateEvent[*apiv1.Composition]{ObjectOld: old, ObjectNew: next}, queue)
				want = []string{"inflight-new", "current-new", "previous-new", "inflight-old", "current-old", "previous-old", "shared"}
			case "delete":
				handler.Delete(ctx, event.TypedDeleteEvent[*apiv1.Composition]{Object: next}, queue)
				want = []string{"inflight-new", "current-new", "previous-new", "shared"}
			case "unknown delete":
				handler.Delete(ctx, event.TypedDeleteEvent[*apiv1.Composition]{Object: next, DeleteStateUnknown: true}, queue)
			case "nil syntheses":
				handler.Create(ctx, event.TypedCreateEvent[*apiv1.Composition]{Object: &apiv1.Composition{}}, queue)
			}
			var got []string
			for queue.Len() > 0 {
				req, shutdown := queue.Get()
				require.False(t, shutdown)
				assert.NotEmpty(t, req.Name)
				assert.Equal(t, "default", req.Namespace)
				got = append(got, req.Name)
				queue.Done(req)
			}
			assert.ElementsMatch(t, want, got)
			assert.Equal(t, before, next)
		})
	}
}

func TestRecoveryCleanupReferences(t *testing.T) {
	for _, test := range []struct {
		name       string
		location   string
		deleting   bool
		reconciled bool
		keep       bool
	}{
		{name: "current", location: "current", keep: true},
		{name: "previous", location: "previous", keep: true},
		{name: "inflight", location: "inflight", keep: true},
		{name: "unreferenced"},
		{name: "finalizer/current unreconciled", location: "current", deleting: true, keep: true},
		{name: "finalizer/current reconciled", location: "current", deleting: true, reconciled: true},
		{name: "finalizer/previous only", location: "previous", deleting: true},
		{name: "finalizer/unreferenced", deleting: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := testutil.NewContext(t)
			comp := recoverySliceComposition([]*apiv1.ResourceSliceRef{nil, {}, {Name: "other"}})
			comp.Status.PreviousSynthesis = &apiv1.Synthesis{ResourceSlices: []*apiv1.ResourceSliceRef{nil, {}, {Name: "older"}}}
			switch test.location {
			case "current":
				comp.Status.CurrentSynthesis.ResourceSlices = append(comp.Status.CurrentSynthesis.ResourceSlices, &apiv1.ResourceSliceRef{Name: "target"})
			case "previous":
				comp.Status.PreviousSynthesis.ResourceSlices = append(comp.Status.PreviousSynthesis.ResourceSlices, &apiv1.ResourceSliceRef{Name: "target"})
			case "inflight":
				comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "producing"}
			}
			if test.reconciled {
				comp.Status.CurrentSynthesis.Reconciled = ptr.To(metav1.Now())
			}
			slice := &apiv1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name: "target", Namespace: comp.Namespace, UID: "slice-uid",
					CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute)),
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: apiv1.SchemeGroupVersion.String(), Kind: "Composition", Name: comp.Name, UID: comp.UID, Controller: ptr.To(true),
					}},
				},
				Spec: apiv1.ResourceSliceSpec{SynthesisUUID: "producing"},
			}
			if test.deleting {
				slice.Finalizers = []string{"eno.azure.io/cleanup"}
				slice.DeletionTimestamp = ptr.To(metav1.Now())
			}
			cli := testutil.NewClient(t, comp, slice)
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
			before := comp.DeepCopy()
			c := &cleanupController{client: cli, noCacheReader: cli}
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(slice)}
			_, err := c.Reconcile(ctx, req)
			require.NoError(t, err)
			err = cli.Get(ctx, req.NamespacedName, slice)
			if test.keep {
				require.NoError(t, err)
				if test.deleting {
					assert.Equal(t, []string{"eno.azure.io/cleanup"}, slice.Finalizers)
				}
			} else {
				assert.True(t, apierrors.IsNotFound(err), "unneeded slice (and any cleanup finalizer) should be removed: %v", err)
			}
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
			assert.Equal(t, before.Status, comp.Status)
		})
	}
}
