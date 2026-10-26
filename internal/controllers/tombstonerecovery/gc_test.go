package tombstonerecovery

import (
	"context"
	"fmt"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func gcTestComposition() *apiv1.Composition {
	comp := inventoryTestComposition()
	comp.Annotations["eno.azure.io/recovery-enabled"] = "true"
	comp.Status.CurrentSynthesis.UUID = controllerTestUUID(2)
	return comp
}

func gcTestSnapshot(t *testing.T, comp *apiv1.Composition, sequence int) corev1.Secret {
	t.Helper()
	comp = comp.DeepCopy()
	comp.Status.CurrentSynthesis.UUID = controllerTestUUID(sequence)
	items, err := makeInventory(comp, nil)
	require.NoError(t, err)
	require.Len(t, items, 1)
	items[0].UID = types.UID(fmt.Sprintf("snapshot-%d-uid", sequence))
	return items[0]
}

func gcTestNames(items []corev1.Secret) []string {
	names := []string{}
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

func gcTestOrphan(t *testing.T, comp *apiv1.Composition) corev1.Secret {
	t.Helper()
	orphan := gcTestSnapshot(t, comp, 3)
	orphan.Name = "orphan"
	orphan.Labels = map[string]string{inventoryLineageLabel: "other-lineage", inventorySynthesisUUIDLabel: controllerTestUUID(3)}
	return orphan
}

func TestFindStaleInventory(t *testing.T) {
	base := gcTestComposition()
	old, current, orphan := gcTestSnapshot(t, base, 1), gcTestSnapshot(t, base, 2), gcTestOrphan(t, base)

	for _, tc := range []struct {
		name    string
		change  func(*apiv1.Composition)
		secrets func() []corev1.Secret
		want    []string
	}{
		{name: "composition gone and old snapshot", want: []string{old.Name, orphan.Name}},
		{name: "current snapshot terminating", secrets: func() []corev1.Secret {
			terminating := *current.DeepCopy()
			terminating.DeletionTimestamp = ptr.To(metav1.Now())
			return []corev1.Secret{old, terminating, orphan}
		}, want: []string{orphan.Name}},
		{name: "current snapshot missing", secrets: func() []corev1.Secret {
			return []corev1.Secret{old, orphan}
		}, want: []string{orphan.Name}},
		{name: "current snapshot incomplete", secrets: func() []corev1.Secret {
			partial := *current.DeepCopy()
			partial.Annotations["eno.azure.io/inventory-chunk-count"] = "2"
			return []corev1.Secret{old, partial, orphan}
		}, want: []string{orphan.Name}},
		{name: "deleting composition", change: func(comp *apiv1.Composition) {
			comp.DeletionTimestamp = ptr.To(metav1.Now())
		}, want: []string{orphan.Name}},
		{name: "no current synthesis", change: func(comp *apiv1.Composition) {
			comp.Status.CurrentSynthesis = nil
		}, want: []string{orphan.Name}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			comp := base.DeepCopy()
			if tc.change != nil {
				tc.change(comp)
			}
			secrets := []corev1.Secret{old, current, orphan}
			if tc.secrets != nil {
				secrets = tc.secrets()
			}
			symph := &apiv1.Symphony{ObjectMeta: metav1.ObjectMeta{Namespace: comp.Namespace}}
			assert.Equal(t, tc.want, gcTestNames(findStaleInventory(secrets, []apiv1.Composition{*comp}, symph)))
		})
	}
}

func TestFindStaleInventoryKeepsSymphonyVariations(t *testing.T) {
	comp := gcTestComposition()
	current, orphan := gcTestSnapshot(t, comp, 2), gcTestOrphan(t, comp)
	secrets := []corev1.Secret{current, orphan}
	withVariation := &apiv1.Symphony{
		ObjectMeta: metav1.ObjectMeta{Namespace: comp.Namespace},
		Spec:       apiv1.SymphonySpec{Variations: []apiv1.Variation{{Synthesizer: comp.Spec.Synthesizer}}},
	}
	withoutVariation := &apiv1.Symphony{ObjectMeta: metav1.ObjectMeta{Namespace: comp.Namespace}}

	got := findStaleInventory(secrets, nil, withVariation)
	assert.Equal(t, []string{orphan.Name}, gcTestNames(got), "a variation whose Composition is being recreated keeps its inventory")

	got = findStaleInventory(secrets, nil, withoutVariation)
	assert.ElementsMatch(t, []string{current.Name, orphan.Name}, gcTestNames(got), "a removed variation's inventory is stale")
}

func TestIsSymphonyActive(t *testing.T) {
	assert.False(t, isSymphonyActive(nil), "missing")
	assert.True(t, isSymphonyActive([]apiv1.Symphony{{}}), "active")
	assert.False(t, isSymphonyActive([]apiv1.Symphony{{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: ptr.To(metav1.Now())}}}), "deleting")
}

func newGCTestCollector(t *testing.T, upstreamObjs []client.Object, secrets ...corev1.Secret) (*inventoryGarbageCollector, client.WithWatch) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.SchemeBuilder.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	downstream := fake.NewClientBuilder().WithScheme(scheme).Build()
	for i := range secrets {
		require.NoError(t, downstream.Create(t.Context(), &secrets[i]))
	}
	return &inventoryGarbageCollector{
		reader:     fake.NewClientBuilder().WithScheme(scheme).WithObjects(upstreamObjs...).Build(),
		downstream: downstream,
		namespace:  "control",
		logger:     logr.Discard(),
	}, downstream
}

func gcTestRemaining(t *testing.T, downstream client.Client) []string {
	t.Helper()
	list := &corev1.SecretList{}
	require.NoError(t, downstream.List(t.Context(), list, client.InNamespace(inventoryNamespace)))
	return gcTestNames(list.Items)
}

func TestInventoryGarbageCollectorDeleteStaleInventory(t *testing.T) {
	comp := gcTestComposition()
	old, current, orphan := gcTestSnapshot(t, comp, 1), gcTestSnapshot(t, comp, 2), gcTestOrphan(t, comp)
	unrelated := corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: inventoryNamespace}}

	for _, tc := range []struct {
		name     string
		symphony *apiv1.Symphony
		want     []string
	}{
		{name: "active symphony", symphony: &apiv1.Symphony{ObjectMeta: metav1.ObjectMeta{Name: "symphony", Namespace: "control"}},
			want: []string{current.Name, "unrelated"}},
		{name: "deleting symphony", symphony: &apiv1.Symphony{ObjectMeta: metav1.ObjectMeta{
			Name: "symphony", Namespace: "control", DeletionTimestamp: ptr.To(metav1.Now()), Finalizers: []string{"test"},
		}}, want: []string{old.Name, current.Name, orphan.Name, "unrelated"}},
		{name: "missing symphony", want: []string{old.Name, current.Name, orphan.Name, "unrelated"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs := []client.Object{comp}
			if tc.symphony != nil {
				objs = append(objs, tc.symphony)
			}
			g, downstream := newGCTestCollector(t, objs, old, current, orphan, unrelated)
			require.NoError(t, g.deleteStaleInventory(t.Context()))
			assert.ElementsMatch(t, tc.want, gcTestRemaining(t, downstream))
		})
	}
}

func TestInventoryGarbageCollectorDeleteFailureContinues(t *testing.T) {
	comp := gcTestComposition()
	current, first := gcTestSnapshot(t, comp, 2), gcTestOrphan(t, comp)
	second := gcTestOrphan(t, comp)
	second.Name, second.UID = "orphan-2", "orphan-2-uid"
	symphony := &apiv1.Symphony{ObjectMeta: metav1.ObjectMeta{Name: "symphony", Namespace: "control"}}

	g, downstream := newGCTestCollector(t, []client.Object{comp, symphony}, current, first, second)
	fail := true
	g.downstream = interceptor.NewClient(downstream, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if fail && obj.GetName() == first.Name {
				return fmt.Errorf("injected delete failure")
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	require.NoError(t, g.deleteStaleInventory(t.Context()))
	assert.ElementsMatch(t, []string{current.Name, first.Name}, gcTestRemaining(t, downstream), "other stale Secrets are still deleted")

	fail = false
	require.NoError(t, g.deleteStaleInventory(t.Context()))
	assert.ElementsMatch(t, []string{current.Name}, gcTestRemaining(t, downstream), "the next cycle retries the failed delete")
}

func TestInventoryGarbageCollectorReadFailureSkipsCycle(t *testing.T) {
	comp := gcTestComposition()
	orphan := gcTestOrphan(t, comp)
	symphony := &apiv1.Symphony{ObjectMeta: metav1.ObjectMeta{Name: "symphony", Namespace: "control"}}

	for _, kind := range []string{"Composition", "Symphony"} {
		t.Run(kind, func(t *testing.T) {
			g, downstream := newGCTestCollector(t, []client.Object{comp, symphony}, orphan)
			g.reader = interceptor.NewClient(g.reader.(client.WithWatch), interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, ok := list.(*apiv1.CompositionList); ok && kind == "Composition" {
						return fmt.Errorf("injected list failure")
					}
					if _, ok := list.(*apiv1.SymphonyList); ok && kind == "Symphony" {
						return fmt.Errorf("injected list failure")
					}
					return c.List(ctx, list, opts...)
				},
			})
			require.Error(t, g.deleteStaleInventory(t.Context()))
			assert.Equal(t, []string{orphan.Name}, gcTestRemaining(t, downstream))
		})
	}
}

func TestInventoryGarbageCollectorRunCycleRecoversPanic(t *testing.T) {
	g := &inventoryGarbageCollector{logger: logr.Discard()}
	assert.NotPanics(t, func() {
		g.runCycle(t.Context())
	})
	assert.True(t, g.NeedLeaderElection())
}
