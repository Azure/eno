package watch

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBasics(t *testing.T) {
	mgr := testutil.NewManager(t)
	require.NoError(t, NewController(mgr.Manager))
	mgr.Start(t)

	ctx := testutil.NewContext(t)
	cli := mgr.GetClient()

	input := &corev1.ConfigMap{}
	input.Name = "test-input"
	input.Namespace = "default"
	require.NoError(t, cli.Create(ctx, input))

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-comp"
	synth.Namespace = "default"
	synth.Spec.Refs = []apiv1.Ref{{
		Key: "foo",
		Resource: apiv1.ResourceRef{
			Version: "v1",
			Kind:    "ConfigMap",
		},
	}}
	require.NoError(t, cli.Create(ctx, synth))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = synth.Name
	comp.Spec.Bindings = []apiv1.Binding{{
		Key: "foo",
		Resource: apiv1.ResourceBinding{
			Name:      input.Name,
			Namespace: input.Namespace,
		},
	}}
	require.NoError(t, cli.Create(ctx, comp))

	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized: ptr.To(metav1.Now()),
	}
	require.NoError(t, cli.Status().Update(ctx, comp))

	// Make sure all informers are in sync
	time.Sleep(time.Millisecond * 200)

	// Update the input
	input.Data = map[string]string{"foo": "bar"}
	require.NoError(t, cli.Update(ctx, input))

	// The composition should be resynthesized
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		syn := comp.Status.CurrentSynthesis
		return syn != nil && syn.Synthesized == nil
	})
}
