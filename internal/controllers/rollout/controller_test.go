package rollout

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
)

func TestController(t *testing.T) {
	cooldown := time.Second

	tests := []struct {
		Name              string
		ExpectedSyntheses []string
		ShouldRequeue     bool
		Inputs            []*apiv1.Composition
	}{
		{
			Name:              "one resource, ready for rollout",
			ExpectedSyntheses: []string{"test"},
			ShouldRequeue:     true,
			Inputs: []*apiv1.Composition{{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Status: apiv1.CompositionStatus{
					PendingResynthesis: inThePast(4),
					CurrentSynthesis: &apiv1.Synthesis{
						Deferred:    true,
						Synthesized: inThePast(8),
					},
				},
			}},
		},
		{
			Name:          "one resource, existing deferred synthesis within cooldown period",
			ShouldRequeue: true,
			Inputs: []*apiv1.Composition{{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Status: apiv1.CompositionStatus{
					PendingResynthesis: inThePast(4),
					CurrentSynthesis: &apiv1.Synthesis{
						Deferred:    true,
						Synthesized: inThePast(0),
					},
				},
			}},
		},
		{
			Name:              "one resource with existing deferred synthesis within cooldown period, another with an active non-rollout synthesis",
			ExpectedSyntheses: []string{"test-1"},
			ShouldRequeue:     true,
			Inputs: []*apiv1.Composition{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-1"},
					Status: apiv1.CompositionStatus{
						PendingResynthesis: inThePast(4),
						CurrentSynthesis: &apiv1.Synthesis{
							Deferred:    true,
							Synthesized: inThePast(8),
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-2"},
					Status: apiv1.CompositionStatus{
						CurrentSynthesis: &apiv1.Synthesis{},
					},
				},
			},
		},
		{
			Name:          "one resource with existing deferred synthesis within cooldown period, another with an active rollout synthesis",
			ShouldRequeue: true,
			Inputs: []*apiv1.Composition{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-1"},
					Status: apiv1.CompositionStatus{
						PendingResynthesis: inThePast(4),
						CurrentSynthesis: &apiv1.Synthesis{
							Deferred:    true,
							Synthesized: inThePast(8),
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-2"},
					Status: apiv1.CompositionStatus{
						CurrentSynthesis: &apiv1.Synthesis{
							Deferred: true,
						},
					},
				},
			},
		},
		{
			Name:              "one resource, existing non-deferred synthesis within cooldown period",
			ExpectedSyntheses: []string{"test"},
			ShouldRequeue:     true,
			Inputs: []*apiv1.Composition{{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Status: apiv1.CompositionStatus{
					PendingResynthesis: inThePast(4),
					CurrentSynthesis: &apiv1.Synthesis{
						Synthesized: inThePast(0),
					},
				},
			}},
		},
		{
			Name:          "one resource, not ready for rollout",
			ShouldRequeue: true,
			Inputs: []*apiv1.Composition{{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Status: apiv1.CompositionStatus{
					PendingResynthesis: inThePast(0),
					CurrentSynthesis: &apiv1.Synthesis{
						Deferred:    true,
						Synthesized: inThePast(8),
					},
				},
			}},
		},
		{
			Name: "no syntheses",
			Inputs: []*apiv1.Composition{{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Status:     apiv1.CompositionStatus{},
			}},
		},
		{
			Name: "one resource, pending but never synthesized",
			Inputs: []*apiv1.Composition{{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Status: apiv1.CompositionStatus{
					PendingResynthesis: inThePast(4),
				},
			}},
		},
		{
			Name:          "one resource, actively synthesizing rollout",
			ShouldRequeue: true,
			Inputs: []*apiv1.Composition{{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Status: apiv1.CompositionStatus{
					PendingResynthesis: inThePast(4),
					CurrentSynthesis: &apiv1.Synthesis{
						Deferred:    true,
						Synthesized: nil,
					},
				},
			}},
		},
		{
			Name:              "one resource, actively synthesizing (not rollout)",
			ExpectedSyntheses: []string{"test"},
			ShouldRequeue:     true,
			Inputs: []*apiv1.Composition{{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Status: apiv1.CompositionStatus{
					PendingResynthesis: inThePast(4),
					CurrentSynthesis: &apiv1.Synthesis{
						Synthesized: nil,
					},
				},
			}},
		},
		{
			Name:              "one resource, actively synthesizing with 2 attempts",
			ExpectedSyntheses: []string{"test"},
			ShouldRequeue:     true,
			Inputs: []*apiv1.Composition{{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Status: apiv1.CompositionStatus{
					PendingResynthesis: inThePast(4),
					CurrentSynthesis: &apiv1.Synthesis{
						Attempts:    2,
						Synthesized: nil,
					},
				},
			}},
		},
		{
			Name:              "fifo semantics",
			ExpectedSyntheses: []string{"test-2"},
			ShouldRequeue:     true,
			Inputs: []*apiv1.Composition{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-1"},
					Status: apiv1.CompositionStatus{
						PendingResynthesis: inThePast(4),
						CurrentSynthesis: &apiv1.Synthesis{
							Synthesized: inThePast(8),
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-2"},
					Status: apiv1.CompositionStatus{
						PendingResynthesis: inThePast(5),
						CurrentSynthesis: &apiv1.Synthesis{
							Synthesized: inThePast(8),
						},
					},
				},
			},
		},
	}

	ctx := testutil.NewContext(t)
	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			cli := testutil.NewClient(t)
			for _, comp := range tc.Inputs {
				err := cli.Create(ctx, comp)
				require.NoError(t, err)

				// set a fake value so we can prove if the controller swapped states later
				if comp.Status.CurrentSynthesis != nil {
					comp.Status.CurrentSynthesis.UUID = "foo"
				}

				err = cli.Status().Update(ctx, comp)
				require.NoError(t, err)
			}

			c := &controller{client: cli, cooldown: cooldown}
			resp, err := c.Reconcile(ctx, ctrl.Request{})
			require.NoError(t, err)
			assert.Equal(t, tc.ShouldRequeue, resp.RequeueAfter != 0 || resp.Requeue)

			list := &apiv1.CompositionList{}
			err = cli.List(ctx, list)
			require.NoError(t, err)

			var names []string
			for _, c := range list.Items {
				if c.Status.CurrentSynthesis != nil && c.Status.CurrentSynthesis.UUID != "foo" {
					names = append(names, c.Name)
					break
				}
			}
			assert.Equal(t, tc.ExpectedSyntheses, names)
		})
	}
}

func inThePast(seconds int) *metav1.Time {
	return ptr.To(metav1.Time{Time: time.Now().Add(-time.Duration(seconds) * time.Second)})
}
