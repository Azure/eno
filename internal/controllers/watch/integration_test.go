package watch

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestBasics(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewController(mgr.Manager, 100))
	mgr.Start(t)

	cm := &corev1.ConfigMap{}
	cm.GenerateName = "test-"
	cm.Namespace = "default"
	require.NoError(t, cli.Create(ctx, cm))

	syn := &apiv1.Synthesizer{}
	syn.GenerateName = "test-"
	syn.Spec.Refs = []apiv1.Ref{{
		Key: "foo",
		Resource: apiv1.ResourceRef{
			Group: "",
			Kind:  "ConfigMap",
		},
	}}
	require.NoError(t, cli.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.GenerateName = "test-"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	comp.Spec.Bindings = []apiv1.Binding{{
		Key: "foo",
		Resource: apiv1.ResourceBinding{
			Name:      cm.Name,
			Namespace: cm.Namespace,
		},
	}}
	require.NoError(t, cli.Create(ctx, comp))

	// It should eventually discover the configmap
	testutil.Eventually(t, func() bool {
		rrl := &apiv1.ReferencedResourceList{}
		err := cli.List(ctx, rrl)
		return err == nil && len(rrl.Items) > 0 && rrl.Items[0].Status.LastSeen != nil
	})

	// Everything cleans up gracefully
	require.NoError(t, cli.Delete(ctx, syn))
	testutil.Eventually(t, func() bool {
		rrl := &apiv1.ReferencedResourceList{}
		err := cli.List(ctx, rrl)
		return err == nil && len(rrl.Items) == 0
	})
}

func Test404(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewController(mgr.Manager, 100))
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.GenerateName = "test-"
	syn.Spec.Refs = []apiv1.Ref{{
		Key: "foo",
		Resource: apiv1.ResourceRef{
			Group: "",
			Kind:  "ConfigMap",
		},
	}}
	require.NoError(t, cli.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.GenerateName = "test-"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	comp.Spec.Bindings = []apiv1.Binding{{
		Key: "foo",
		Resource: apiv1.ResourceBinding{
			Name:      "anything",
			Namespace: "default",
		},
	}}
	require.NoError(t, cli.Create(ctx, comp))

	// It should eventually discover the configmap
	testutil.Eventually(t, func() bool {
		rrl := &apiv1.ReferencedResourceList{}
		err := cli.List(ctx, rrl)
		return err == nil && len(rrl.Items) > 0 && rrl.Items[0].Status.LastSeen != nil && rrl.Items[0].Status.LastSeen.Missing
	})

	// Everything cleans up gracefully
	require.NoError(t, cli.Delete(ctx, syn))
	testutil.Eventually(t, func() bool {
		rrl := &apiv1.ReferencedResourceList{}
		err := cli.List(ctx, rrl)
		return err == nil && len(rrl.Items) == 0
	})
}
