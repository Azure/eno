package reconciliation

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	"github.com/Azure/eno/internal/testutil"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestRequeue(t *testing.T) {
	tests := []struct {
		name           string
		comp           *apiv1.Composition
		resource       *resource.Resource
		ready          *metav1.Time
		minReconcile   time.Duration
		expectedResult time.Duration
	}{
		{
			name: "resource is not ready, requeue after readiness poll interval",
			comp: &apiv1.Composition{},
			resource: &resource.Resource{
				ReconcileInterval: nil,
			},
			ready:          nil,
			minReconcile:   10 * time.Second,
			expectedResult: 10 * time.Second,
		},
		{
			name: "resource is deleted, no requeue",
			comp: &apiv1.Composition{},
			resource: &resource.Resource{
				ReconcileInterval: nil,
			},
			ready:          &metav1.Time{},
			minReconcile:   10 * time.Second,
			expectedResult: 0,
		},
		{
			name: "resource has reconcile interval less than minReconcileInterval",
			comp: &apiv1.Composition{},
			resource: &resource.Resource{
				ReconcileInterval: &metav1.Duration{Duration: 5 * time.Second},
			},
			ready:          &metav1.Time{},
			minReconcile:   10 * time.Second,
			expectedResult: 10 * time.Second,
		},
		{
			name: "resource has valid reconcile interval",
			comp: &apiv1.Composition{},
			resource: &resource.Resource{
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
	patch := buildNonStrategicPatch(nil)
	require.NoError(t, cli.Patch(ctx, expected, patch))

	// Verify
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(actual), actual))
	assert.Equal(t, map[string]string{
		"added":    "value",
		"original": "value",
	}, actual.Data)
}
