package resource

// managedCreatedOrder maps infrastructure Kinds to reserved readiness groups
// Resources matching these kinds will have their readiness and deletion groups
// overridden to enforce a safe reconciliation order

// Reserved Range -100 - -81. User groups should be >=-80
// Deletion groups are the negation of the create groups
// Order is derived from Helm's InstallOrder/UninstallOrder
// https://github.com/helm/helm/blob/main/pkg/release/v1/util/kind_sorter.go
var managedCreateOrder = map[string]int{
	"PriorityClass":            -100,
	"Namespace":                -100,
	"NetworkPolicy":            -99,
	"ResourceQuota":            -99,
	"LimitRange":               -99,
	"PodSecurityPolicy":        -98,
	"PodDisruptionBudget":      -98,
	"ServiceAccount":           -97,
	"Secret":                   -96,
	"SecretList":               -96,
	"ConfigMap":                -96,
	"StorageClass":             -95,
	"PersistentVolume":         -94,
	"PersistentVolumeClaim":    -93,
	"CustomResourceDefinition": -92,
	"ClusterRole":              -91,
	"ClusterRoleList":          -91,
	"ClusterRoleBinding":       -91,
	"ClusterRoleBindingList":   -91,
	"Role":                     -90,
	"RoleList":                 -90,
	"RoleBinding":              -90,
	"RoleBindingList":          -90,
	"Service":                  -89,
}

// applyManagedOrdering overrides the readiness and deletion group for managed infrastructure kinds
func applyManagedOrdering(res *Resource) {
	createGrp, ok := managedCreateOrder[res.GVK.Kind]
	if !ok {
		return
	}
	res.readinessGroup = createGrp
	delGroup := -createGrp
	res.deletionGroup = &delGroup
}
