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
		WithMutation("with dependencies", func(state *simplifiedStatusState) *simplifiedStatusState {
			state.Comp.Spec.DependsOn = []apiv1.CompositionDependency{{Name: "dep-a", Namespace: "default"}}
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
		WithInvariant("waiting on dependencies", func(state *simplifiedStatusState, result *apiv1.SimplifiedStatus) bool {
			hasDeps := len(state.Comp.Spec.DependsOn) > 0
			noSynthesis := state.Comp.Status.CurrentSynthesis == nil && state.Comp.Status.InFlightSynthesis == nil
			notDeleting := state.Comp.DeletionTimestamp == nil
			hasSynth := state.Synth != nil
			return !(hasDeps && noSynthesis && notDeleting && hasSynth) || result.Status == apiv1.WaitingOnDependenciesReason
		}).
		Evaluate(t)
}

type simplifiedStatusState struct {
	Synth *apiv1.Synthesizer
	Comp  *apiv1.Composition
}

func TestShouldForceRemoveFinalizer(t *testing.T) {
	const symphonyName = "my-symphony"
	const namespace = "default"

	newComp := func(annotations map[string]string, withOwnerRef bool) *apiv1.Composition {
		comp := &apiv1.Composition{}
		comp.Name = "comp-1"
		comp.Namespace = namespace
		comp.Annotations = annotations
		comp.Finalizers = []string{"eno.azure.io/cleanup"}
		if withOwnerRef {
			comp.OwnerReferences = []metav1.OwnerReference{{
				APIVersion: apiv1.SchemeGroupVersion.String(),
				Kind:       "Symphony",
				Name:       symphonyName,
				UID:        "test-uid",
			}}
		}
		return comp
	}

	tests := []struct {
		name           string
		annotations    map[string]string
		withOwnerRef   bool
		symphonyExists bool
		expected       bool
	}{
		{
			name:         "no annotations",
			annotations:  nil,
			withOwnerRef: true,
			expected:     false,
		},
		{
			name:         "annotation set to false",
			annotations:  map[string]string{enoCompositionForceDeleteAnnotation: "false"},
			withOwnerRef: true,
			expected:     false,
		},
		{
			name:         "annotation set to true - symphony gone",
			annotations:  map[string]string{enoCompositionForceDeleteAnnotation: "true"},
			withOwnerRef: true,
			expected:     true,
		},
		{
			name:           "annotation set to true - symphony exists",
			annotations:    map[string]string{enoCompositionForceDeleteAnnotation: "true"},
			withOwnerRef:   true,
			symphonyExists: true,
			expected:       false,
		},
		{
			name:         "annotation set to true - no owner ref",
			annotations:  map[string]string{enoCompositionForceDeleteAnnotation: "true"},
			withOwnerRef: false,
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comp := newComp(tt.annotations, tt.withOwnerRef)
			objs := []client.Object{comp}
			if tt.symphonyExists {
				symph := &apiv1.Symphony{}
				symph.Name = symphonyName
				symph.Namespace = namespace
				objs = append(objs, symph)
			}

			ctx := testutil.NewContext(t)
			cli := testutil.NewClient(t, objs...)
			c := &compositionController{client: cli}

			result := c.shouldForceRemoveFinalizer(ctx, comp)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHasActiveDependents(t *testing.T) {
	const namespace = "default"

	t.Run("no dependents", func(t *testing.T) {
		comp := &apiv1.Composition{}
		comp.Name = "parent"
		comp.Namespace = namespace

		ctx := testutil.NewContext(t)
		cli := testutil.NewClient(t, comp)
		c := &compositionController{client: cli}

		blocked, blockedBy, err := c.hasActiveDependents(ctx, comp)
		require.NoError(t, err)
		assert.False(t, blocked)
		assert.Empty(t, blockedBy)
	})

	t.Run("one active dependent", func(t *testing.T) {
		comp := &apiv1.Composition{}
		comp.Name = "parent"
		comp.Namespace = namespace

		dep := &apiv1.Composition{}
		dep.Name = "child"
		dep.Namespace = namespace
		dep.Finalizers = []string{EnoCleanupFinalizer}
		dep.Spec.DependsOn = []apiv1.CompositionDependency{{Name: "parent", Namespace: namespace}}

		ctx := testutil.NewContext(t)
		cli := testutil.NewClient(t, comp, dep)
		c := &compositionController{client: cli}

		blocked, blockedBy, err := c.hasActiveDependents(ctx, comp)
		require.NoError(t, err)
		assert.True(t, blocked)
		require.Len(t, blockedBy, 1)
		assert.Equal(t, "child", blockedBy[0].Name)
		assert.Equal(t, namespace, blockedBy[0].Namespace)
		assert.Equal(t, apiv1.WaitingOnDependentsDeletedReason, blockedBy[0].Reason)
	})

	t.Run("dependent being deleted with finalizer still blocks", func(t *testing.T) {
		comp := &apiv1.Composition{}
		comp.Name = "parent"
		comp.Namespace = namespace

		dep := &apiv1.Composition{}
		dep.Name = "child"
		dep.Namespace = namespace
		dep.Finalizers = []string{EnoCleanupFinalizer}
		dep.Spec.DependsOn = []apiv1.CompositionDependency{{Name: "parent", Namespace: namespace}}

		ctx := testutil.NewContext(t)
		cli := testutil.NewClient(t, comp, dep)
		// Simulate deletion by deleting the dependent (fake client sets DeletionTimestamp when finalizers exist)
		require.NoError(t, cli.Delete(ctx, dep))
		c := &compositionController{client: cli}

		blocked, blockedBy, err := c.hasActiveDependents(ctx, comp)
		require.NoError(t, err)
		assert.True(t, blocked)
		require.Len(t, blockedBy, 1)
		assert.Equal(t, "child", blockedBy[0].Name)
	})

	t.Run("dependent being deleted without finalizer does not block", func(t *testing.T) {
		comp := &apiv1.Composition{}
		comp.Name = "parent"
		comp.Namespace = namespace

		dep := &apiv1.Composition{}
		dep.Name = "child"
		dep.Namespace = namespace
		// No finalizer - dependent will be fully deleted
		dep.Spec.DependsOn = []apiv1.CompositionDependency{{Name: "parent", Namespace: namespace}}

		ctx := testutil.NewContext(t)
		cli := testutil.NewClient(t, comp, dep)
		// Delete the dependent - without finalizers it gets removed immediately in fake client
		require.NoError(t, cli.Delete(ctx, dep))
		c := &compositionController{client: cli}

		blocked, blockedBy, err := c.hasActiveDependents(ctx, comp)
		require.NoError(t, err)
		assert.False(t, blocked)
		assert.Empty(t, blockedBy)
	})

	t.Run("multiple dependents mixed states", func(t *testing.T) {
		comp := &apiv1.Composition{}
		comp.Name = "parent"
		comp.Namespace = namespace

		activeDep := &apiv1.Composition{}
		activeDep.Name = "active-child"
		activeDep.Namespace = namespace
		activeDep.Finalizers = []string{EnoCleanupFinalizer}
		activeDep.Spec.DependsOn = []apiv1.CompositionDependency{{Name: "parent", Namespace: namespace}}

		anotherActiveDep := &apiv1.Composition{}
		anotherActiveDep.Name = "another-active-child"
		anotherActiveDep.Namespace = namespace
		anotherActiveDep.Finalizers = []string{EnoCleanupFinalizer}
		anotherActiveDep.Spec.DependsOn = []apiv1.CompositionDependency{{Name: "parent", Namespace: namespace}}

		// This dependent has no finalizer - will be fully deleted
		deletableDep := &apiv1.Composition{}
		deletableDep.Name = "deletable-child"
		deletableDep.Namespace = namespace
		deletableDep.Spec.DependsOn = []apiv1.CompositionDependency{{Name: "parent", Namespace: namespace}}

		ctx := testutil.NewContext(t)
		cli := testutil.NewClient(t, comp, activeDep, anotherActiveDep, deletableDep)
		// Delete the one without a finalizer
		require.NoError(t, cli.Delete(ctx, deletableDep))
		c := &compositionController{client: cli}

		blocked, blockedBy, err := c.hasActiveDependents(ctx, comp)
		require.NoError(t, err)
		assert.True(t, blocked)
		assert.Len(t, blockedBy, 2)

		names := []string{blockedBy[0].Name, blockedBy[1].Name}
		assert.Contains(t, names, "active-child")
		assert.Contains(t, names, "another-active-child")
	})

	t.Run("unrelated composition does not count as dependent", func(t *testing.T) {
		comp := &apiv1.Composition{}
		comp.Name = "parent"
		comp.Namespace = namespace

		unrelated := &apiv1.Composition{}
		unrelated.Name = "unrelated"
		unrelated.Namespace = namespace
		unrelated.Finalizers = []string{EnoCleanupFinalizer}
		// Depends on a different composition
		unrelated.Spec.DependsOn = []apiv1.CompositionDependency{{Name: "other-parent", Namespace: namespace}}

		ctx := testutil.NewContext(t)
		cli := testutil.NewClient(t, comp, unrelated)
		c := &compositionController{client: cli}

		blocked, blockedBy, err := c.hasActiveDependents(ctx, comp)
		require.NoError(t, err)
		assert.False(t, blocked)
		assert.Empty(t, blockedBy)
	})

	t.Run("dependent without finalizer and not deleted is active", func(t *testing.T) {
		comp := &apiv1.Composition{}
		comp.Name = "parent"
		comp.Namespace = namespace

		dep := &apiv1.Composition{}
		dep.Name = "child-no-finalizer"
		dep.Namespace = namespace
		// No finalizer, not deleted - still counts as active
		dep.Spec.DependsOn = []apiv1.CompositionDependency{{Name: "parent", Namespace: namespace}}

		ctx := testutil.NewContext(t)
		cli := testutil.NewClient(t, comp, dep)
		c := &compositionController{client: cli}

		blocked, blockedBy, err := c.hasActiveDependents(ctx, comp)
		require.NoError(t, err)
		assert.True(t, blocked)
		require.Len(t, blockedBy, 1)
		assert.Equal(t, "child-no-finalizer", blockedBy[0].Name)
	})
}
