package fuzzing

import (
	"context"

	apiv1 "github.com/Azure/eno/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type mutation struct {
	Name      string
	Assert    func(context.Context, *apiv1.Composition, client.Client) (bool, error)
	Apply     func(context.Context, *apiv1.Composition, client.Client) error
	Supported func(context.Context, *apiv1.Composition, client.Client) (bool, error)
}

// TODO: remember that one of the transitions should be deletion, but some transitions are also possible to be applied to a deleted resource
var mutations = []mutation{
	{
		Name: "update synthesizer to generate a single resource",
		Assert: func(ctx context.Context, comp *apiv1.Composition, c client.Client) (bool, error) {
			return false, nil
		},
		Apply: func(ctx context.Context, comp *apiv1.Composition, c client.Client) error {
			return nil
		},
		Supported: func(ctx context.Context, comp *apiv1.Composition, c client.Client) (bool, error) {
			return false, nil
		},
	},
}

// TODO: Mutation ideas
// - Resynthesize (pick random generated synth)
// - Update input
// - Delete resource slice(s)

// TODO: problem: some assertions take precedence over others

// TODO: synth dimensions
// - Resource count
// - Readiness logic
// - Readiness group
// - Resource type
