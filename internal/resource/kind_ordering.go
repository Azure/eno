package resource

// managedCreateOrder maps Kind names (group-insensitive) to reserved
// readiness groups. We intentionally key on Kind alone: any resource
// whose Kind matches one of these names is treated as infrastructure
// and reconciled first, regardless of its API group.
//
// User-supplied readiness/deletion groups must be >= -60.
// Values in [-100, -60] are reserved for Eno-managed infrastructure defaults.
// Deletion groups are the negation of the create groups.
// Order derived from Helm's InstallOrder/UninstallOrder:
// https://github.com/helm/helm/blob/main/pkg/release/v1/util/kind_sorter.go
var managedCreateOrder = map[string]int{
	"PriorityClass":            -100,
	"Namespace":                -100,
	"NetworkPolicy":            -100,
	"ResourceQuota":            -100,
	"LimitRange":               -100,
	"PodSecurityPolicy":        -100,
	"PodDisruptionBudget":      -100,
	"ServiceAccount":           -100,
	"Secret":                   -100,
	"SecretList":               -100,
	"ConfigMap":                -100,
	"StorageClass":             -100,
	"PersistentVolume":         -100,
	"PersistentVolumeClaim":    -100,
	"CustomResourceDefinition": -100,
	"ClusterRole":              -100,
	"ClusterRoleList":          -100,
	"ClusterRoleBinding":       -100,
	"ClusterRoleBindingList":   -100,
	"Role":                     -100,
	"RoleList":                 -100,
	"RoleBinding":              -100,
	"RoleBindingList":          -100,
	"Service":                  -100,
	// TO-DO: Cleanup this once ETCD will reconcile on its own
	"EtcdCluster": -100,
}
