package resource

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestManagedCreateOrderCoversExpectedKinds(t *testing.T) {
	expectedKinds := []string{
		"PriorityClass", "Namespace", "NetworkPolicy", "ResourceQuota",
		"LimitRange", "PodSecurityPolicy", "PodDisruptionBudget",
		"ServiceAccount", "Secret", "SecretList", "ConfigMap",
		"StorageClass", "PersistentVolume", "PersistentVolumeClaim",
		"CustomResourceDefinition", "ClusterRole", "ClusterRoleList",
		"ClusterRoleBinding", "ClusterRoleBindingList",
		"Role", "RoleList", "RoleBinding", "RoleBindingList", "Service",
	}
	for _, kind := range expectedKinds {
		_, ok := managedCreateOrder[kind]
		assert.True(t, ok, "expected kind %q to be in managedCreateOrder", kind)
	}
}

func TestManagedCreateOrderGroupRange(t *testing.T) {
	for kind, grp := range managedCreateOrder {
		assert.GreaterOrEqual(t, grp, -100, "kind %q group %d below minimum", kind, grp)
		assert.LessOrEqual(t, grp, -60, "kind %q group %d above reserved max", kind, grp)
	}
}

func TestApplyDefaultOrdering_UnmanagedKindNotInMap(t *testing.T) {
	unmanagedKinds := []string{
		"Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob",
		"Ingress", "IngressClass", "HorizontalPodAutoscaler",
		"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration",
		"APIService", "Pod", "ReplicaSet", "ReplicationController",
	}
	for _, kind := range unmanagedKinds {
		t.Run(kind, func(t *testing.T) {
			_, ok := managedCreateOrder[kind]
			assert.False(t, ok, "kind %q should not be in managedCreateOrder", kind)
		})
	}
}

func TestNewResource_DefaultOrderingForManagedKind(t *testing.T) {
	cases := []struct {
		name          string
		manifest      string
		wantReadiness int
		wantDeletion  *int
	}{
		{
			name: "managed kind without annotations gets defaults",
			manifest: `{"apiVersion":"v1","kind":"Secret",
				"metadata":{"name":"s","namespace":"default"}}`,
			wantReadiness: -100,
			wantDeletion:  intPtr(100),
		},
		{
			name: "user readiness annotation wins; deletion still defaulted",
			manifest: `{"apiVersion":"v1","kind":"Secret",
				"metadata":{"name":"s","namespace":"default",
					"annotations":{"eno.azure.io/readiness-group":"5"}}}`,
			wantReadiness: 5,
			wantDeletion:  intPtr(100),
		},
		{
			name: "user deletion annotation wins; readiness still defaulted",
			manifest: `{"apiVersion":"v1","kind":"Secret",
				"metadata":{"name":"s","namespace":"default",
					"annotations":{"eno.azure.io/deletion-group":"10"}}}`,
			wantReadiness: -100,
			wantDeletion:  intPtr(10),
		},
		{
			name: "user sets both annotations; both win",
			manifest: `{"apiVersion":"v1","kind":"Secret",
				"metadata":{"name":"s","namespace":"default",
					"annotations":{"eno.azure.io/readiness-group":"5","eno.azure.io/deletion-group":"10"}}}`,
			wantReadiness: 5,
			wantDeletion:  intPtr(10),
		},
		{
			name: "unmanaged kind untouched",
			manifest: `{"apiVersion":"apps/v1","kind":"Deployment",
				"metadata":{"name":"d","namespace":"default"}}`,
			wantReadiness: 0,
			wantDeletion:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := &unstructured.Unstructured{}
			require.NoError(t, u.UnmarshalJSON([]byte(tc.manifest)))
			r, err := newResource(context.Background(), u, false)
			require.NoError(t, err)
			assert.Equal(t, tc.wantReadiness, r.readinessGroup)
			if tc.wantDeletion == nil {
				assert.Nil(t, r.deletionGroup)
			} else {
				require.NotNil(t, r.deletionGroup)
				assert.Equal(t, *tc.wantDeletion, *r.deletionGroup)
			}
		})
	}
}

func intPtr(i int) *int { return &i }

func TestManagedOrderingFlat(t *testing.T) {
	// All managed kinds share the same reserved create group (-100) so they
	// are reconciled together as infrastructure, ahead of user resources
	// (whose groups must be >= -60). Deletion groups are the negation.
	for kind, grp := range managedCreateOrder {
		assert.Equal(t, -100, grp, "managed kind %q should be at reserved group -100", kind)
	}
}
