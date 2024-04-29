package reconciliation

import (
	"fmt"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/aggregation"
	testv1 "github.com/Azure/eno/internal/controllers/reconciliation/fixtures/v1"
	"github.com/Azure/eno/internal/controllers/rollout"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBulkRollout(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	// Register supporting controllers
	require.NoError(t, rollout.NewController(mgr.Manager, time.Millisecond))
	require.NoError(t, synthesis.NewStatusController(mgr.Manager))
	require.NoError(t, aggregation.NewSliceController(mgr.Manager))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, defaultConf))
	require.NoError(t, synthesis.NewExecController(mgr.Manager, defaultConf, &testutil.ExecConn{Hook: func(s *apiv1.Synthesizer) []client.Object {
		obj := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-obj",
				Namespace: "default",
			},
			Data: map[string]string{"image": s.Spec.Image},
		}

		gvks, _, err := scheme.ObjectKinds(obj)
		require.NoError(t, err)
		obj.GetObjectKind().SetGroupVersionKind(gvks[0])
		return []client.Object{obj}
	}}))

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "create"
	require.NoError(t, upstream.Create(ctx, syn))

	// Create a bunch of compositions
	const n = 25
	for i := 0; i < n; i++ {
		comp := &apiv1.Composition{}
		comp.Name = fmt.Sprintf("test-comp-%d", i)
		comp.Namespace = "default"
		comp.Spec.Synthesizer.Name = syn.Name
		require.NoError(t, upstream.Create(ctx, comp))
	}

	testutil.Eventually(t, func() bool {
		for i := 0; i < n; i++ {
			comp := &apiv1.Composition{}
			comp.Name = fmt.Sprintf("test-comp-%d", i)
			comp.Namespace = "default"
			err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
			inSync := err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
			if !inSync {
				return false
			}
		}
		return true
	})

	// Update the synthesizer, prove all compositions converge
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		syn.Spec.Image = "updated"
		return upstream.Update(ctx, syn)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		outOfSync := []string{}
		for i := 0; i < n; i++ {
			comp := &apiv1.Composition{}
			comp.Name = fmt.Sprintf("test-comp-%d", i)
			comp.Namespace = "default"
			err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
			inSync := err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
			if comp.Status.CurrentSynthesis != nil {
				if !inSync {
					outOfSync = append(outOfSync, fmt.Sprintf("composition %s with synthesized=%t reconciled=%t syngen=%d", comp.Name, comp.Status.CurrentSynthesis.Synthesized != nil, comp.Status.CurrentSynthesis.Reconciled != nil, comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration))
				}
			}
		}
		// t.Logf("out of sync compositions: %+s", outOfSync)
		return len(outOfSync) == 0
	})
}
