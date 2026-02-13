package composition

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/Azure/eno/internal/testutil/statespace"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
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

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Spec.Synthesizer.Name = synth.Name
	comp.Status.InFlightSynthesis = &apiv1.Synthesis{Initialized: ptr.To(metav1.NewTime(time.Now().Add(-time.Minute)))}
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)}

	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t, comp, synth)
	c := &compositionController{client: cli, podTimeout: time.Hour}

	res, err := c.Reconcile(ctx, req) // status update
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)

	res, err = c.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.NotZero(t, res.RequeueAfter)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Nil(t, comp.Status.InFlightSynthesis.Canceled)
}

func TestTimeoutCancelation(t *testing.T) {
	synth := &apiv1.Synthesizer{}
	synth.Name = "test"

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Spec.Synthesizer.Name = synth.Name
	comp.Status.InFlightSynthesis = &apiv1.Synthesis{Initialized: ptr.To(metav1.NewTime(time.Now().Add(-time.Hour)))}
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)}

	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t, comp, synth)
	c := &compositionController{client: cli, podTimeout: time.Minute}

	res, err := c.Reconcile(ctx, req) // status update
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)

	res, err = c.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.NotNil(t, comp.Status.InFlightSynthesis.Canceled)

	res, err = c.Reconcile(ctx, req) // status update
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)

	// Idempotence check
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	c.client = testutil.NewReadOnlyClient(t, comp, synth)
	res, err = c.Reconcile(ctx, req)
	assert.NoError(t, err)
}

func TestSimplifiedStatus(t *testing.T) {
	statespace.Test(func(state *simplifiedStatusState) *apiv1.SimplifiedStatus {
		return buildSimplifiedStatus(state.Synth, state.Comp)
	}).
		WithInitialState(func() *simplifiedStatusState {
			return &simplifiedStatusState{
				Synth: &apiv1.Synthesizer{},
				Comp:  &apiv1.Composition{},
			}
		}).
		WithMutation("nil synth", func(state *simplifiedStatusState) *simplifiedStatusState {
			state.Synth = nil
			return state
		}).
		WithMutation("deleting composition", func(state *simplifiedStatusState) *simplifiedStatusState {
			state.Comp.DeletionTimestamp = &metav1.Time{}
			return state
		}).
		WithMutation("in-flight synthesis", func(state *simplifiedStatusState) *simplifiedStatusState {
			state.Comp.Status.InFlightSynthesis = &apiv1.Synthesis{}
			return state
		}).
		WithMutation("canceled in-flight synthesis", func(state *simplifiedStatusState) *simplifiedStatusState {
			if state.Comp.Status.InFlightSynthesis != nil {
				state.Comp.Status.InFlightSynthesis.Canceled = ptr.To(metav1.Now())
			}
			return state
		}).
		WithMutation("non-nil current synthesis", func(state *simplifiedStatusState) *simplifiedStatusState {
			state.Comp.Status.CurrentSynthesis = &apiv1.Synthesis{}
			return state
		}).
		WithMutation("reconciled current synthesis", func(state *simplifiedStatusState) *simplifiedStatusState {
			if state.Comp.Status.CurrentSynthesis != nil {
				state.Comp.Status.CurrentSynthesis.Reconciled = ptr.To(metav1.Now())
			}
			return state
		}).
		WithMutation("ready current synthesis", func(state *simplifiedStatusState) *simplifiedStatusState {
			if state.Comp.Status.CurrentSynthesis != nil {
				state.Comp.Status.CurrentSynthesis.Ready = ptr.To(metav1.Now())
			}
			return state
		}).
		WithMutation("with error message", func(state *simplifiedStatusState) *simplifiedStatusState {
			if state.Comp.Status.InFlightSynthesis != nil {
				state.Comp.Status.InFlightSynthesis.Results = []apiv1.Result{
					{Severity: krmv1.ResultSeverityError, Message: "Test error"},
				}
			}
			return state
		}).
		WithMutation("with simplified status error", func(state *simplifiedStatusState) *simplifiedStatusState {
			state.Comp.Status.Simplified = &apiv1.SimplifiedStatus{Error: "Previous reconciliation error"}
			return state
		}).
		WithInvariant("missing synth", func(state *simplifiedStatusState, result *apiv1.SimplifiedStatus) bool {
			return state.Comp.DeletionTimestamp != nil || state.Synth != nil || result.Status == "MissingSynthesizer"
		}).
		WithInvariant("deleting composition", func(state *simplifiedStatusState, result *apiv1.SimplifiedStatus) bool {
			return state.Comp.DeletionTimestamp == nil || result.Status == "Deleting"
		}).
		WithInvariant("in flight", func(state *simplifiedStatusState, result *apiv1.SimplifiedStatus) bool {
			return state.Comp.DeletionTimestamp != nil ||
				state.Synth == nil ||
				state.Comp.Status.InFlightSynthesis == nil ||
				state.Comp.Status.InFlightSynthesis.Canceled != nil ||
				result.Status == "Synthesizing"
		}).
		WithInvariant("synthesis canceled", func(state *simplifiedStatusState, result *apiv1.SimplifiedStatus) bool {
			return state.Comp.DeletionTimestamp != nil ||
				state.Synth == nil ||
				state.Comp.Status.InFlightSynthesis == nil ||
				state.Comp.Status.InFlightSynthesis.Canceled == nil ||
				result.Status == "SynthesisBackoff"
		}).
		WithInvariant("synthesis canceled no message", func(state *simplifiedStatusState, result *apiv1.SimplifiedStatus) bool {
			return result.Status != "SynthesisBackoff" ||
				state.Comp.Status.InFlightSynthesis == nil ||
				len(state.Comp.Status.InFlightSynthesis.Results) > 0 ||
				result.Error == "Timeout"
		}).
		WithInvariant("synthesis error", func(state *simplifiedStatusState, result *apiv1.SimplifiedStatus) bool {
			return state.Comp.DeletionTimestamp != nil ||
				state.Synth == nil ||
				state.Comp.Status.InFlightSynthesis == nil ||
				len(state.Comp.Status.InFlightSynthesis.Results) == 0 ||
				result.Error == "Test error"
		}).
		WithInvariant("ready", func(state *simplifiedStatusState, result *apiv1.SimplifiedStatus) bool {
			return state.Comp.DeletionTimestamp != nil ||
				state.Synth == nil ||
				state.Comp.Status.InFlightSynthesis != nil ||
				state.Comp.Status.CurrentSynthesis == nil ||
				state.Comp.Status.CurrentSynthesis.Ready == nil ||
				result.Status == "Ready"
		}).
		WithInvariant("reconciling preserves error", func(state *simplifiedStatusState, result *apiv1.SimplifiedStatus) bool {
			return result.Status != "Reconciling" ||
				state.Comp.Status.Simplified == nil ||
				state.Comp.Status.Simplified.Error == "" ||
				result.Error == state.Comp.Status.Simplified.Error
		}).
		Evaluate(t)
}

type simplifiedStatusState struct {
	Synth *apiv1.Synthesizer
	Comp  *apiv1.Composition
}

// TestResolvedSynthNameInSimplifiedStatus proves that buildSimplifiedStatus correctly populates
// ResolvedSynthName from the synthesizer, and leaves it empty when the synthesizer is nil.
func TestResolvedSynthNameInSimplifiedStatus(t *testing.T) {
	t.Run("non-nil synth populates ResolvedSynthName", func(t *testing.T) {
		synth := &apiv1.Synthesizer{}
		synth.Name = "my-synth"
		comp := &apiv1.Composition{}

		status := buildSimplifiedStatus(synth, comp)
		assert.Equal(t, "my-synth", status.ResolvedSynthName)
	})

	t.Run("nil synth leaves ResolvedSynthName empty", func(t *testing.T) {
		comp := &apiv1.Composition{}

		status := buildSimplifiedStatus(nil, comp)
		assert.Equal(t, "", status.ResolvedSynthName)
		assert.Equal(t, "MissingSynthesizer", status.Status)
	})

	t.Run("deleting composition with nil synth leaves ResolvedSynthName empty", func(t *testing.T) {
		comp := &apiv1.Composition{}
		comp.DeletionTimestamp = &metav1.Time{}

		status := buildSimplifiedStatus(nil, comp)
		assert.Equal(t, "", status.ResolvedSynthName)
		assert.Equal(t, "Deleting", status.Status)
	})
}

// TestLabelSelectorResolution proves that the composition controller correctly resolves
// a synthesizer via label selector and populates the simplified status.
func TestLabelSelectorResolution(t *testing.T) {
	synth := &apiv1.Synthesizer{}
	synth.Name = "label-synth"
	synth.Labels = map[string]string{"team": "platform"}

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.LabelSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{"team": "platform"},
	}
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)}

	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t, comp, synth)
	c := &compositionController{client: cli}

	// First reconcile adds finalizer
	_, err := c.Reconcile(ctx, req)
	require.NoError(t, err)

	// Second reconcile resolves synthesizer and updates status
	_, err = c.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	require.NotNil(t, comp.Status.Simplified)
	assert.Equal(t, "label-synth", comp.Status.Simplified.ResolvedSynthName)
}

// TestLabelSelectorNoMatch proves that the composition controller returns an error
// when no synthesizer matches the label selector (since ErrNoMatchingSelector is not a NotFound error).
func TestLabelSelectorNoMatch(t *testing.T) {
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Spec.Synthesizer.LabelSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{"team": "nonexistent"},
	}
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)}

	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t, comp)
	c := &compositionController{client: cli}

	_, err := c.Reconcile(ctx, req)
	require.Error(t, err)
	assert.ErrorIs(t, err, apiv1.ErrNoMatchingSelector)
}

// TestLabelSelectorMultipleMatches proves that the composition controller returns an error
// when multiple synthesizers match the label selector.
func TestLabelSelectorMultipleMatches(t *testing.T) {
	synth1 := &apiv1.Synthesizer{}
	synth1.Name = "synth-1"
	synth1.Labels = map[string]string{"team": "platform"}

	synth2 := &apiv1.Synthesizer{}
	synth2.Name = "synth-2"
	synth2.Labels = map[string]string{"team": "platform"}

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Spec.Synthesizer.LabelSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{"team": "platform"},
	}
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)}

	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t, comp, synth1, synth2)
	c := &compositionController{client: cli}

	_, err := c.Reconcile(ctx, req)
	require.Error(t, err)
	assert.ErrorIs(t, err, apiv1.ErrMultipleMatches)
}

// TestLabelSelectorPrecedence proves that when both name and labelSelector are set,
// labelSelector takes precedence.
func TestLabelSelectorPrecedence(t *testing.T) {
	nameSynth := &apiv1.Synthesizer{}
	nameSynth.Name = "name-synth"
	nameSynth.Labels = map[string]string{"team": "other"}

	labelSynth := &apiv1.Synthesizer{}
	labelSynth.Name = "label-synth"
	labelSynth.Labels = map[string]string{"team": "platform"}

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = "name-synth"
	comp.Spec.Synthesizer.LabelSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{"team": "platform"},
	}
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(comp)}

	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t, comp, nameSynth, labelSynth)
	c := &compositionController{client: cli}

	// Add finalizer
	_, err := c.Reconcile(ctx, req)
	require.NoError(t, err)

	// Resolve and update status
	_, err = c.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	require.NotNil(t, comp.Status.Simplified)
	// Should resolve to the label-matched synth, not the name-matched synth
	assert.Equal(t, "label-synth", comp.Status.Simplified.ResolvedSynthName)
}
