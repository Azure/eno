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
		assert.LessOrEqual(t, grp, -81, "kind %q group %d above reserved max", kind, grp)
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
			wantReadiness: -96,
			wantDeletion:  intPtr(96),
		},
		{
			name: "user readiness annotation wins; deletion still defaulted",
			manifest: `{"apiVersion":"v1","kind":"Secret",
				"metadata":{"name":"s","namespace":"default",
					"annotations":{"eno.azure.io/readiness-group":"5"}}}`,
			wantReadiness: 5,
			wantDeletion:  intPtr(96),
		},
		{
			name: "user deletion annotation wins; readiness still defaulted",
			manifest: `{"apiVersion":"v1","kind":"Secret",
				"metadata":{"name":"s","namespace":"default",
					"annotations":{"eno.azure.io/deletion-group":"10"}}}`,
			wantReadiness: -96,
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

func TestManagedOrderingPrecedence(t *testing.T) {
	// Namespace/PriorityClass (-100) before everything
	assert.Less(t, managedCreateOrder["Namespace"], managedCreateOrder["ServiceAccount"])
	// ServiceAccount (-97) before Secret/ConfigMap (-96)
	assert.Less(t, managedCreateOrder["ServiceAccount"], managedCreateOrder["Secret"])
	assert.Less(t, managedCreateOrder["ServiceAccount"], managedCreateOrder["ConfigMap"])
	// StorageClass (-95) before PV (-94) before PVC (-93)
	assert.Less(t, managedCreateOrder["StorageClass"], managedCreateOrder["PersistentVolume"])
	assert.Less(t, managedCreateOrder["PersistentVolume"], managedCreateOrder["PersistentVolumeClaim"])
	// PVC (-93) before CRD (-92)
	assert.Less(t, managedCreateOrder["PersistentVolumeClaim"], managedCreateOrder["CustomResourceDefinition"])
	// CRD (-92) before ClusterRole (-91)
	assert.Less(t, managedCreateOrder["CustomResourceDefinition"], managedCreateOrder["ClusterRole"])
	// ClusterRole (-91) before Role (-90)
	assert.Less(t, managedCreateOrder["ClusterRole"], managedCreateOrder["Role"])
	// Role (-90) before Service (-89)
	assert.Less(t, managedCreateOrder["Role"], managedCreateOrder["Service"])
}

func TestManagedOrderingDeletionPrecedence(t *testing.T) {
	// Deletion group is negation of create group, so deletion precedence is reversed.
	delGrp := func(kind string) int { return -managedCreateOrder[kind] }

	// Service (+89) < Role (+90) < ClusterRole (+91) < CRD (+92) < ... < Namespace (+100)
	assert.Less(t, delGrp("Service"), delGrp("Role"))
	assert.Less(t, delGrp("Role"), delGrp("ClusterRole"))
	assert.Less(t, delGrp("ClusterRole"), delGrp("CustomResourceDefinition"))
	assert.Less(t, delGrp("CustomResourceDefinition"), delGrp("PersistentVolumeClaim"))
	assert.Less(t, delGrp("PersistentVolumeClaim"), delGrp("PersistentVolume"))
	assert.Less(t, delGrp("PersistentVolume"), delGrp("StorageClass"))
	assert.Less(t, delGrp("StorageClass"), delGrp("ConfigMap"))
	assert.Less(t, delGrp("ConfigMap"), delGrp("ServiceAccount"))
	assert.Less(t, delGrp("ServiceAccount"), delGrp("Namespace"))
}


