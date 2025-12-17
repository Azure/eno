package overlaysync

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestInputMirrorName(t *testing.T) {
	tests := []struct {
		symphonyName string
		key          string
		expected     string
	}{
		{"symphony-123", "metricsSettings", "symphony-123-metricsSettings"},
		{"my-symphony", "config", "my-symphony-config"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := inputMirrorName(tt.symphonyName, tt.key)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSetSyncedCondition(t *testing.T) {
	mirror := &apiv1.InputMirror{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-mirror",
			Namespace:  "test-ns",
			Generation: 1,
		},
	}

	// Test setting synced=true
	setSyncedCondition(mirror, true, "SyncSuccess", "Successfully synced")

	require.Len(t, mirror.Status.Conditions, 1)
	assert.Equal(t, ConditionTypeSynced, mirror.Status.Conditions[0].Type)
	assert.Equal(t, metav1.ConditionTrue, mirror.Status.Conditions[0].Status)
	assert.Equal(t, "SyncSuccess", mirror.Status.Conditions[0].Reason)
	assert.Equal(t, "Successfully synced", mirror.Status.Conditions[0].Message)

	// Test updating to synced=false
	setSyncedCondition(mirror, false, "SyncFailed", "Failed to sync")

	require.Len(t, mirror.Status.Conditions, 1)
	assert.Equal(t, metav1.ConditionFalse, mirror.Status.Conditions[0].Status)
	assert.Equal(t, "SyncFailed", mirror.Status.Conditions[0].Reason)
}

func TestReconcile_NoOverlayRefs(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.SchemeBuilder.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	symphony := &apiv1.Symphony{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-symphony",
			Namespace: "test-ns",
		},
		Spec: apiv1.SymphonySpec{
			// No OverlayResourceRefs
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(symphony).
		Build()

	controller := &Controller{
		client:          client,
		scheme:          scheme,
		watcherCacheTTL: 30 * time.Minute,
		allowedKinds:    AllowedSyncKinds,
		reconcileQueue:  workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[ctrl.Request]()),
	}

	result, err := controller.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-symphony",
			Namespace: "test-ns",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}

func TestReconcile_NoCredentials(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.SchemeBuilder.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	symphony := &apiv1.Symphony{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-symphony",
			Namespace: "test-ns",
		},
		Spec: apiv1.SymphonySpec{
			OverlayResourceRefs: []apiv1.OverlayResourceRef{
				{
					Key: "test",
					Resource: apiv1.OverlayResourceSelector{
						Kind:    "ConfigMap",
						Version: "v1",
						Name:    "test-cm",
					},
				},
			},
			// No OverlayCredentials
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(symphony).
		Build()

	controller := &Controller{
		client:          client,
		scheme:          scheme,
		watcherCacheTTL: 30 * time.Minute,
		allowedKinds:    AllowedSyncKinds,
		reconcileQueue:  workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[ctrl.Request]()),
	}

	result, err := controller.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-symphony",
			Namespace: "test-ns",
		},
	})

	require.NoError(t, err)
	// Should return empty result since no credentials
	assert.Equal(t, reconcile.Result{}, result)
}

func TestReconcile_SymphonyNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.SchemeBuilder.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	controller := &Controller{
		client:          client,
		scheme:          scheme,
		watcherCacheTTL: 30 * time.Minute,
		allowedKinds:    AllowedSyncKinds,
		reconcileQueue:  workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[ctrl.Request]()),
	}

	result, err := controller.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "non-existent",
			Namespace: "test-ns",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}
