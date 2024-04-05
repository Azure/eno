package aggregation

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestBuildSymphonyStatusHappyPath(t *testing.T) {
	now := metav1.Now()

	symph := &apiv1.Symphony{}
	symph.Generation = 123
	symph.Spec.Variations = []apiv1.Variation{{Synthesizer: apiv1.SynthesizerRef{Name: "synth1"}}, {Synthesizer: apiv1.SynthesizerRef{Name: "synth2"}}}

	comp1 := apiv1.Composition{}
	comp1.Name = "comp-1"
	comp1.Spec.Synthesizer.Name = "synth1"
	comp1.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized: &now,
		Reconciled:  ptr.To(metav1.NewTime(now.Add(time.Minute + time.Second))),
		Ready:       ptr.To(metav1.NewTime(now.Add(time.Second))),
	}

	comp2 := apiv1.Composition{}
	comp2.Name = "comp-2"
	comp2.Spec.Synthesizer.Name = "synth2"
	comp2.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized: ptr.To(metav1.NewTime(now.Add(time.Minute))),
		Reconciled:  ptr.To(metav1.NewTime(now.Add(time.Second))),
		Ready:       ptr.To(metav1.NewTime(now.Add(time.Minute + (time.Second * 2)))),
	}

	comps := &apiv1.CompositionList{}
	comps.Items = []apiv1.Composition{comp1, comp2}

	c := &symphonyController{}
	status, changed := c.buildStatus(symph, comps)
	require.True(t, changed)
	assert.Equal(t, apiv1.SymphonyStatus{
		ObservedGeneration: 123,
		Synthesized:        ptr.To(metav1.NewTime(now.Add(time.Minute))),
		Reconciled:         ptr.To(metav1.NewTime(now.Add(time.Minute + time.Second))),
		Ready:              ptr.To(metav1.NewTime(now.Add(time.Minute + (time.Second * 2)))),
	}, status)

	// It should not update the status when it already matches
	symph.Status = status
	_, changed = c.buildStatus(symph, comps)
	require.False(t, changed)
}

func TestBuildSymphonyStatusMissingSynth(t *testing.T) {
	now := metav1.Now()

	symph := &apiv1.Symphony{}
	symph.Spec.Variations = []apiv1.Variation{{Synthesizer: apiv1.SynthesizerRef{Name: "synth1"}}, {Synthesizer: apiv1.SynthesizerRef{Name: "synth2"}}}

	comp1 := apiv1.Composition{}
	comp1.Name = "comp-1"
	comp1.Spec.Synthesizer.Name = "synth1"
	comp1.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized: &now,
		Reconciled:  ptr.To(metav1.NewTime(now.Add(time.Minute + time.Second))),
		Ready:       ptr.To(metav1.NewTime(now.Add(time.Second))),
	}

	comps := &apiv1.CompositionList{}
	comps.Items = []apiv1.Composition{comp1}

	c := &symphonyController{}
	_, changed := c.buildStatus(symph, comps)
	require.False(t, changed)
}

func TestBuildSymphonyStatusMissingSynthesis(t *testing.T) {
	now := metav1.Now()

	symph := &apiv1.Symphony{}
	symph.Spec.Variations = []apiv1.Variation{{Synthesizer: apiv1.SynthesizerRef{Name: "synth1"}}, {Synthesizer: apiv1.SynthesizerRef{Name: "synth2"}}}

	comp1 := apiv1.Composition{}
	comp1.Name = "comp-1"
	comp1.Spec.Synthesizer.Name = "synth1"
	comp1.Status.CurrentSynthesis = nil // missing

	comp2 := apiv1.Composition{}
	comp2.Name = "comp-2"
	comp2.Spec.Synthesizer.Name = "synth2"
	comp2.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized: ptr.To(metav1.NewTime(now.Add(time.Minute))),
		Reconciled:  ptr.To(metav1.NewTime(now.Add(time.Second))),
		Ready:       ptr.To(metav1.NewTime(now.Add(time.Minute + (time.Second * 2)))),
	}

	comps := &apiv1.CompositionList{}
	comps.Items = []apiv1.Composition{comp1, comp2}

	c := &symphonyController{}
	_, changed := c.buildStatus(symph, comps)
	require.False(t, changed)
}
