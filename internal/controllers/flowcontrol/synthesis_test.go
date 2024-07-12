package flowcontrol

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSynthesisConcurrencyLimitUnder(t *testing.T) {
	cli := testutil.NewClient(t)
	ctx := testutil.NewContext(t)
	c := &synthesisConcurrencyLimiter{}
	c.client = cli
	c.limit = 1

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	require.NoError(t, cli.Create(ctx, comp))

	comp.Status.CurrentSynthesis = &apiv1.Synthesis{}
	require.NoError(t, cli.Status().Update(ctx, comp))

	_, err := c.Reconcile(ctx, ctrl.Request{})
	require.NoError(t, err)

	err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	require.NoError(t, err)
	assert.NotEmpty(t, comp.Status.CurrentSynthesis.UUID)
}

func TestSynthesisConcurrencyLimitNotReady(t *testing.T) {
	cli := testutil.NewClient(t)
	ctx := testutil.NewContext(t)
	c := &synthesisConcurrencyLimiter{}
	c.client = cli
	c.limit = 1

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	require.NoError(t, cli.Create(ctx, comp))
	// Nil CurrentSynthesis

	_, err := c.Reconcile(ctx, ctrl.Request{})
	require.NoError(t, err)

	err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	require.NoError(t, err)
	assert.Nil(t, comp.Status.CurrentSynthesis)
}

func TestSynthesisConcurrencyLimitOver(t *testing.T) {
	cli := testutil.NewClient(t)
	ctx := testutil.NewContext(t)
	c := &synthesisConcurrencyLimiter{}
	c.client = cli
	c.limit = 1

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	require.NoError(t, cli.Create(ctx, comp))

	comp.Status.CurrentSynthesis = &apiv1.Synthesis{}
	require.NoError(t, cli.Status().Update(ctx, comp))

	comp2 := &apiv1.Composition{}
	comp2.Name = "test-comp-2"
	require.NoError(t, cli.Create(ctx, comp2))

	comp2.Status.CurrentSynthesis = &apiv1.Synthesis{}
	require.NoError(t, cli.Status().Update(ctx, comp2))

	for i := 0; i < 3; i++ {
		_, err := c.Reconcile(ctx, ctrl.Request{})
		require.NoError(t, err)
	}

	var active int
	err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	require.NoError(t, err)
	if comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.UUID != "" {
		active++
	}

	err = cli.Get(ctx, client.ObjectKeyFromObject(comp2), comp2)
	require.NoError(t, err)
	if comp2.Status.CurrentSynthesis != nil && comp2.Status.CurrentSynthesis.UUID != "" {
		active++
	}

	assert.Equal(t, 1, active) // only one was dispatched
}
