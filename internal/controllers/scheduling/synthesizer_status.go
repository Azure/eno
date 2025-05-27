package scheduling

import (
	"context"
	"fmt"

	apiv1 "github.com/Azure/eno/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// Condition reasons
	reasonRolloutInProgress = "RolloutInProgress"
	reasonRolloutCompleted  = "RolloutCompleted"
	reasonRolloutPaused     = "RolloutPaused"
	reasonReconciled        = "Reconciled"
	reasonNotReconciled     = "NotReconciled"
)

// updateSynthesizerStatus updates the status conditions of the synthesizer based on the compositions' state
func updateSynthesizerStatus(ctx context.Context, cli client.Client, synth *apiv1.Synthesizer, comps []apiv1.Composition) error {
	// Count compositions that use this synthesizer
	var totalComps, updatedComps, reconciledComps int
	
	for _, comp := range comps {
		if comp.Spec.Synthesizer.Name == synth.Name {
			totalComps++
			
			isCurrent := false

			syn := comp.Status.CurrentSynthesis
			if syn != nil && syn.ObservedSynthesizerGeneration == synth.Generation {
				isCurrent = true
				if syn.Reconciled != nil {
					reconciledComps++
				}
			}
			
			if isCurrent {
				updatedComps++
			}
		}
	}
	
	// Create a copy of the synthesizer to update
	synthCopy := synth.DeepCopy()
	
	// Update rollout completed condition
	if synthCopy.Spec.Paused {
		synthCopy.SetCondition(apiv1.RolloutCompletedCondition, metav1.ConditionUnknown, reasonRolloutPaused, 
			"Rollout is paused")
	} else if totalComps == 0 {
		synthCopy.SetCondition(apiv1.RolloutCompletedCondition, metav1.ConditionTrue, reasonRolloutCompleted, 
			"No compositions are using this synthesizer")
	} else if updatedComps < totalComps {
		synthCopy.SetCondition(apiv1.RolloutCompletedCondition, metav1.ConditionFalse, reasonRolloutInProgress, 
			fmt.Sprintf("Rollout in progress: %d/%d compositions updated", updatedComps, totalComps))
	} else {
		synthCopy.SetCondition(apiv1.RolloutCompletedCondition, metav1.ConditionTrue, reasonRolloutCompleted, 
			fmt.Sprintf("All %d compositions have been updated", totalComps))
	}
	
	// Update first successful reconciliation condition
	if reconciledComps > 0 {
		synthCopy.SetCondition(apiv1.FirstSuccessfulReconciliationCondition, metav1.ConditionTrue, reasonReconciled, 
			fmt.Sprintf("%d compositions have been successfully reconciled", reconciledComps))
	} else if totalComps == 0 {
		synthCopy.SetCondition(apiv1.FirstSuccessfulReconciliationCondition, metav1.ConditionUnknown, reasonNotReconciled, 
			"No compositions are using this synthesizer")
	} else {
		synthCopy.SetCondition(apiv1.FirstSuccessfulReconciliationCondition, metav1.ConditionFalse, reasonNotReconciled, 
			"No compositions have been successfully reconciled")
	}
	
	// Update the synthesizer status if there are changes
	if len(synth.Status.Conditions) != len(synthCopy.Status.Conditions) {
		return updateSynthesizerWithRetry(ctx, cli, synthCopy)
	}
	
	for i, condition := range synthCopy.Status.Conditions {
		if i >= len(synth.Status.Conditions) || 
			condition.Status != synth.Status.Conditions[i].Status ||
			condition.Reason != synth.Status.Conditions[i].Reason || 
			condition.Message != synth.Status.Conditions[i].Message {
			return updateSynthesizerWithRetry(ctx, cli, synthCopy)
		}
	}
	
	return nil
}

// updateSynthesizerWithRetry updates the synthesizer with retry on conflicts
func updateSynthesizerWithRetry(ctx context.Context, cli client.Client, synth *apiv1.Synthesizer) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the latest version of the synthesizer
		current := &apiv1.Synthesizer{}
		if err := cli.Get(ctx, client.ObjectKeyFromObject(synth), current); err != nil {
			return err
		}
		
		// Update only the status
		current.Status = synth.Status
		return cli.Status().Update(ctx, current)
	})
}