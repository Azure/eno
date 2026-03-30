package resource

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestApplyManagedOrdering_ManagedKind(t *testing.T) {
	tests := []struct {
		kind             string
		expectedCreate   int
		expectedDeletion int
	}{
		{"Namespace", -100, 100},
		{"PriorityClass", -100, 100},
		{"ServiceAccount", -97, 97},
		{"Secret", -96, 96},
		{"ConfigMap", -96, 96},
		{"CustomResourceDefinition", -92, 92},
		{"ClusterRole", -91, 91},
		{"Role", -90, 90},
		{"Service", -89, 89},
	}

	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			res := &Resource{}
			res.GVK.Kind = tc.kind
			res.readinessGroup = 5 // user tried to set a custom group

			applyManagedOrdering(res)

			assert.Equal(t, tc.expectedCreate, res.readinessGroup)
			require.NotNil(t, res.deletionGroup)
			assert.Equal(t, tc.expectedDeletion, *res.deletionGroup)
		})
	}
}

func TestApplyManagedOrdering_UnmanagedKind(t *testing.T) {
	unmanagedKinds := []string{
		"Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob",
		"Ingress", "IngressClass", "HorizontalPodAutoscaler",
		"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration",
		"APIService", "Pod", "ReplicaSet", "ReplicationController",
	}
	for _, kind := range unmanagedKinds {
		t.Run(kind, func(t *testing.T) {
			res := &Resource{}
			res.GVK.Kind = kind
			res.readinessGroup = 3

			applyManagedOrdering(res)

			assert.Equal(t, 3, res.readinessGroup, "group should remain user-defined")
			assert.Nil(t, res.deletionGroup, "should have no auto-assigned deletion group")
		})
	}
}

func TestApplyManagedOrdering_DeletionIsReverseOfCreate(t *testing.T) {
	for kind, createGrp := range managedCreateOrder {
		t.Run(kind, func(t *testing.T) {
			res := &Resource{}
			res.GVK.Kind = kind
			applyManagedOrdering(res)

			require.NotNil(t, res.deletionGroup)
			assert.Equal(t, -createGrp, *res.deletionGroup,
				"deletion group should be negation of create group")
		})
	}
}

func TestApplyManagedOrdering_OverridesUserAnnotations(t *testing.T) {
	// User sets readiness-group=5 and deletion-group=10 on a Namespace.
	// Both should be overridden.
	res := &Resource{}
	res.GVK.Kind = "Namespace"
	res.readinessGroup = 5
	delGrp := 10
	res.deletionGroup = &delGrp

	applyManagedOrdering(res)

	assert.Equal(t, -100, res.readinessGroup)
	require.NotNil(t, res.deletionGroup)
	assert.Equal(t, 100, *res.deletionGroup)
}

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
	// Deletion order is reversed: Service deleted first, Namespace last
	getDelGrp := func(kind string) int {
		res := &Resource{}
		res.GVK.Kind = kind
		applyManagedOrdering(res)
		return *res.deletionGroup
	}

	// Service (+89) < Role (+90) < ClusterRole (+91) < CRD (+92) < ... < Namespace (+100)
	assert.Less(t, getDelGrp("Service"), getDelGrp("Role"))
	assert.Less(t, getDelGrp("Role"), getDelGrp("ClusterRole"))
	assert.Less(t, getDelGrp("ClusterRole"), getDelGrp("CustomResourceDefinition"))
	assert.Less(t, getDelGrp("CustomResourceDefinition"), getDelGrp("PersistentVolumeClaim"))
	assert.Less(t, getDelGrp("PersistentVolumeClaim"), getDelGrp("PersistentVolume"))
	assert.Less(t, getDelGrp("PersistentVolume"), getDelGrp("StorageClass"))
	assert.Less(t, getDelGrp("StorageClass"), getDelGrp("ConfigMap"))
	assert.Less(t, getDelGrp("ConfigMap"), getDelGrp("ServiceAccount"))
	assert.Less(t, getDelGrp("ServiceAccount"), getDelGrp("Namespace"))
}

func TestApplyManagedOrdering_NoEffectOnDefaultUnmanagedResource(t *testing.T) {
	// A Deployment with no explicit annotations should be unaffected
	res := &Resource{}
	res.GVK.Kind = "Deployment"

	applyManagedOrdering(res)

	assert.Equal(t, 0, res.readinessGroup)
	assert.Nil(t, res.deletionGroup)
}

func TestManagedKindGroupsAreDeterministic(t *testing.T) {
	// Same kind should always get the same group across two calls
	for kind := range managedCreateOrder {
		res1 := &Resource{}
		res1.GVK.Kind = kind
		applyManagedOrdering(res1)

		res2 := &Resource{}
		res2.GVK.Kind = kind
		applyManagedOrdering(res2)

		assert.Equal(t, res1.readinessGroup, res2.readinessGroup, "kind %q", kind)
		require.NotNil(t, res1.deletionGroup)
		require.NotNil(t, res2.deletionGroup)
		assert.Equal(t, *res1.deletionGroup, *res2.deletionGroup, "kind %q", kind)
	}
}
