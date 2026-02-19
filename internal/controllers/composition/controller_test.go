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

func TestIsAddonComposition(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		expected bool
	}{
		{
			name:     "nil labels",
			labels:   nil,
			expected: false,
		},
		{
			name:     "empty labels",
			labels:   map[string]string{},
			expected: false,
		},
		{
			name:     "unrelated labels",
			labels:   map[string]string{"foo": "bar"},
			expected: false,
		},
		{
			name:     "AKS component label with wrong value",
			labels:   map[string]string{AKSComponentLabel: "not-addon"},
			expected: false,
		},
		{
			name:     "AKS component label with addon value",
			labels:   map[string]string{AKSComponentLabel: addOnLabelValue},
			expected: true,
		},

	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comp := &apiv1.Composition{}
			comp.Labels = tt.labels
			assert.Equal(t, tt.expected, isAddonComposition(comp))
		})
	}
}

func TestShouldForceRemoveFinalizer(t *testing.T) {
	const symphonyName = "my-symphony"
	const namespace = "default"

	addonLabels := map[string]string{AKSComponentLabel: addOnLabelValue}

	newComp := func(annotations map[string]string, labels map[string]string, withOwnerRef bool) *apiv1.Composition {
		comp := &apiv1.Composition{}
		comp.Name = "comp-1"
		comp.Namespace = namespace
		comp.Annotations = annotations
		comp.Labels = labels
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
		labels         map[string]string
		withOwnerRef   bool
		symphonyExists bool
		expected       bool
	}{
		{
			name:         "no annotations and no addon label",
			annotations:  nil,
			labels:       nil,
			withOwnerRef: true,
			expected:     false,
		},
		{
			name:         "no annotations and wrong addon label value",
			annotations:  nil,
			labels:       map[string]string{AKSComponentLabel: "not-addon"},
			withOwnerRef: true,
			expected:     false,
		},
		{
			name:         "annotation set to false and no addon label",
			annotations:  map[string]string{enoCompositionForceDeleteAnnotation: "false"},
			labels:       nil,
			withOwnerRef: true,
			expected:     false,
		},
		{
			name:         "no annotation - addon label only - symphony gone",
			annotations:  nil,
			labels:       addonLabels,
			withOwnerRef: true,
			expected:     true,
		},
		{
			name:           "no annotation - addon label only - symphony exists",
			annotations:    nil,
			labels:         addonLabels,
			withOwnerRef:   true,
			symphonyExists: true,
			expected:       false,
		},
		{
			name:         "annotation set to true - no addon label - symphony gone",
			annotations:  map[string]string{enoCompositionForceDeleteAnnotation: "true"},
			labels:       nil,
			withOwnerRef: true,
			expected:     true,
		},
		{
			name:         "annotation set to true - addon label - symphony gone",
			annotations:  map[string]string{enoCompositionForceDeleteAnnotation: "true"},
			labels:       addonLabels,
			withOwnerRef: true,
			expected:     true,
		},
		{
			name:           "annotation set to true - addon label - symphony exists",
			annotations:    map[string]string{enoCompositionForceDeleteAnnotation: "true"},
			labels:         addonLabels,
			withOwnerRef:   true,
			symphonyExists: true,
			expected:       false,
		},
		{
			name:         "annotation set to true - addon label - no owner ref",
			annotations:  map[string]string{enoCompositionForceDeleteAnnotation: "true"},
			labels:       addonLabels,
			withOwnerRef: false,
			expected:     false,
		},
		{
			name:         "addon label only - no owner ref",
			annotations:  nil,
			labels:       addonLabels,
			withOwnerRef: false,
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comp := newComp(tt.annotations, tt.labels, tt.withOwnerRef)
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
