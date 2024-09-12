package watch

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/retry"
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
	synth.Name = "test-synth"
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

	// The initial status is populated
	var initialResourceVersion string
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if len(comp.Status.InputRevisions) != 1 {
			return false
		}

		rv := comp.Status.InputRevisions[0].ResourceVersion
		initialResourceVersion = rv
		return rv != ""
	})

	// Update the input
	input.Data = map[string]string{"foo": "bar"}
	require.NoError(t, cli.Update(ctx, input))

	// The status is eventually updated
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if len(comp.Status.InputRevisions) != 1 {
			return false
		}

		rv := comp.Status.InputRevisions[0].ResourceVersion
		return rv != "" && rv != initialResourceVersion
	})
}

func TestIgnoreSideEffects(t *testing.T) {
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
	synth.Name = "test-synth"
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

	// The initial status is populated
	var initialResourceVersion string
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if len(comp.Status.InputRevisions) != 1 {
			return false
		}

		rv := comp.Status.InputRevisions[0].ResourceVersion
		initialResourceVersion = rv
		return rv != ""
	})

	// Start to ignore side effects.
	comp.Annotations = map[string]string{
		"eno.azure.io/ignore-side-effects": "true",
	}
	require.NoError(t, cli.Update(ctx, comp))

	// Give some time to the controller to process this.
	time.Sleep(time.Millisecond * 500)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	require.Equal(t, initialResourceVersion, comp.Status.InputRevisions[0].ResourceVersion)

	// Update the input
	input.Data = map[string]string{"foo": "bar"}
	require.NoError(t, cli.Update(ctx, input))

	// Give some time to the controller to process this.
	time.Sleep(time.Millisecond * 500)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	require.Equal(t, initialResourceVersion, comp.Status.InputRevisions[0].ResourceVersion)

	// Side effects are no longer ignored.
	comp.Annotations = map[string]string{
		"eno.azure.io/ignore-side-effects": "false",
	}
	require.NoError(t, cli.Update(ctx, comp))

	// The status is eventually updated.
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if len(comp.Status.InputRevisions) != 1 {
			return false
		}

		rv := comp.Status.InputRevisions[0].ResourceVersion
		return rv != "" && rv != initialResourceVersion
	})
}

func TestDeferredBasics(t *testing.T) {
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
		Key:   "foo",
		Defer: true,
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

	// The initial status is populated and pending
	var initialResourceVersion string
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if len(comp.Status.InputRevisions) != 1 {
			return false
		}

		rv := comp.Status.InputRevisions[0].ResourceVersion
		initialResourceVersion = rv
		return rv != "" && comp.Status.PendingResynthesis != nil
	})

	// Update the input
	input.Data = map[string]string{"foo": "bar"}
	require.NoError(t, cli.Update(ctx, input))

	// The status is eventually updated
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if len(comp.Status.InputRevisions) != 1 {
			return false
		}

		rv := comp.Status.InputRevisions[0].ResourceVersion
		return rv != "" && rv != initialResourceVersion
	})
}

func TestCompositionChange(t *testing.T) {
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

	// The initial status is populated
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return len(comp.Status.InputRevisions) == 1 && comp.Status.InputRevisions[0].ResourceVersion != ""
	})

	// Create another composition with the same input
	comp2 := &apiv1.Composition{}
	comp2.Name = "test-comp-2"
	comp2.Namespace = "default"
	comp2.Spec.Synthesizer.Name = synth.Name
	comp2.Spec.Bindings = []apiv1.Binding{{
		Key: "foo",
		Resource: apiv1.ResourceBinding{
			Name:      input.Name,
			Namespace: input.Namespace,
		},
	}}
	require.NoError(t, cli.Create(ctx, comp2))

	// The status is populated
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp2), comp2)
		return len(comp2.Status.InputRevisions) == 1 && comp2.Status.InputRevisions[0].ResourceVersion != ""
	})
}

func TestSynthesizerChange(t *testing.T) {
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

	// The initial status is populated
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return len(comp.Status.InputRevisions) == 1 && comp.Status.InputRevisions[0].ResourceVersion != ""
	})

	// Create another composition with the same input
	comp2 := &apiv1.Composition{}
	comp2.Name = "test-comp-2"
	comp2.Namespace = "default"
	comp2.Spec.Synthesizer.Name = synth.Name
	comp2.Spec.Bindings = []apiv1.Binding{{
		Key: "bar", // not the current key
		Resource: apiv1.ResourceBinding{
			Name:      input.Name,
			Namespace: input.Namespace,
		},
	}}
	require.NoError(t, cli.Create(ctx, comp2))

	// Make sure the watch event has been handled before updating the synthesizer
	testutil.Eventually(t, func() bool {
		return cli.Get(ctx, client.ObjectKeyFromObject(comp2), comp2) == nil
	})
	assert.Len(t, comp2.Status.InputRevisions, 0)

	// Update synth to match the binding key
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(synth), synth)
		synth.Spec.Refs[0].Key = "bar"
		return mgr.GetClient().Update(ctx, synth)
	})
	require.NoError(t, err)

	// Things converge
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp2), comp2)
		return len(comp2.Status.InputRevisions) == 1 && comp2.Status.InputRevisions[0].ResourceVersion != ""
	})
}

func TestRemoveInput(t *testing.T) {
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

	// The initial status is populated
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return len(comp.Status.InputRevisions) == 1 && comp.Status.InputRevisions[0].ResourceVersion != ""
	})

	// Remove binding
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Spec.Bindings = nil
		return mgr.GetClient().Update(ctx, comp)
	})
	require.NoError(t, err)

	// Things converge
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return len(comp.Status.InputRevisions) == 0
	})
}
