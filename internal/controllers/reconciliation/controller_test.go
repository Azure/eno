package reconciliation

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	"github.com/Azure/eno/internal/testutil"
	"github.com/Azure/eno/internal/testutil/statespace"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestRequeue(t *testing.T) {
	tests := []struct {
		name           string
		comp           *apiv1.Composition
		resource       *resource.Snapshot
		ready          *metav1.Time
		minReconcile   time.Duration
		expectedResult time.Duration
	}{
		{
			name: "resource is not ready, requeue after readiness poll interval",
			comp: &apiv1.Composition{},
			resource: &resource.Snapshot{
				ReconcileInterval: nil,
			},
			ready:          nil,
			minReconcile:   10 * time.Second,
			expectedResult: 10 * time.Second,
		},
		{
			name: "resource is deleted, no requeue",
			comp: &apiv1.Composition{},
			resource: &resource.Snapshot{
				ReconcileInterval: nil,
			},
			ready:          &metav1.Time{},
			minReconcile:   10 * time.Second,
			expectedResult: 0,
		},
		{
			name: "resource has reconcile interval less than minReconcileInterval",
			comp: &apiv1.Composition{},
			resource: &resource.Snapshot{
				ReconcileInterval: &metav1.Duration{Duration: 5 * time.Second},
			},
			ready:          &metav1.Time{},
			minReconcile:   10 * time.Second,
			expectedResult: 10 * time.Second,
		},
		{
			name: "resource has valid reconcile interval",
			comp: &apiv1.Composition{},
			resource: &resource.Snapshot{
				ReconcileInterval: &metav1.Duration{Duration: 15 * time.Second},
			},
			ready:          &metav1.Time{},
			minReconcile:   10 * time.Second,
			expectedResult: 15 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := logr.Discard()
			c := &Controller{
				readinessPollInterval: 10 * time.Second,
				minReconcileInterval:  tt.minReconcile,
			}
			if tt.resource.Resource == nil {
				tt.resource.Resource = &resource.Resource{}
			}

			result, err := c.requeue(logger, tt.comp, tt.resource, tt.ready)
			assert.NoError(t, err)
			assert.InDelta(t, tt.expectedResult, result.RequeueAfter, float64(2*time.Second))
		})
	}
}

func TestBuildNonStrategicPatch_NilPrevious(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	// Create
	actual := &corev1.ConfigMap{}
	actual.Name = "test-configmap"
	actual.Namespace = "default"
	actual.Data = map[string]string{"original": "value"}
	require.NoError(t, cli.Create(ctx, actual))

	// Patch
	expected := actual.DeepCopy()
	expected.Data = map[string]string{"added": "value"}
	patch, err := buildNonStrategicPatch(ctx, &apiv1.Composition{}, nil, nil)
	require.NoError(t, err)
	require.NoError(t, cli.Patch(ctx, expected, patch))

	// Verify
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(actual), actual))
	assert.Equal(t, map[string]string{
		"added":    "value",
		"original": "value",
	}, actual.Data)
}

func TestShouldFailOpen(t *testing.T) {
	c := &Controller{failOpen: true}
	assert.True(t, c.shouldFailOpen(&resource.Resource{FailOpen: nil}))
	assert.False(t, c.shouldFailOpen(&resource.Resource{FailOpen: ptr.To(false)}))
	assert.True(t, c.shouldFailOpen(&resource.Resource{FailOpen: ptr.To(true)}))

	c.failOpen = false
	assert.False(t, c.shouldFailOpen(&resource.Resource{FailOpen: nil}))
	assert.False(t, c.shouldFailOpen(&resource.Resource{FailOpen: ptr.To(false)}))
	assert.True(t, c.shouldFailOpen(&resource.Resource{FailOpen: ptr.To(true)}))
}

func TestRequeueDoesNotPanic(t *testing.T) {
	type testState struct {
		snapshot *resource.Snapshot
		ready    *metav1.Time
	}

	statespace.Test(func(state *testState) bool {
		c := &Controller{}
		comp := &apiv1.Composition{}
		_, err := c.requeue(logr.Discard(), comp, state.snapshot, state.ready)
		require.NoError(t, err)
		return true
	}).
		WithInitialState(func() *testState {
			return &testState{
				snapshot: &resource.Snapshot{
					Resource:          &resource.Resource{},
					ReconcileInterval: &metav1.Duration{Duration: 15 * time.Second},
				},
				ready: &metav1.Time{Time: time.Now()},
			}
		}).
		WithMutation("nil snapshot", func(state *testState) *testState {
			state.snapshot = nil
			return state
		}).
		WithMutation("nil ready", func(state *testState) *testState {
			state.ready = nil
			return state
		}).
		WithMutation("nil reconcile interval", func(state *testState) *testState {
			if state.snapshot != nil {
				state.snapshot.ReconcileInterval = nil
			}
			return state
		}).
		WithMutation("short reconcile interval", func(state *testState) *testState {
			if state.snapshot != nil {
				state.snapshot.ReconcileInterval = &metav1.Duration{Duration: 1 * time.Second}
			}
			return state
		}).
		WithMutation("disable", func(state *testState) *testState {
			if state.snapshot != nil {
				state.snapshot.Disable = true
			}
			return state
		}).
		WithMutation("foreground deletion", func(state *testState) *testState {
			if state.snapshot != nil {
				state.snapshot.ForegroundDeletion = true
			}
			return state
		}).
		WithMutation("orphan", func(state *testState) *testState {
			if state.snapshot != nil {
				state.snapshot.Orphan = true
			}
			return state
		}).
		WithMutation("disable updates", func(state *testState) *testState {
			if state.snapshot != nil {
				state.snapshot.DisableUpdates = true
			}
			return state
		}).
		WithMutation("replace", func(state *testState) *testState {
			if state.snapshot != nil {
				state.snapshot.Replace = true
			}
			return state
		}).
		WithInvariant("doesn't panic", func(state *testState, res bool) bool {
			return res
		}).
		Evaluate(t)
}
