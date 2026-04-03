package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	flow "github.com/Azure/go-workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	fw "github.com/Azure/eno/e2e/framework"
)

// TestResourceDependencyOrdering validates that Eno correctly handles the
// dependency ordering between a Secret and a Deployment that mounts it,
// WITHOUT explicit eno.azure.io/readiness-group annotations.
//
// The synthesizer outputs both a Secret and a Deployment (which mounts
// the Secret as a volume). The test verifies:
//   - Both resources are created successfully
//   - The Deployment becomes available (at least one ready pod)
//   - Pods have zero restarts (no CrashLoopBackOff)
//   - No volume mount failure events occurred
func TestResourceDependencyOrdering(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()

	cli := fw.NewClient(t)

	synthName := fw.UniqueName("dep-order-synth")
	compName := fw.UniqueName("dep-order-comp")
	secretName := fw.UniqueName("dep-order-secret")
	deployName := fw.UniqueName("dep-order-deploy")

	// Secret with NO readiness-group annotation.
	secret := &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"},
		StringData: map[string]string{"token": "test-value-123"},
	}

	// Deployment that mounts the Secret as a volume — NO readiness-group annotation.
	replicas := int32(1)
	deploy := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: deployName, Namespace: "default"},
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
						Image:   "docker.io/busybox:latest",
						Command: []string{"sh", "-c", "cat /etc/secret-vol/token && sleep 3600"},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "secret-vol",
							MountPath: "/etc/secret-vol",
							ReadOnly:  true,
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "secret-vol",
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{
								SecretName: secretName,
							},
						},
					}},
				},
			},
		},
	}

	// Synthesizer outputs both Secret and Deployment.
	// Eno should automatically resolve the dependency ordering.
	synth := fw.NewMinimalSynthesizer(synthName, fw.WithCommand(fw.ToCommand(secret, deploy)))
	comp := fw.NewComposition(compName, "default",
		fw.WithSynthesizerRefs(apiv1.SynthesizerRef{Name: synthName}))
	compKey := types.NamespacedName{Name: compName, Namespace: "default"}

	// --- Workflow steps ---

	createSynthesizer := fw.CreateStep(t, "createSynthesizer", cli, synth)

	createComposition := fw.CreateStep(t, "createComposition", cli, comp)

	waitCompositionReady := flow.Func("waitCompositionReady", func(ctx context.Context) error {
		fw.WaitForCompositionReady(t, ctx, cli, compKey, 4*time.Minute)
		t.Log("composition is Ready")
		return nil
	})

	verifySecret := flow.Func("verifySecret", func(ctx context.Context) error {
		s := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"},
		}
		fw.WaitForResourceExists(t, ctx, cli, s, 30*time.Second)
		assert.Equal(t, "test-value-123", string(s.Data["token"]),
			"secret should contain expected data")
		t.Logf("secret %s exists with correct data", secretName)
		return nil
	})

	verifyDeploymentReady := flow.Func("verifyDeploymentReady", func(ctx context.Context) error {
		err := wait.PollUntilContextTimeout(ctx, 3*time.Second, 3*time.Minute, true,
			func(ctx context.Context) (bool, error) {
				d := &appsv1.Deployment{}
				if err := cli.Get(ctx, types.NamespacedName{
					Name: deployName, Namespace: "default",
				}, d); err != nil {
					return false, nil
				}
				t.Logf("deployment %s: replicas=%d available=%d ready=%d",
					deployName,
					d.Status.Replicas,
					d.Status.AvailableReplicas,
					d.Status.ReadyReplicas)
				return d.Status.AvailableReplicas >= 1, nil
			})
		require.NoError(t, err,
			"timed out waiting for deployment %s to have available replicas", deployName)
		t.Logf("deployment %s is available", deployName)
		return nil
	})

	verifyZeroRestarts := flow.Func("verifyZeroRestarts", func(ctx context.Context) error {
		podList := &corev1.PodList{}
		require.NoError(t, cli.List(ctx, podList,
			client.InNamespace("default"),
			client.MatchingLabels{"app": deployName}))
		require.NotEmpty(t, podList.Items,
			"expected at least one pod for deployment %s", deployName)

		for _, pod := range podList.Items {
			for _, cs := range pod.Status.ContainerStatuses {
				assert.Equal(t, int32(0), cs.RestartCount,
					"pod %s container %s should have 0 restarts", pod.Name, cs.Name)
				assert.True(t, cs.Ready,
					"pod %s container %s should be ready", pod.Name, cs.Name)
				t.Logf("pod %s container %s: restarts=%d ready=%v",
					pod.Name, cs.Name, cs.RestartCount, cs.Ready)
			}
		}
		return nil
	})

	verifyNoMountErrors := flow.Func("verifyNoMountErrors", func(ctx context.Context) error {
		podList := &corev1.PodList{}
		require.NoError(t, cli.List(ctx, podList,
			client.InNamespace("default"),
			client.MatchingLabels{"app": deployName}))

		eventList := &corev1.EventList{}
		require.NoError(t, cli.List(ctx, eventList, client.InNamespace("default")))

		for _, pod := range podList.Items {
			for _, event := range eventList.Items {
				if event.InvolvedObject.Name == pod.Name &&
					event.InvolvedObject.Kind == "Pod" {
					assert.False(t,
						strings.Contains(event.Message, "MountVolume.SetUp failed"),
						"pod %s should not have volume mount failures: %s",
						pod.Name, event.Message)
					assert.False(t,
						strings.Contains(event.Reason, "FailedMount"),
						"pod %s should not have FailedMount event: %s",
						pod.Name, event.Message)
				}
			}
		}
		t.Log("no volume mount errors found in pod events")
		return nil
	})

	deleteComposition := fw.DeleteStep(t, "deleteComposition", cli, comp)

	verifyResourcesDeleted := flow.Func("verifyResourcesDeleted", func(ctx context.Context) error {
		d := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: deployName, Namespace: "default"},
		}
		s := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"},
		}
		fw.WaitForResourceDeleted(t, ctx, cli, d, 90*time.Second)
		fw.WaitForResourceDeleted(t, ctx, cli, s, 90*time.Second)
		t.Log("managed resources (deployment + secret) deleted")
		return nil
	})

	cleanupSynthesizer := fw.CleanupStep(t, "cleanupSynthesizer", cli, synth)

	// --- Wire the workflow DAG ---

	w := new(flow.Workflow)
	w.Add(
		flow.Step(createComposition).DependsOn(createSynthesizer),
		flow.Step(waitCompositionReady).DependsOn(createComposition),

		// Parallel verification after composition is ready
		flow.Step(verifySecret).DependsOn(waitCompositionReady),
		flow.Step(verifyDeploymentReady).DependsOn(waitCompositionReady),

		// Pod-level checks after deployment is verified ready
		flow.Step(verifyZeroRestarts).DependsOn(verifyDeploymentReady),
		flow.Step(verifyNoMountErrors).DependsOn(verifyDeploymentReady),

		// Cleanup after all verifications pass
		flow.Step(deleteComposition).DependsOn(
			verifySecret, verifyZeroRestarts, verifyNoMountErrors),
		flow.Step(verifyResourcesDeleted).DependsOn(deleteComposition),
		flow.Step(cleanupSynthesizer).DependsOn(verifyResourcesDeleted),
	)

	require.NoError(t, w.Do(ctx))
}
