package synthref

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

func newSynth(name string, labels map[string]string) *apiv1.Synthesizer {
	return &apiv1.Synthesizer{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func selectorRef(name string, labels map[string]string) *apiv1.SynthesizerRef {
	return &apiv1.SynthesizerRef{Name: name, LabelSelector: &metav1.LabelSelector{MatchLabels: labels}}
}

func newClient(t *testing.T, objs ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.SchemeBuilder.AddToScheme(scheme))
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithIndex(&apiv1.Composition{}, manager.IdxCompositionsBySynthesizer, func(o client.Object) []string {
			return []string{manager.SynthesizerIndexKey(o.(*apiv1.Composition))}
		}).
		Build()
}

func TestResolve(t *testing.T) {
	ctx := context.Background()
	cli := newClient(t,
		newSynth("coredns-synth", nil),
		newSynth("coredns-synth-slot-1", map[string]string{"component": "coredns", "version": "v1"}),
		newSynth("coredns-synth-slot-2", map[string]string{"component": "coredns", "version": "v2"}),
	)

	synth, err := Resolve(ctx, cli, &apiv1.SynthesizerRef{Name: "coredns-synth"})
	require.NoError(t, err)
	assert.Equal(t, "coredns-synth", synth.Name)

	synth, err = Resolve(ctx, cli, selectorRef("coredns-synth", map[string]string{"component": "coredns", "version": "v2"}))
	require.NoError(t, err)
	assert.Equal(t, "coredns-synth-slot-2", synth.Name, "the selector wins over the name")

	_, err = Resolve(ctx, cli, selectorRef("coredns-synth", map[string]string{"component": "coredns", "version": "v3"}))
	assert.True(t, IsMissing(err), "no fallback to the name when the selector matches nothing")

	_, err = Resolve(ctx, cli, selectorRef("coredns-synth", map[string]string{"component": "coredns"}))
	assert.ErrorIs(t, err, ErrAmbiguous)
	assert.True(t, IsMissing(err))

	_, err = Resolve(ctx, cli, &apiv1.SynthesizerRef{Name: "missing"})
	assert.True(t, IsMissing(err))
}

func TestResolveFromMap(t *testing.T) {
	synths := map[string]apiv1.Synthesizer{
		"coredns-synth":        *newSynth("coredns-synth", nil),
		"coredns-synth-slot-1": *newSynth("coredns-synth-slot-1", map[string]string{"version": "v1"}),
	}

	synth, err := ResolveFromMap(&apiv1.SynthesizerRef{Name: "coredns-synth"}, synths)
	require.NoError(t, err)
	assert.Equal(t, "coredns-synth", synth.Name)

	synth, err = ResolveFromMap(selectorRef("coredns-synth", map[string]string{"version": "v1"}), synths)
	require.NoError(t, err)
	assert.Equal(t, "coredns-synth-slot-1", synth.Name)

	_, err = ResolveFromMap(selectorRef("coredns-synth", map[string]string{"version": "v2"}), synths)
	assert.True(t, IsMissing(err))

	_, err = ResolveFromMap(&apiv1.SynthesizerRef{Name: "missing"}, synths)
	assert.True(t, IsMissing(err))
}

func TestListCompositions(t *testing.T) {
	ctx := context.Background()
	byName := &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{Name: "by-name", Namespace: "ns"}}
	byName.Spec.Synthesizer = apiv1.SynthesizerRef{Name: "coredns-synth-slot-2"}
	pinned := &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{Name: "pinned", Namespace: "ns"}}
	pinned.Spec.Synthesizer = *selectorRef("coredns-synth", map[string]string{"version": "v2"})
	otherVersion := &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{Name: "other-version", Namespace: "ns"}}
	otherVersion.Spec.Synthesizer = *selectorRef("coredns-synth", map[string]string{"version": "v1"})
	original := &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{Name: "original", Namespace: "ns"}}
	original.Spec.Synthesizer = apiv1.SynthesizerRef{Name: "coredns-synth"}
	cli := newClient(t, byName, pinned, otherVersion, original)

	comps, err := ListCompositions(ctx, cli, newSynth("coredns-synth-slot-2", map[string]string{"version": "v2"}), manager.IdxCompositionsBySynthesizer, Identity)
	require.NoError(t, err)
	names := []string{}
	for _, comp := range comps {
		names = append(names, comp.Name)
	}
	assert.ElementsMatch(t, []string{"by-name", "pinned"}, names)

	// A pinned composition keeps its unit name but is not selected by the original-name synthesizer.
	comps, err = ListCompositions(ctx, cli, newSynth("coredns-synth", nil), manager.IdxCompositionsBySynthesizer, Identity)
	require.NoError(t, err)
	require.Len(t, comps, 1)
	assert.Equal(t, "original", comps[0].Name)
}

func TestMatches(t *testing.T) {
	assert.True(t, Matches(&apiv1.SynthesizerRef{Name: "a"}, newSynth("a", nil)))
	assert.False(t, Matches(&apiv1.SynthesizerRef{Name: "a"}, newSynth("b", nil)))
	assert.True(t, Matches(selectorRef("a", map[string]string{"v": "1"}), newSynth("b", map[string]string{"v": "1"})))
	assert.False(t, Matches(selectorRef("a", map[string]string{"v": "1"}), newSynth("a", nil)))
	invalid := &apiv1.SynthesizerRef{LabelSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "v", Operator: "bogus"}}}}
	assert.False(t, Matches(invalid, newSynth("a", map[string]string{"v": "1"})))
}
