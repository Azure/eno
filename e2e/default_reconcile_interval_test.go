package e2e

import (
	"context"
	"testing"
	"time"

	flow "github.com/Azure/go-workflow"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	fw "github.com/Azure/eno/e2e/framework"
)

// TestDefaultReconcileIntervalRecreatesDeletedResources validates the
// eno.azure.io/use-default-reconcile-interval annotation.
//
// A synthesizer emits a ServiceAccount, ClusterRole, RoleBinding and Deployment.
// All four carry eno.azure.io/use-default-reconcile-interval: "true". Only the
// Deployment additionally sets an explicit eno.azure.io/reconcile-interval; the
// other three rely purely on the default interval opt-in.
//
// After the composition is Ready, the four live resources are deleted out-of-band
// (simulating a customer deleting a resource they can see). Without a reconcile
// interval Eno would not requeue these resources until the next synthesis, so they
// would stay deleted. The test verifies that eno-reconciler recreates each resource
// (observed via a change in UID) purely because of the periodic requeue driven by
// the default reconcile interval (and, for the Deployment, its explicit interval).
//
// NOTE: This requires the eno-reconciler deployed in the cluster to be built from a
// revision that supports eno.azure.io/use-default-reconcile-interval.
func TestDefaultReconcileIntervalRecreatesDeletedResources(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cli := fw.NewClient(t)

	synthName := fw.UniqueName("default-interval-synth")
	compName := fw.UniqueName("default-interval-comp")
	saName := fw.UniqueName("default-interval-sa")
	clusterRoleName := fw.UniqueName("default-interval-cr")
	roleBindingName := fw.UniqueName("default-interval-rb")
	deployName := fw.UniqueName("default-interval-deploy")

	const useDefault = "eno.azure.io/use-default-reconcile-interval"

	// ServiceAccount - opts into the default reconcile interval, no explicit interval.
	sa := &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        saName,
			Namespace:   "default",
			Annotations: map[string]string{useDefault: "true"},
		},
	}

	// ClusterRole (cluster-scoped) - opts into the default reconcile interval.
	clusterRole := &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        clusterRoleName,
			Annotations: map[string]string{useDefault: "true"},
		},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""},
			Resources: []string{"configmaps"},
			Verbs:     []string{"get", "list", "watch"},
		}},
	}

	// RoleBinding - opts into the default reconcile interval, binds the ClusterRole to the ServiceAccount.
	roleBinding := &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        roleBindingName,
			Namespace:   "default",
			Annotations: map[string]string{useDefault: "true"},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     clusterRoleName,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      saName,
			Namespace: "default",
		}},
	}

	// Deployment - opts into the default reconcile interval AND sets an explicit interval.
	replicas := int32(1)
	deploy := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployName,
			Namespace: "default",
			Annotations: map[string]string{
				useDefault:                        "true",
				"eno.azure.io/reconcile-interval": "10s",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": deployName},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": deployName},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:    "test",
						Image:   "docker.io/busybox:1.36.1",
						Command: []string{"sh", "-c", "sleep 3600"},
					}},
				},
			},
		},
	}

	synth := fw.NewMinimalSynthesizer(synthName,
		fw.WithCommand(fw.ToCommand(sa, clusterRole, roleBinding, deploy)))
	comp := fw.NewComposition(compName, "default",
		fw.WithSynthesizerRefs(apiv1.SynthesizerRef{Name: synthName}))
	compKey := types.NamespacedName{Name: compName, Namespace: "default"}

	// managed captures the resources Eno should manage together with a live handle
	// that we use to read/delete them and track their UIDs for the recreation check.
	type managed struct {
		name string
		obj  client.Object
	}
	resources := []managed{
		{name: saName, obj: &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: "default"}}},
		{name: clusterRoleName, obj: &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: clusterRoleName}}},
		{name: roleBindingName, obj: &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: roleBindingName, Namespace: "default"}}},
		{name: deployName, obj: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: deployName, Namespace: "default"}}},
	}

	// originalUIDs records the UID observed for each resource before the out-of-band
	// deletion so the recreation check can confirm a genuinely new object.
	originalUIDs := make(map[string]types.UID, len(resources))

	// --- Workflow steps ---

	createSynthesizer := fw.CreateStep(t, "createSynthesizer", cli, synth)

	createComposition := fw.CreateStep(t, "createComposition", cli, comp)

	waitCompositionReady := flow.Func("waitCompositionReady", func(ctx context.Context) error {
		fw.WaitForCompositionReady(t, ctx, cli, compKey, 4*time.Minute)
		t.Log("composition is Ready")
		return nil
	})

	verifyCreated := flow.Func("verifyCreated", func(ctx context.Context) error {
		for _, r := range resources {
			fw.WaitForResourceExists(t, ctx, cli, r.obj, 60*time.Second)
			uid := r.obj.GetUID()
			require.NotEmpty(t, uid, "resource %s should have a UID once created", r.name)
			originalUIDs[r.name] = uid
			t.Logf("resource %s created with UID %s", r.name, uid)
		}
		return nil
	})

	deleteResourcesOutOfBand := flow.Func("deleteResourcesOutOfBand", func(ctx context.Context) error {
		for _, r := range resources {
			t.Logf("deleting resource %s out-of-band", r.name)
			require.NoError(t, cli.Delete(ctx, r.obj), "failed to delete %s out-of-band", r.name)
		}
		return nil
	})

	verifyRecreated := flow.Func("verifyRecreated", func(ctx context.Context) error {
		for _, r := range resources {
			key := client.ObjectKeyFromObject(r.obj)
			err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 60*time.Second, true,
				func(ctx context.Context) (bool, error) {
					if err := cli.Get(ctx, key, r.obj); err != nil {
						return false, nil
					}
					// Ensure this is a freshly recreated object (new UID) and not the
					// terminating original still lingering.
					if r.obj.GetDeletionTimestamp() != nil {
						return false, nil
					}
					return r.obj.GetUID() != "" && r.obj.GetUID() != originalUIDs[r.name], nil
				})
			require.NoError(t, err,
				"timed out waiting for eno-reconciler to recreate %s (original UID %s)", r.name, originalUIDs[r.name])
			t.Logf("resource %s recreated with new UID %s", r.name, r.obj.GetUID())
		}
		return nil
	})

	deleteComposition := fw.DeleteStep(t, "deleteComposition", cli, comp)

	verifyResourcesDeleted := flow.Func("verifyResourcesDeleted", func(ctx context.Context) error {
		for _, r := range resources {
			fw.WaitForResourceDeleted(t, ctx, cli, r.obj, 90*time.Second)
		}
		t.Log("all managed resources deleted after composition deletion")
		return nil
	})

	cleanupSynthesizer := fw.CleanupStep(t, "cleanupSynthesizer", cli, synth)

	// --- Wire the workflow DAG ---

	w := new(flow.Workflow)
	w.Add(
		flow.Step(createComposition).DependsOn(createSynthesizer),
		flow.Step(waitCompositionReady).DependsOn(createComposition),
		flow.Step(verifyCreated).DependsOn(waitCompositionReady),
		flow.Step(deleteResourcesOutOfBand).DependsOn(verifyCreated),
		flow.Step(verifyRecreated).DependsOn(deleteResourcesOutOfBand),
		flow.Step(deleteComposition).DependsOn(verifyRecreated),
		flow.Step(verifyResourcesDeleted).DependsOn(deleteComposition),
		flow.Step(cleanupSynthesizer).DependsOn(verifyResourcesDeleted),
	)

	require.NoError(t, w.Do(ctx))
}
