package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func gcTestSource() (*apiv1.Composition, *apiv1.Symphony) {
	symph := &apiv1.Symphony{ObjectMeta: metav1.ObjectMeta{
		Name: "symphony", Namespace: "tenant", UID: "symphony-uid",
	}}
	comp := &apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "removed", Namespace: symph.Namespace, UID: "composition-uid",
			Labels:          map[string]string{"backup-scope": "owned"},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(symph, apiv1.SchemeGroupVersion.WithKind("Symphony"))},
		},
		Spec: apiv1.CompositionSpec{Synthesizer: apiv1.SynthesizerRef{Name: "retired"}},
	}
	return comp, symph
}

func gcTestClient(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.SchemeBuilder.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func gcTestInventory(t *testing.T, comp *apiv1.Composition, sequence int, legacy bool) *corev1.ConfigMap {
	t.Helper()
	owner := metav1.GetControllerOf(comp)
	require.NotNil(t, owner)
	source := &inventorySourceComposition{
		Name: comp.Name, UID: comp.UID, Labels: comp.Labels, Annotations: comp.Annotations,
		Symphony: &inventorySourceSymphony{Name: owner.Name, UID: owner.UID},
	}
	if legacy {
		source = nil
	}
	uuid := fmt.Sprintf("00000000-0000-4000-8000-%012d", sequence)
	data, err := json.Marshal(inventory{
		FormatVersion:        inventoryFormatVersion,
		CompositionNamespace: comp.Namespace,
		SynthesizerName:      comp.Spec.Synthesizer.Name,
		SynthesisUUID:        uuid,
		Synthesized:          metav1.NewTime(time.Unix(1700000000+int64(sequence), 0)),
		Resources:            []inventoryResource{},
		SourceComposition:    source,
	})
	require.NoError(t, err)
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: inventoryName(comp, uuid), Namespace: inventoryNamespace,
			UID: types.UID(fmt.Sprintf("inventory-%d", sequence)), ResourceVersion: fmt.Sprint(sequence),
			Labels: map[string]string{inventoryLineageLabel: inventoryLineage(comp)},
		},
		Data: map[string]string{inventoryDataKey: string(data)},
	}
}

func gcTestController(t *testing.T, symph *apiv1.Symphony, objects ...client.Object) *backupController {
	t.Helper()
	upstream := gcTestClient(t, symph)
	return &backupController{
		enabled: true, client: upstream, reader: upstream, downstream: gcTestClient(t, objects...),
		compositionNamespace: symph.Namespace,
		compositionSelector:  labels.SelectorFromSet(labels.Set{"backup-scope": "owned"}),
	}
}

func gcTestRecordDeletes(t *testing.T, downstream client.Client) (client.WithWatch, *[]string) {
	t.Helper()
	watched, ok := downstream.(client.WithWatch)
	require.True(t, ok)
	var deleted []string
	return interceptor.NewClient(watched, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			stored := &corev1.ConfigMap{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(obj), stored))
			options := (&client.DeleteOptions{}).ApplyOptions(opts)
			require.NotNil(t, options.Preconditions)
			require.NotEmpty(t, stored.UID)
			require.NotEmpty(t, stored.ResourceVersion)
			require.Equal(t, &stored.UID, options.Preconditions.UID)
			require.Equal(t, &stored.ResourceVersion, options.Preconditions.ResourceVersion)
			require.Equal(t, stored.UID, obj.GetUID())
			require.Equal(t, stored.ResourceVersion, obj.GetResourceVersion())
			deleted = append(deleted, obj.GetName())
			return c.Delete(ctx, obj, opts...)
		},
	}), &deleted
}

func gcTestAssertRetained(t *testing.T, downstream client.Client, objects ...client.Object) {
	t.Helper()
	for _, obj := range objects {
		actual := &corev1.ConfigMap{}
		require.NoError(t, downstream.Get(context.Background(), client.ObjectKeyFromObject(obj), actual))
		require.Equal(t, obj, actual)
	}
}

func TestBackupGCVariationEligibility(t *testing.T) {
	readErr := errors.New("authoritative read failed")
	tests := []struct {
		name     string
		arrange  func(*apiv1.Composition, *apiv1.Symphony) []client.Object
		failRead string
		eligible bool
	}{
		{name: "removed variation with absent source and same living Symphony", eligible: true},
		{name: "active source Composition", arrange: func(comp *apiv1.Composition, symph *apiv1.Symphony) []client.Object {
			return []client.Object{symph, comp.DeepCopy()}
		}},
		{name: "replacement Composition with same name", arrange: func(comp *apiv1.Composition, symph *apiv1.Symphony) []client.Object {
			replacement := comp.DeepCopy()
			replacement.UID = "replacement-uid"
			replacement.Spec.Synthesizer.Name = "different"
			return []client.Object{symph, replacement}
		}},
		{name: "another Composition using same lineage", arrange: func(comp *apiv1.Composition, symph *apiv1.Symphony) []client.Object {
			other := comp.DeepCopy()
			other.Name, other.UID = "other", "other-uid"
			return []client.Object{symph, other}
		}},
		{name: "variation still desired", arrange: func(comp *apiv1.Composition, symph *apiv1.Symphony) []client.Object {
			symph.Spec.Variations = []apiv1.Variation{{Synthesizer: comp.Spec.Synthesizer}}
			return []client.Object{symph}
		}},
		{name: "Symphony absent", arrange: func(*apiv1.Composition, *apiv1.Symphony) []client.Object {
			return nil
		}},
		{name: "Symphony deleting", arrange: func(_ *apiv1.Composition, symph *apiv1.Symphony) []client.Object {
			now := metav1.NewTime(time.Unix(1700000000, 0))
			symph.DeletionTimestamp = &now
			symph.Finalizers = []string{"test.eno.azure.io/hold"}
			return []client.Object{symph}
		}},
		{name: "Symphony replaced", arrange: func(_ *apiv1.Composition, symph *apiv1.Symphony) []client.Object {
			symph.UID = "replacement-symphony-uid"
			return []client.Object{symph}
		}},
		{name: "insufficient owner metadata", arrange: func(comp *apiv1.Composition, symph *apiv1.Symphony) []client.Object {
			comp.OwnerReferences[0].UID = ""
			return []client.Object{symph}
		}},
		{name: "Symphony teardown label", arrange: func(comp *apiv1.Composition, symph *apiv1.Symphony) []client.Object {
			comp.Labels["eno.azure.io/symphony-deleting"] = "true"
			return []client.Object{symph}
		}},
		{name: "authoritative Composition list failure", failRead: "list"},
		{name: "authoritative Symphony get failure", failRead: "get"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comp, symph := gcTestSource()
			objects := []client.Object{symph}
			if tt.arrange != nil {
				objects = tt.arrange(comp, symph)
			}
			reader := interceptor.NewClient(gcTestClient(t, objects...), interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if tt.failRead == "list" {
						return readErr
					}
					return c.List(ctx, list, opts...)
				},
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if tt.failRead == "get" {
						return readErr
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})
			eligible, err := variationInventoryCanBeDeleted(context.Background(), reader, comp)
			require.Equal(t, tt.eligible, eligible)
			if tt.failRead != "" {
				require.ErrorIs(t, err, readErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBackupGCRetirementTriggers(t *testing.T) {
	for _, trigger := range []string{"composition deletion event", "periodic without event"} {
		t.Run(trigger, func(t *testing.T) {
			ctx := context.Background()
			comp, symph := gcTestSource()
			matching := gcTestInventory(t, comp, 1, false)
			legacy := gcTestInventory(t, comp, 2, true)
			unowned := comp.DeepCopy()
			unowned.Name, unowned.UID = "unowned", "unowned-uid"
			unowned.Labels["backup-scope"] = "other"
			outOfScope := gcTestInventory(t, unowned, 3, false)
			unrelated := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name: "unrelated", Namespace: inventoryNamespace, UID: "unrelated-uid", ResourceVersion: "1",
			}}
			otherNamespace := matching.DeepCopy()
			otherNamespace.Namespace = "other"
			c := gcTestController(t, symph, matching, legacy, outOfScope, unrelated, otherNamespace)
			var deleted *[]string
			c.downstream, deleted = gcTestRecordDeletes(t, c.downstream)
			if trigger == "composition deletion event" {
				q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
				defer q.ShutDown()
				c.newDeletedCompositionHandler().Delete(ctx, event.TypedDeleteEvent[*apiv1.Composition]{Object: comp}, q)
				require.Equal(t, 1, q.Len())
				req, shutdown := q.Get()
				require.False(t, shutdown)
				require.Equal(t, client.ObjectKeyFromObject(comp), req.NamespacedName)
				_, err := c.Reconcile(ctx, req)
				q.Done(req)
				require.NoError(t, err)
			} else {
				g := &inventoryGC{reader: c.reader, downstream: c.downstream, owns: c.ownsComposition, logger: logr.Discard()}
				g.sweep(ctx)
			}
			require.Equal(t, []string{matching.Name}, *deleted)
			require.True(t, apierrors.IsNotFound(c.downstream.Get(ctx, client.ObjectKeyFromObject(matching), &corev1.ConfigMap{})))
			gcTestAssertRetained(t, c.downstream, legacy, outOfScope, unrelated, otherNamespace)
		})
	}
}

func TestBackupGCInvalidLineageRetained(t *testing.T) {
	for _, trigger := range []string{"retirement", "periodic"} {
		t.Run(trigger, func(t *testing.T) {
			comp, symph := gcTestSource()
			valid := gcTestInventory(t, comp, 1, false)
			invalid := gcTestInventory(t, comp, 2, false)
			invalid.Data[inventoryDataKey] = `{"formatVersion":`
			c := gcTestController(t, symph, valid, invalid)
			var deleted *[]string
			c.downstream, deleted = gcTestRecordDeletes(t, c.downstream)
			if trigger == "retirement" {
				err := c.deleteRemovedVariationInventory(context.Background(), comp)
				var invalidErr *invalidInventoryError
				require.ErrorAs(t, err, &invalidErr)
			} else {
				g := &inventoryGC{reader: c.reader, downstream: c.downstream, owns: c.ownsComposition, logger: logr.Discard()}
				g.sweep(context.Background())
			}
			require.Empty(t, *deleted)
			gcTestAssertRetained(t, c.downstream, valid, invalid)
		})
	}
}

func TestBackupGCFreshAuthoritativeRecheck(t *testing.T) {
	ctx := context.Background()
	comp, symph := gcTestSource()
	item := gcTestInventory(t, comp, 1, false)
	c := gcTestController(t, symph, item)
	var deleted *[]string
	c.downstream, deleted = gcTestRecordDeletes(t, c.downstream)
	replacement := comp.DeepCopy()
	replacement.UID = "replacement-uid"
	created := false
	reader := interceptor.NewClient(gcTestClient(t, symph), interceptor.Funcs{
		List: func(ctx context.Context, cli client.WithWatch, obj client.ObjectList, opts ...client.ListOption) error {
			err := cli.List(ctx, obj, opts...)
			if _, ok := obj.(*apiv1.CompositionList); ok && err == nil && !created {
				// The response is already read when a replacement appears.
				require.NoError(t, cli.Create(ctx, replacement))
				created = true
			}
			return err
		},
	})
	g := &inventoryGC{reader: reader, downstream: c.downstream, owns: c.ownsComposition, logger: logr.Discard()}
	g.sweep(ctx)
	require.True(t, created)
	require.Empty(t, *deleted)
	gcTestAssertRetained(t, c.downstream, item)
}

func TestBackupGCPaginatedBudgetResume(t *testing.T) {
	ctx := context.Background()
	comp, symph := gcTestSource()
	first := gcTestInventory(t, comp, 1, false)
	last := gcTestInventory(t, comp, 2, false)
	c := gcTestController(t, symph, first, last)
	downstream, deleted := gcTestRecordDeletes(t, c.downstream)
	listTokens := map[string][]string{}
	reads := map[string]int{}
	interrupted, lastRead := false, false
	c.downstream = interceptor.NewClient(downstream, interceptor.Funcs{
		List: func(_ context.Context, _ client.WithWatch, obj client.ObjectList, opts ...client.ListOption) error {
			options := (&client.ListOptions{}).ApplyOptions(opts)
			require.Equal(t, inventoryNamespace, options.Namespace)
			require.EqualValues(t, 100, options.Limit)
			require.IsType(t, &metav1.PartialObjectMetadataList{}, obj)
			scope := "lineage"
			if options.LabelSelector.String() == inventoryLineageLabel {
				scope = "discovery"
			} else {
				require.Equal(t, labels.SelectorFromSet(labels.Set{inventoryLineageLabel: inventoryLineage(comp)}).String(), options.LabelSelector.String())
			}
			listTokens[scope] = append(listTokens[scope], options.Continue)
			list := obj.(*metav1.PartialObjectMetadataList)
			if options.Continue == "" {
				list.Items = []metav1.PartialObjectMetadata{{ObjectMeta: *first.ObjectMeta.DeepCopy()}}
				list.Continue = scope + "-next"
			} else {
				require.Equal(t, scope+"-next", options.Continue)
				list.Items = []metav1.PartialObjectMetadata{{ObjectMeta: *last.ObjectMeta.DeepCopy()}}
				list.Continue = ""
			}
			return nil
		},
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			reads[key.Name]++
			if key == client.ObjectKeyFromObject(last) && !interrupted {
				interrupted = true
				return errGCBudget
			}
			err := c.Get(ctx, key, obj, opts...)
			if key == client.ObjectKeyFromObject(last) && err == nil {
				lastRead = true
			}
			return err
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			require.True(t, lastRead, "the full lineage must be read before any deletion")
			return c.Delete(ctx, obj, opts...)
		},
	})
	g := &inventoryGC{reader: c.reader, downstream: c.downstream, owns: c.ownsComposition, logger: logr.Discard()}
	g.sweep(ctx)
	require.True(t, interrupted)
	require.False(t, lastRead)
	require.Empty(t, *deleted)
	gcTestAssertRetained(t, c.downstream, first, last)
	// Retention assertions use GET too; only the resumed validation should satisfy the delete guard.
	lastRead = false
	firstReads := reads[first.Name]

	g.sweep(ctx)
	require.True(t, lastRead)
	require.ElementsMatch(t, []string{first.Name, last.Name}, *deleted)
	require.Equal(t, firstReads, reads[first.Name], "resuming must not revalidate the completed prefix")
	require.Contains(t, listTokens["discovery"], "discovery-next")
	require.Contains(t, listTokens["lineage"], "lineage-next")
	for _, item := range []*corev1.ConfigMap{first, last} {
		require.True(t, apierrors.IsNotFound(c.downstream.Get(ctx, client.ObjectKeyFromObject(item), &corev1.ConfigMap{})))
	}
}

type gcTestRoundTripper func(*http.Request) (*http.Response, error)

func (fn gcTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestBackupGCTransportBudgets(t *testing.T) {
	for _, tt := range []struct {
		name, method string
		limit        int
	}{
		{name: "100 actual requests", method: http.MethodGet, limit: 100},
		{name: "20 DELETE attempts", method: http.MethodDelete, limit: 20},
	} {
		t.Run(tt.name, func(t *testing.T) {
			budget := &gcBudget{}
			ctx := context.WithValue(context.Background(), gcBudgetKey{}, budget)
			transportErr := errors.New("transport failed")
			var methods []string
			base := gcTestRoundTripper(func(req *http.Request) (*http.Response, error) {
				methods = append(methods, req.Method)
				switch len(methods) % 3 {
				case 1:
					return nil, transportErr
				case 2:
					return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader(""))}, nil
				default:
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
			})
			transports := []*gcTransport{{base: base}, {base: base}}
			for i := 0; i < tt.limit+2; i++ {
				req, err := http.NewRequestWithContext(ctx, tt.method, "http://gc.invalid/inventories", nil)
				require.NoError(t, err)
				resp, err := transports[i%len(transports)].RoundTrip(req)
				if i >= tt.limit {
					require.ErrorIs(t, err, errGCBudget)
					require.Nil(t, resp)
				} else if i%3 == 0 {
					require.ErrorIs(t, err, transportErr)
					require.Nil(t, resp)
				} else {
					require.NoError(t, err)
					require.NotNil(t, resp)
					if i%3 == 1 {
						require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
					} else {
						require.Equal(t, http.StatusOK, resp.StatusCode)
					}
					require.NoError(t, resp.Body.Close())
				}
			}
			require.Len(t, methods, tt.limit)
			for _, method := range methods {
				require.Equal(t, tt.method, method)
			}
			if tt.method == http.MethodDelete {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://gc.invalid/inventories", nil)
				require.NoError(t, err)
				resp, err := transports[0].RoundTrip(req)
				require.NoError(t, err, "exhausting DELETE attempts must not exhaust the GET allowance")
				require.NoError(t, resp.Body.Close())
				require.Len(t, methods, 21)
				require.Equal(t, http.MethodGet, methods[20])
			}
		})
	}
}

func TestBackupGCTransportCancellation(t *testing.T) {
	budget := &gcBudget{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), gcBudgetKey{}, budget))
	cancel()
	calls := 0
	transport := &gcTransport{base: gcTestRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected HTTP request")
	})}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, "http://gc.invalid/inventories", nil)
	require.NoError(t, err)
	resp, err := transport.RoundTrip(req)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, resp)
	require.Zero(t, calls)
}

func TestBackupGCStartCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := &inventoryGC{logger: logr.Discard()}
	require.True(t, g.NeedLeaderElection())
	require.NoError(t, g.Start(ctx))
}
