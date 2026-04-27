package resource

// managedCreateOrder maps Kind names (group-insensitive) to reserved
// readiness groups. We intentionally key on Kind alone: any resource
// whose Kind matches one of these names is treated as infrastructure
// and reconciled first, regardless of its API group.
//
// User-supplied readiness/deletion groups must be >= -80.
// Values in [-100, -81] are reserved for Eno-managed infrastructure defaults.
// Deletion groups are the negation of the create groups.
// Order derived from Helm's InstallOrder/UninstallOrder:
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
