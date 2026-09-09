package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	flow "github.com/Azure/go-workflow"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	fw "github.com/Azure/eno/e2e/framework"
)

func TestReadinessEnabledServiceTombstoneCompletes(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	const readinessExpression = "self.spec.type == 'ExternalName' || (self.spec.clusterIP != '' && (self.spec.type != 'LoadBalancer' || size(self.spec.externalIPs) > 0 || size(self.status.loadBalancer.ingress) > 0))"

	cli := fw.NewClient(t)
	synthName := fw.UniqueName("tombstone-readiness-synth")
	compName := fw.UniqueName("tombstone-readiness-comp")
	serviceName := fw.UniqueName("tombstone-readiness-service")
	compKey := types.NamespacedName{Name: compName, Namespace: "default"}

	service := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: "default",
			Annotations: map[string]string{
				"eno.azure.io/readiness": readinessExpression,
			},
		},
		Spec: corev1.ServiceSpec{
			Type:         corev1.ServiceTypeExternalName,
			ExternalName: "example.com",
		},
	}
	synth := fw.NewMinimalSynthesizer(synthName, fw.WithCommand(fw.ToCommand(service)))
	comp := fw.NewComposition(compName, "default", fw.WithSynthesizerRefs(apiv1.SynthesizerRef{Name: synthName}))

	var initialSynthesisUUID string
	var initialSynthesizerGeneration int64

	createSynthesizer := fw.CreateStep(t, "createSynthesizer", cli, synth)
	createComposition := fw.CreateStep(t, "createComposition", cli, comp)

	waitInitialReady := flow.Func("waitInitialReady", func(ctx context.Context) error {
		fw.WaitForCompositionReady(t, ctx, cli, compKey, 3*time.Minute)
		require.NoError(t, cli.Get(ctx, compKey, comp))
		require.NotNil(t, comp.Status.CurrentSynthesis)
		initialSynthesisUUID = comp.Status.CurrentSynthesis.UUID
		initialSynthesizerGeneration = comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration
		return nil
	})

	verifyInitialService := flow.Func("verifyInitialService", func(ctx context.Context) error {
		actual := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: serviceName, Namespace: "default"},
		}
		fw.WaitForResourceExists(t, ctx, cli, actual, 30*time.Second)

		manifest, state, err := findServiceInSynthesis(ctx, cli, comp, comp.Status.CurrentSynthesis, serviceName)
		require.NoError(t, err)
		require.False(t, manifest.Deleted)
		annotations := serviceAnnotations(t, manifest)
		require.Equal(t, readinessExpression, annotations["eno.azure.io/readiness"])
		require.NotContains(t, annotations, "eno.azure.io/readiness-group")
		require.NotNil(t, state)
		require.True(t, state.Reconciled)
		require.False(t, state.Deleted)
		require.NotNil(t, state.Ready)
		return nil
	})

	removeServiceFromOutput := flow.Func("removeServiceFromOutput", func(ctx context.Context) error {
		require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: synthName}, synth))
		synth.Spec.Command = fw.ToCommand()
		return cli.Update(ctx, synth)
	})

	waitForTombstoneSynthesis := flow.Func("waitForTombstoneSynthesis", func(ctx context.Context) error {
		fw.WaitForCompositionResynthesized(t, ctx, cli, compKey, initialSynthesizerGeneration, 3*time.Minute)
		require.NoError(t, cli.Get(ctx, compKey, comp))
		require.NotNil(t, comp.Status.PreviousSynthesis)
		require.NotNil(t, comp.Status.CurrentSynthesis)
		require.Equal(t, initialSynthesisUUID, comp.Status.PreviousSynthesis.UUID)
		require.NotEqual(t, initialSynthesisUUID, comp.Status.CurrentSynthesis.UUID)
		return nil
	})

	verifyServiceDeleted := flow.Func("verifyServiceDeleted", func(ctx context.Context) error {
		actual := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: serviceName, Namespace: "default"},
		}
		fw.WaitForResourceDeleted(t, ctx, cli, actual, 60*time.Second)
		return nil
	})

	verifyTombstoneStatus := flow.Func("verifyTombstoneStatus", func(ctx context.Context) error {
		require.NoError(t, cli.Get(ctx, compKey, comp))
		require.NotNil(t, comp.Status.CurrentSynthesis)

		manifest, state, err := findServiceInSynthesis(ctx, cli, comp, comp.Status.CurrentSynthesis, serviceName)
		require.NoError(t, err)
		require.True(t, manifest.Deleted)
		annotations := serviceAnnotations(t, manifest)
		require.Equal(t, readinessExpression, annotations["eno.azure.io/readiness"])
		require.NotContains(t, annotations, "eno.azure.io/readiness-group")
		require.NotNil(t, state)
		require.True(t, state.Reconciled)
		require.True(t, state.Deleted)
		require.NotNil(t, state.Ready)
		return nil
	})

	cleanup := fw.CleanupStep(t, "cleanup", cli, comp, synth)

	w := new(flow.Workflow)
	w.Add(
		flow.Step(createComposition).DependsOn(createSynthesizer),
		flow.Step(waitInitialReady).DependsOn(createComposition),
		flow.Step(verifyInitialService).DependsOn(waitInitialReady),
		flow.Step(removeServiceFromOutput).DependsOn(verifyInitialService),
		flow.Step(waitForTombstoneSynthesis).DependsOn(removeServiceFromOutput),
		flow.Step(verifyServiceDeleted).DependsOn(waitForTombstoneSynthesis),
		flow.Step(verifyTombstoneStatus).DependsOn(waitForTombstoneSynthesis),
		flow.Step(cleanup).DependsOn(verifyServiceDeleted, verifyTombstoneStatus),
	)

	require.NoError(t, w.Do(ctx))
}

func findServiceInSynthesis(
	ctx context.Context,
	cli client.Client,
	comp *apiv1.Composition,
	synthesis *apiv1.Synthesis,
	serviceName string,
) (*apiv1.Manifest, *apiv1.ResourceState, error) {
	for _, ref := range synthesis.ResourceSlices {
		slice := &apiv1.ResourceSlice{}
		if err := cli.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: comp.Namespace}, slice); err != nil {
			return nil, nil, fmt.Errorf("getting resource slice %q: %w", ref.Name, err)
		}

		for i := range slice.Spec.Resources {
			manifest := &slice.Spec.Resources[i]
			obj := &unstructured.Unstructured{}
			if err := obj.UnmarshalJSON([]byte(manifest.Manifest)); err != nil {
				return nil, nil, fmt.Errorf("decoding manifest %d in resource slice %q: %w", i, slice.Name, err)
			}
			if obj.GetKind() != "Service" || obj.GetName() != serviceName {
				continue
			}

			if len(slice.Status.Resources) <= i {
				return manifest, nil, nil
			}
			return manifest, &slice.Status.Resources[i], nil
		}
	}

	return nil, nil, fmt.Errorf("service %q was not found in synthesis %q", serviceName, synthesis.UUID)
}

func serviceAnnotations(t *testing.T, manifest *apiv1.Manifest) map[string]string {
	t.Helper()
	obj := &unstructured.Unstructured{}
	require.NoError(t, obj.UnmarshalJSON([]byte(manifest.Manifest)))
	return obj.GetAnnotations()
}
