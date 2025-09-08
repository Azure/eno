package reconciliation

import (
	"errors"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	"github.com/Azure/eno/internal/testutil"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

func TestSummarizeError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"non-api", errors.New("test"), "test"},
		{"non-api-64", errors.New("this error message has exactly sixty four chars for edge testing"), "this error message has exactly sixty four chars for edge testing"},
		{"non-api-long", errors.New("very long error message that exceeds the sixty four character limit for messages"), "very long error message that exceeds the sixty four character li"},
		{"conflict", k8serrors.NewConflict(schema.GroupResource{Group: "", Resource: "test"}, "", nil), ""},
		{"bad-request", &k8serrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonBadRequest, Message: "bad"}}, "bad"},
		{"not-acceptable", &k8serrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonNotAcceptable, Message: "not ok"}}, "not ok"},
		{"too-large", &k8serrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonRequestEntityTooLarge, Message: "big"}}, "big"},
		{"method-not-allowed", &k8serrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonMethodNotAllowed, Message: "no"}}, "no"},
		{"too-many-requests", &k8serrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonTooManyRequests, Message: "slow"}}, "slow"},
		{"gone", &k8serrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonGone, Message: "bye"}}, "bye"},
		{"not-found", &k8serrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonNotFound, Message: "missing"}}, "missing"},
		{"forbidden", &k8serrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonForbidden, Message: "nope"}}, "nope"},
		{"unauthorized", &k8serrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonUnauthorized, Message: "denied"}}, "denied"},
		{"server-error", &k8serrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonInternalError, Message: "internal"}}, "apiserver error, see eno-reconciler logs for details"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, summarizeError(tt.err))
		})
	}
}
