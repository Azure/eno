package backup

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type backupTestManager struct {
	ctrl.Manager
	httpClient *http.Client
}

func (m backupTestManager) GetHTTPClient() *http.Client { return m.httpClient }

type backupTestRoundTripper func(*http.Request) (*http.Response, error)

func (f backupTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestBackupControllerNamespaceIsolation(t *testing.T) {
	mgr := testutil.NewManager(t, testutil.WithCompositionNamespace(metav1.NamespaceAll))
	ctx := t.Context()
	upstreamConfig := rest.CopyConfig(mgr.RestConfig)
	upstreamConfig.QPS = 200
	upstream, err := client.New(upstreamConfig, client.Options{Scheme: mgr.GetScheme()})
	require.NoError(t, err)
	downstreamConfig := rest.CopyConfig(mgr.DownstreamRestConfig)
	downstreamConfig.QPS = 200
	downstream, err := client.New(downstreamConfig, client.Options{Scheme: mgr.GetScheme()})
	require.NoError(t, err)
	for _, namespace := range []string{"backup-system", "other-system"} {
		require.NoError(t, upstream.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	}

	comps := []*apiv1.Composition{
		{ObjectMeta: metav1.ObjectMeta{Name: "selected", Namespace: "backup-system", Labels: map[string]string{"backup": "enabled"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "selected", Namespace: "other-system", Labels: map[string]string{"backup": "enabled"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "excluded", Namespace: "backup-system", Labels: map[string]string{"backup": "disabled"}}},
	}
	slices := make([]*apiv1.ResourceSlice, len(comps))
	now := metav1.Now()
	for i, comp := range comps {
		comp.Spec.Synthesizer.Name = "test"
		require.NoError(t, upstream.Create(ctx, comp))
		slices[i] = &apiv1.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name: comp.Name, Namespace: comp.Namespace,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(comp, apiv1.SchemeGroupVersion.WithKind("Composition"))},
			},
			Spec: apiv1.ResourceSliceSpec{
				SynthesisUUID: controllerTestUUID(i + 1),
				Resources:     []apiv1.Manifest{inventoryTestManifest(t, controllerTestResource(comp.Name))},
			},
		}
		require.NoError(t, upstream.Create(ctx, slices[i]))
		comp.Status.CurrentSynthesis = &apiv1.Synthesis{
			UUID: slices[i].Spec.SynthesisUUID, Synthesized: &now, Ready: &now,
			TombstoneRecoveryRequired: true,
			ResourceSlices:            []*apiv1.ResourceSliceRef{{Name: slices[i].Name}},
		}
		require.NoError(t, upstream.Status().Update(ctx, comp))
	}

	type cacheRequest struct {
		path, selector, accept string
		watch                  bool
	}
	var mu sync.Mutex
	var requests []cacheRequest
	httpClient := *mgr.GetHTTPClient()
	transport := httpClient.Transport
	httpClient.Transport = backupTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet && (strings.HasSuffix(req.URL.Path, "/compositions") || strings.HasSuffix(req.URL.Path, "/resourceslices")) {
			mu.Lock()
			requests = append(requests, cacheRequest{
				path: req.URL.Path, selector: req.URL.Query().Get("labelSelector"),
				accept: req.Header.Get("Accept"), watch: req.URL.Query().Get("watch") == "true",
			})
			mu.Unlock()
		}
		return transport.RoundTrip(req)
	})
	require.NoError(t, NewController(backupTestManager{Manager: mgr.Manager, httpClient: &httpClient}, Options{
		Enabled: true, Namespace: "backup-system", CompositionSelector: labels.SelectorFromSet(labels.Set{"backup": "enabled"}),
		Downstream: mgr.DownstreamRestConfig,
	}))
	mgr.Start(t)

	inventoryKey := client.ObjectKey{Namespace: inventoryNamespace, Name: inventoryName(comps[0], controllerTestUUID(1))}
	waitForInventory := func() {
		t.Helper()
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			inventory := &corev1.ConfigMap{}
			require.NoError(c, downstream.Get(ctx, inventoryKey, inventory))
			resources, err := decodeInventorySnapshot(comps[0], *inventory)
			require.NoError(c, err)
			require.Equal(c, []inventoryResource{controllerTestResource("selected")}, resources,
				"inventory must use full APIReader manifests, not metadata-only cached slices")
		}, 10*time.Second, 10*time.Millisecond)
	}
	waitForInventory()
	observed := &apiv1.Composition{}
	require.NoError(t, upstream.Get(ctx, client.ObjectKeyFromObject(comps[0]), observed))
	require.True(t, observed.Status.CurrentSynthesis.TombstoneRecoveryComplete())
	require.Equal(t, reasonInventoryNotFound, observed.Status.CurrentSynthesis.TombstoneRecoveryFinished.Reason)

	// The main manager still caches every namespace and does not inherit the backup selector.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		allComps := &apiv1.CompositionList{}
		require.NoError(c, mgr.GetClient().List(ctx, allComps))
		require.Len(c, allComps.Items, len(comps))
		allSlices := &apiv1.ResourceSliceList{}
		require.NoError(c, mgr.GetClient().List(ctx, allSlices))
		require.Len(c, allSlices.Items, len(slices))
	}, 10*time.Second, 10*time.Millisecond)

	for _, change := range []string{"status", "metadata"} {
		// No Composition event or explicit requeue may recreate this inventory.
		require.NoError(t, downstream.Delete(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Name: inventoryKey.Name, Namespace: inventoryKey.Namespace,
		}}))
		require.Eventually(t, func() bool {
			return apierrors.IsNotFound(downstream.Get(ctx, inventoryKey, &corev1.ConfigMap{}))
		}, time.Second, 10*time.Millisecond)
		require.Never(t, func() bool {
			return !apierrors.IsNotFound(downstream.Get(ctx, inventoryKey, &corev1.ConfigMap{}))
		}, 300*time.Millisecond, 50*time.Millisecond, "inventory must remain absent until an owner event")
		for _, slice := range slices {
			if change == "status" {
				slice.Status.Resources = []apiv1.ResourceState{{Reconciled: true}}
				require.NoError(t, upstream.Status().Update(ctx, slice))
			} else {
				slice.Annotations = map[string]string{"test.example/owner-watch": "updated"}
				require.NoError(t, upstream.Update(ctx, slice))
			}
		}
		waitForInventory()
	}

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		mu.Lock()
		defer mu.Unlock()
		seen := map[string]map[bool]bool{}
		for _, req := range requests {
			resource := req.path[strings.LastIndex(req.path, "/")+1:]
			require.Equal(c, "/apis/"+apiv1.SchemeGroupVersion.String()+"/namespaces/backup-system/"+resource, req.path,
				"every backup LIST/WATCH must be namespace-restricted at the API")
			if resource == "compositions" {
				require.Equal(c, "backup=enabled", req.selector)
			} else {
				require.Empty(c, req.selector, "slice owner watches must not require Composition labels")
				require.Contains(c, req.accept, "as=PartialObjectMetadata")
			}
			if seen[resource] == nil {
				seen[resource] = map[bool]bool{}
			}
			seen[resource][req.watch] = true
		}
		for _, resource := range []string{"compositions", "resourceslices"} {
			require.True(c, seen[resource][false], "missing %s LIST", resource)
			require.True(c, seen[resource][true], "missing %s WATCH", resource)
		}
	}, 10*time.Second, 10*time.Millisecond)
	for range 5 {
		for _, comp := range comps[1:] {
			got := &apiv1.Composition{}
			require.NoError(t, upstream.Get(ctx, client.ObjectKeyFromObject(comp), got))
			require.Equal(t, comp, got, "ignored Compositions must remain unchanged even after owner events")
			key := client.ObjectKey{Namespace: inventoryNamespace, Name: inventoryName(comp, comp.Status.CurrentSynthesis.UUID)}
			err := downstream.Get(ctx, key, &corev1.ConfigMap{})
			require.True(t, apierrors.IsNotFound(err), "ignored inventory must not exist, got %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestBackupControllerCompositionWatchAdvancesPhases(t *testing.T) {
	mgr := testutil.NewManager(t)
	require.NoError(t, NewController(mgr.Manager, Options{Enabled: true, Namespace: "default", Downstream: mgr.DownstreamRestConfig}))
	mgr.Start(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	upstream, err := client.NewWithWatch(mgr.RestConfig, client.Options{Scheme: mgr.GetScheme()})
	require.NoError(t, err)
	downstream, err := client.New(mgr.DownstreamRestConfig, client.Options{Scheme: mgr.GetScheme()})
	require.NoError(t, err)
	comp := &apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{Name: "watch-phases", Namespace: "default"},
		Spec:       apiv1.CompositionSpec{Synthesizer: apiv1.SynthesizerRef{Name: "test"}},
	}
	require.NoError(t, upstream.Create(ctx, comp))
	events, err := upstream.Watch(ctx, &apiv1.CompositionList{}, client.InNamespace(comp.Namespace),
		client.MatchingFields{"metadata.name": comp.Name},
		&client.ListOptions{Raw: &metav1.ListOptions{ResourceVersion: comp.ResourceVersion}})
	require.NoError(t, err)
	defer events.Stop()

	// No ResourceSlices or other controllers can enqueue work between these status transitions.
	now := metav1.Now()
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		UUID: controllerTestUUID(2), Synthesized: &now, Ready: &now,
		TombstoneRecoveryRequired: true,
	}
	require.NoError(t, upstream.Status().Update(ctx, comp))

	var reasons []string
	for len(reasons) < 1 {
		select {
		case event, ok := <-events.ResultChan():
			require.True(t, ok, "Composition watch closed before recovery completed")
			if event.Type == watch.Error {
				t.Fatalf("Composition watch failed: %v", apierrors.FromObject(event.Object))
			}
			observed, ok := event.Object.(*apiv1.Composition)
			require.True(t, ok, "unexpected watch object %T", event.Object)
			syn := observed.Status.CurrentSynthesis
			if syn == nil || syn.TombstoneRecoveryFinished == nil {
				continue
			}
			status := syn.TombstoneRecoveryFinished
			require.Equal(t, controllerTestUUID(2), status.SynthesisUUID)
			require.True(t, status.Status)
			reasons = append(reasons, status.Reason)
		case <-ctx.Done():
			t.Fatalf("recovery did not advance through Composition watch events: %v (observed %v)", ctx.Err(), reasons)
		}
	}
	require.Equal(t, []string{reasonInventoryNotFound}, reasons)

	inventory := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: inventoryNamespace, Name: inventoryName(comp, controllerTestUUID(2))}
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		require.NoError(c, downstream.Get(ctx, key, inventory))
	}, 10*time.Second, 10*time.Millisecond, "terminal status watch event must trigger inventory recording")
	resources, err := decodeInventorySnapshot(comp, *inventory)
	require.NoError(t, err)
	require.Empty(t, resources)
}

func TestBackupControllerAPIPreconditions(t *testing.T) {
	mgr := testutil.NewManager(t)
	upstream, err := client.New(mgr.RestConfig, client.Options{Scheme: mgr.GetScheme()})
	require.NoError(t, err)
	downstream, err := client.New(mgr.DownstreamRestConfig, client.Options{Scheme: mgr.GetScheme()})
	require.NoError(t, err)
	c := &backupController{client: upstream, reader: upstream, downstream: downstream}
	ctx := t.Context()
	comp := &apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{Name: "api-preconditions", Namespace: "default"},
		Spec:       apiv1.CompositionSpec{Synthesizer: apiv1.SynthesizerRef{Name: "test"}},
	}
	require.NoError(t, upstream.Create(ctx, comp))
	now := metav1.Now()
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		UUID: controllerTestUUID(2), Synthesized: &now, Ready: &now,
	}
	require.NoError(t, upstream.Status().Update(ctx, comp))

	for _, field := range []string{"uid", "resourceVersion", "currentUUID"} {
		t.Run("status-"+field, func(t *testing.T) {
			before := comp.DeepCopy()
			switch field {
			case "uid":
				before.UID = "stale-uid"
			case "resourceVersion":
				before.ResourceVersion = "0"
			case "currentUUID":
				before.Status.CurrentSynthesis.UUID = controllerTestUUID(1)
			}
			require.Error(t, c.markTombstoneRecoveryFinished(ctx, before, reasonNotNeeded, "", nil),
				"each JSON patch test must independently reject a stale operation")
			got := &apiv1.Composition{}
			require.NoError(t, upstream.Get(ctx, client.ObjectKeyFromObject(comp), got))
			assert.Equal(t, comp, got)
		})
	}

	t.Run("slice-resourceVersion", func(t *testing.T) {
		slice := &apiv1.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: "optimistic-append", Namespace: comp.Namespace},
			Spec: apiv1.ResourceSliceSpec{
				SynthesisUUID: comp.Status.CurrentSynthesis.UUID,
				Resources:     []apiv1.Manifest{inventoryTestManifest(t, controllerTestResource("desired"))},
			},
		}
		require.NoError(t, upstream.Create(ctx, slice))
		stale := slice.DeepCopy()
		slice.Spec.Resources = append(slice.Spec.Resources, inventoryTestManifest(t, controllerTestResource("concurrent")))
		require.NoError(t, upstream.Update(ctx, slice))
		tombstone := inventoryTestManifest(t, controllerTestResource("removed"))
		tombstone.Deleted = true
		refs, err := c.writeTombstones(ctx, comp, []apiv1.ResourceSlice{*stale}, []apiv1.Manifest{tombstone})
		require.True(t, apierrors.IsConflict(err), "expected optimistic-lock conflict, got %v", err)
		assert.Nil(t, refs)
		got := &apiv1.ResourceSlice{}
		require.NoError(t, upstream.Get(ctx, client.ObjectKeyFromObject(slice), got))
		assert.Equal(t, slice, got, "a stale append must not overwrite concurrent resources")
	})

	for _, mode := range []string{"mutation", "replacement"} {
		t.Run("inventory-delete-"+mode, func(t *testing.T) {
			oldComp := comp.DeepCopy()
			if mode == "mutation" {
				oldComp.Status.CurrentSynthesis.UUID = controllerTestUUID(3)
			} else {
				oldComp.Status.CurrentSynthesis.UUID = controllerTestUUID(4)
			}
			item, err := makeInventory(oldComp, nil)
			require.NoError(t, err)
			require.NoError(t, downstream.Create(ctx, item))
			listed := item.DeepCopy()
			if mode == "mutation" {
				item.Annotations["test.example/changed"] = "true"
				require.NoError(t, downstream.Update(ctx, item))
			} else {
				require.NoError(t, downstream.Delete(ctx, item))
				item, err = makeInventory(oldComp, nil)
				require.NoError(t, err)
				require.NoError(t, downstream.Create(ctx, item))
				require.NotEqual(t, listed.UID, item.UID)
				// Isolate the UID precondition: the resourceVersion is otherwise current.
				listed.ResourceVersion = item.ResourceVersion
			}
			keep, err := makeInventory(comp, nil)
			require.NoError(t, err)
			err = c.deleteOtherInventories(ctx, comp, keep, []corev1.ConfigMap{*listed})
			require.True(t, apierrors.IsConflict(err), "expected delete precondition conflict, got %v", err)
			got := &corev1.ConfigMap{}
			require.NoError(t, downstream.Get(ctx, client.ObjectKeyFromObject(item), got))
			assert.Equal(t, item, got, "concurrent inventory changes must survive cleanup")
		})
	}
}
