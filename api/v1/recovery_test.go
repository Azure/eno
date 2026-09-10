package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoveryFlagJSON(t *testing.T) {
	for _, flag := range []bool{false, true} {
		name := "false"
		if flag {
			name = "true"
		}
		t.Run(name, func(t *testing.T) {
			comp := &Composition{Status: CompositionStatus{
				CurrentSynthesis:  &Synthesis{UUID: "current", TombstoneRecoveryRequired: flag},
				PreviousSynthesis: &Synthesis{UUID: "previous", TombstoneRecoveryRequired: flag},
				InFlightSynthesis: &Synthesis{UUID: "inflight", TombstoneRecoveryRequired: flag},
			}}
			data, err := json.Marshal(comp)
			require.NoError(t, err)

			var wire struct {
				Status map[string]map[string]json.RawMessage `json:"status"`
			}
			require.NoError(t, json.Unmarshal(data, &wire))
			for _, name := range []string{"currentSynthesis", "previousSynthesis", "inFlightSynthesis"} {
				field, present := wire.Status[name]["tombstoneRecoveryRequired"]
				assert.Equal(t, flag, present, name)
				if flag {
					assert.JSONEq(t, "true", string(field), name)
				}
			}

			var decoded Composition
			require.NoError(t, json.Unmarshal(data, &decoded))
			assert.Equal(t, comp.Status, decoded.Status)
		})
	}

	t.Run("legacy field absent", func(t *testing.T) {
		var comp Composition
		require.NoError(t, json.Unmarshal([]byte(`{"status":{
			"currentSynthesis":{"uuid":"current"},
			"previousSynthesis":{"uuid":"previous"},
			"inFlightSynthesis":{"uuid":"inflight"}
		}}`), &comp))
		for _, syn := range []*Synthesis{comp.Status.CurrentSynthesis, comp.Status.PreviousSynthesis, comp.Status.InFlightSynthesis} {
			require.NotNil(t, syn)
			assert.False(t, syn.TombstoneRecoveryRequired)
		}
	})
}

func TestRecoveryFlagDeepCopy(t *testing.T) {
	for _, flag := range []bool{false, true} {
		comp := &Composition{Status: CompositionStatus{
			CurrentSynthesis:  &Synthesis{UUID: "current", TombstoneRecoveryRequired: flag},
			PreviousSynthesis: &Synthesis{UUID: "previous", TombstoneRecoveryRequired: flag},
			InFlightSynthesis: &Synthesis{UUID: "inflight", TombstoneRecoveryRequired: flag},
		}}
		copied := comp.DeepCopy()
		require.Equal(t, comp, copied)
		originals := []*Synthesis{comp.Status.CurrentSynthesis, comp.Status.PreviousSynthesis, comp.Status.InFlightSynthesis}
		copies := []*Synthesis{copied.Status.CurrentSynthesis, copied.Status.PreviousSynthesis, copied.Status.InFlightSynthesis}
		for i, syn := range copies {
			assert.NotSame(t, originals[i], syn)
			syn.TombstoneRecoveryRequired = !flag
			syn.UUID = "changed"
			assert.Equal(t, flag, originals[i].TombstoneRecoveryRequired)
			assert.NotEqual(t, syn.UUID, originals[i].UUID)
		}
	}
}
