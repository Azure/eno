package watchdog

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apiv1 "github.com/Azure/eno/api/v1"
)

var controllerLogicTests = []struct {
	Name                               string
	Composition                        *apiv1.Composition
	ExpectPendingInitialReconciliation bool
	ExpectPendingReconciliation        bool
	ExpectPendingReadiness             bool
	ExpectTerminalError                bool
}{
	{
		Name: "ready",
		Composition: &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				CurrentSynthesis: &apiv1.Synthesis{
					Reconciled: &metav1.Time{},
					Ready:      &metav1.Time{},
				},
			},
		},
	},
	{
		Name: "previously ready",
		Composition: &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				PreviousSynthesis: &apiv1.Synthesis{
					Reconciled: &metav1.Time{},
					Ready:      &metav1.Time{},
				},
			},
		},
	},
	{
		Name: "within threshold",
		Composition: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.Now(),
			},
			Status: apiv1.CompositionStatus{},
		},
	},
	{
		Name: "reconciliation outside of threshold",
		Composition: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute * 3)),
			},
			Status: apiv1.CompositionStatus{},
		},
		ExpectPendingInitialReconciliation: true,
		// readiness isn't firing yet, since we haven't finished reconciliation
	},
	{
		Name: "initial reconciliation outside of synthesis creation threshold",
		Composition: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{},
			Status: apiv1.CompositionStatus{
				CurrentSynthesis: &apiv1.Synthesis{
					Initialized: ptr.To(metav1.NewTime(time.Now().Add(-time.Minute * 3))),
				},
			},
		},
		ExpectPendingInitialReconciliation: true,
		ExpectPendingReconciliation:        true,
	},
	{
		Name: "subsequent reconciliation outside of synthesis creation threshold",
		Composition: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{},
			Status: apiv1.CompositionStatus{
				CurrentSynthesis: &apiv1.Synthesis{
					Initialized: ptr.To(metav1.NewTime(time.Now().Add(-time.Minute * 3))),
				},
				PreviousSynthesis: &apiv1.Synthesis{Reconciled: &metav1.Time{}},
			},
		},
		ExpectPendingReconciliation: true,
	},
	{
		Name: "readiness outside of threshold",
		Composition: &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				CurrentSynthesis: &apiv1.Synthesis{
					Reconciled: ptr.To(metav1.NewTime(time.Now().Add(-time.Minute * 3))),
				},
			},
		},
		ExpectPendingReadiness: true,
	},
	{
		Name: "readiness within threshold",
		Composition: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute * 3)),
			},
			Status: apiv1.CompositionStatus{
				CurrentSynthesis: &apiv1.Synthesis{
					Reconciled: ptr.To(metav1.NewTime(time.Now().Add(-time.Second))),
				},
			},
		},
	},
	{
		Name:                               "in terminal error",
		ExpectTerminalError:                true,
		ExpectPendingInitialReconciliation: true,
		Composition: &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				CurrentSynthesis: &apiv1.Synthesis{
					Results: []apiv1.Result{{
						Severity: "error",
					}},
				},
			},
		},
	},
}

func TestControllerLogic(t *testing.T) {
	for _, tc := range controllerLogicTests {
		t.Run(tc.Name, func(t *testing.T) {
			c := &watchdogController{threshold: time.Minute}
			unrecdInit := c.pendingInitialReconciliation(tc.Composition)
			unrecd := c.pendingReconciliation(tc.Composition)
			unready := c.pendingReadiness(tc.Composition)
			terminal := c.inTerminalError(tc.Composition)
			assert.Equal(t, tc.ExpectPendingInitialReconciliation, unrecdInit, "InitialReconciliation")
			assert.Equal(t, tc.ExpectPendingReconciliation, unrecd, "Reconciliation")
			assert.Equal(t, tc.ExpectPendingReadiness, unready, "Readiness")
			assert.Equal(t, tc.ExpectTerminalError, terminal, "TerminalError")
		})
	}
}
