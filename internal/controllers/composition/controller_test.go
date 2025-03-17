package composition

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestFinalizerBasics(t *testing.T) {
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)}

	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t, comp)
	c := &compositionController{client: cli}

	// Add finalizer
	_, err := c.Reconcile(ctx, req)
	require.NoError(t, err)

	_, err = c.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Len(t, comp.Finalizers, 1)

	// Remove finalizer
	require.NoError(t, cli.Delete(ctx, comp))

	_, err = c.Reconcile(ctx, req)
	require.NoError(t, err)

	_, err = c.Reconcile(ctx, req)
	require.NoError(t, err)

	require.True(t, errors.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
}

func TestFinalizerStillReconciling(t *testing.T) {
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{Reconciled: nil}
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)}

	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t, comp)
	c := &compositionController{client: cli}

	require.NoError(t, cli.Delete(ctx, comp))

	_, err := c.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Len(t, comp.Finalizers, 1)
}

func TestFinalizerSynthesisOutdated(t *testing.T) {
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{ObservedCompositionGeneration: -1}
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)}

	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t, comp)
	c := &compositionController{client: cli}

	require.NoError(t, cli.Delete(ctx, comp))

	_, err := c.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Len(t, comp.Finalizers, 1)
	assert.NotEmpty(t, comp.Status.CurrentSynthesis.UUID)
}

func TestTimeoutDeferral(t *testing.T) {
	synth := &apiv1.Synthesizer{}
	synth.Name = "test"
	synth.Spec.PodTimeout = &metav1.Duration{Duration: time.Hour}

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Spec.Synthesizer.Name = synth.Name
	comp.Status.InFlightSynthesis = &apiv1.Synthesis{Initialized: ptr.To(metav1.NewTime(time.Now().Add(-time.Minute)))}
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)}

	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t, comp, synth)
	c := &compositionController{client: cli}

	res, err := c.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.NotZero(t, res.RequeueAfter)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Nil(t, comp.Status.InFlightSynthesis.Canceled)
}

func TestTimeoutCancelation(t *testing.T) {
	synth := &apiv1.Synthesizer{}
	synth.Name = "test"
	synth.Spec.PodTimeout = &metav1.Duration{Duration: time.Minute}

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Spec.Synthesizer.Name = synth.Name
	comp.Status.InFlightSynthesis = &apiv1.Synthesis{Initialized: ptr.To(metav1.NewTime(time.Now().Add(-time.Hour)))}
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)}

	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t, comp, synth)
	c := &compositionController{client: cli}

	res, err := c.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.NotNil(t, comp.Status.InFlightSynthesis.Canceled)
}
