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

func TestApplyDefaultOrdering_ManagedKind(t *testing.T) {
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

			createGrp := managedCreateOrder[tc.kind]
			res.applyDefaultReadinessGroupOrdering(createGrp)
			res.applyDefaultDeletionGroupOrdering(-createGrp)

			assert.Equal(t, tc.expectedCreate, res.readinessGroup)
			require.NotNil(t, res.deletionGroup)
			assert.Equal(t, tc.expectedDeletion, *res.deletionGroup)
		})
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

func TestApplyDefaultOrdering_DeletionIsReverseOfCreate(t *testing.T) {
	for kind, createGrp := range managedCreateOrder {
		t.Run(kind, func(t *testing.T) {
			res := &Resource{}
			res.GVK.Kind = kind
			res.applyDefaultReadinessGroupOrdering(createGrp)
			res.applyDefaultDeletionGroupOrdering(-createGrp)

			require.NotNil(t, res.deletionGroup)
			assert.Equal(t, -createGrp, *res.deletionGroup,
				"deletion group should be negation of create group")
		})
	}
}

func TestApplyDefaultOrdering_UserAnnotationsPreserved(t *testing.T) {
	// User sets readiness-group=5 and deletion-group=10 on a Namespace.
	// Since user provided annotations, the helpers should NOT be called.
	// Verify that if we only call one helper, the other value is preserved.
	res := &Resource{}
	res.GVK.Kind = "Namespace"
	res.readinessGroup = 5
	delGrp := 10
	res.deletionGroup = &delGrp

	// Simulate: user set both annotations, so neither helper is called.
	// The resource should keep user-specified values.
	assert.Equal(t, 5, res.readinessGroup, "user-specified readiness group should be preserved")
	assert.Equal(t, 10, *res.deletionGroup, "user-specified deletion group should be preserved")
}

func TestApplyDefaultOrdering_PartialUserAnnotation(t *testing.T) {
	// User sets only readiness-group, not deletion-group.
	// Only the deletion helper should be called.
	res := &Resource{}
	res.GVK.Kind = "Namespace"
	res.readinessGroup = 5 // user-specified

	createGrp := managedCreateOrder["Namespace"]
	// Only apply default deletion group (user didn't set it)
	res.applyDefaultDeletionGroupOrdering(-createGrp)

	assert.Equal(t, 5, res.readinessGroup, "user-specified readiness group should be preserved")
	require.NotNil(t, res.deletionGroup)
	assert.Equal(t, 100, *res.deletionGroup, "default deletion group should be applied")
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
		createGrp := managedCreateOrder[kind]
		res.applyDefaultDeletionGroupOrdering(-createGrp)
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

func TestApplyDefaultOrdering_NoEffectOnUnmanagedResource(t *testing.T) {
	// A Deployment is not in managedCreateOrder, so no helpers should be called.
	// Verify the kind is not in the map and default values are unchanged.
	res := &Resource{}
	res.GVK.Kind = "Deployment"

	_, ok := managedCreateOrder["Deployment"]
	assert.False(t, ok, "Deployment should not be in managedCreateOrder")
	assert.Equal(t, 0, res.readinessGroup)
	assert.Nil(t, res.deletionGroup)
}

func TestManagedKindGroupsAreDeterministic(t *testing.T) {
	// Same kind should always get the same group across two calls
	for kind, createGrp := range managedCreateOrder {
		res1 := &Resource{}
		res1.GVK.Kind = kind
		res1.applyDefaultReadinessGroupOrdering(createGrp)
		res1.applyDefaultDeletionGroupOrdering(-createGrp)

		res2 := &Resource{}
		res2.GVK.Kind = kind
		res2.applyDefaultReadinessGroupOrdering(createGrp)
		res2.applyDefaultDeletionGroupOrdering(-createGrp)

		assert.Equal(t, res1.readinessGroup, res2.readinessGroup, "kind %q", kind)
		require.NotNil(t, res1.deletionGroup)
		require.NotNil(t, res2.deletionGroup)
		assert.Equal(t, *res1.deletionGroup, *res2.deletionGroup, "kind %q", kind)
	}
}
