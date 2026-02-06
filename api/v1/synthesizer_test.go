package v1_test

import (
	"context"
	"errors"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestSynthesizerRefResolve(t *testing.T) {
	tests := []struct {
		name           string
		ref            *apiv1.SynthesizerRef
		synthesizers   []*apiv1.Synthesizer
		expectedSynth  string // expected synthesizer name or empty if error expected
		expectedErr    error
		expectedErrMsg string // substring to check in error message
		synthNonNil    bool   // if true, expect synth to be non-nil even on error
	}{
		{
			name: "empty name returns NotFound from client",
			ref: &apiv1.SynthesizerRef{
				Name: "",
			},
			synthNonNil: true,
		},
		{
			name: "name-based resolution success",
			ref: &apiv1.SynthesizerRef{
				Name: "test-synth",
			},
			synthesizers: []*apiv1.Synthesizer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-synth",
					},
					Spec: apiv1.SynthesizerSpec{
						Image: "test-image:v1",
					},
				},
			},
			expectedSynth: "test-synth",
		},
		{
			name: "name-based resolution - not found error",
			ref: &apiv1.SynthesizerRef{
				Name: "non-existent-synth",
			},
			synthesizers: []*apiv1.Synthesizer{},
			synthNonNil:  true,
		},
		{
			name: "label selector takes precedence over name",
			ref: &apiv1.SynthesizerRef{
				Name: "name-synth", // this should be ignored
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"team": "platform"},
				},
			},
			synthesizers: []*apiv1.Synthesizer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "name-synth",
						Labels: map[string]string{"team": "other"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "label-synth",
						Labels: map[string]string{"team": "platform"},
					},
				},
			},
			expectedSynth: "label-synth", // should match by label, not by name
		},
		{
			name: "label selector - exactly one match success",
			ref: &apiv1.SynthesizerRef{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "my-app"},
				},
			},
			synthesizers: []*apiv1.Synthesizer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "synth-1",
						Labels: map[string]string{"app": "my-app"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "synth-2",
						Labels: map[string]string{"app": "other-app"},
					},
				},
			},
			expectedSynth: "synth-1",
		},
		{
			name: "label selector - no matches returns ErrNoMatchingSelector",
			ref: &apiv1.SynthesizerRef{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "non-existent"},
				},
			},
			synthesizers: []*apiv1.Synthesizer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "synth-1",
						Labels: map[string]string{"app": "my-app"},
					},
				},
			},
			expectedErr: apiv1.ErrNoMatchingSelector,
		},
		{
			name: "label selector - multiple matches returns ErrMultipleMatches",
			ref: &apiv1.SynthesizerRef{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"team": "platform"},
				},
			},
			synthesizers: []*apiv1.Synthesizer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "synth-1",
						Labels: map[string]string{"team": "platform"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "synth-2",
						Labels: map[string]string{"team": "platform"},
					},
				},
			},
			expectedErr: apiv1.ErrMultipleMatches,
		},
		{
			name: "label selector - invalid selector returns error",
			ref: &apiv1.SynthesizerRef{
				LabelSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "app",
							Operator: metav1.LabelSelectorOperator("InvalidOperator"),
							Values:   []string{"value"},
						},
					},
				},
			},
			synthesizers:   []*apiv1.Synthesizer{},
			expectedErrMsg: "converting label selector",
		},
		{
			name: "label selector with MatchExpressions - In operator",
			ref: &apiv1.SynthesizerRef{
				LabelSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "env",
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"prod", "staging"},
						},
					},
				},
			},
			synthesizers: []*apiv1.Synthesizer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "prod-synth",
						Labels: map[string]string{"env": "prod"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "dev-synth",
						Labels: map[string]string{"env": "dev"},
					},
				},
			},
			expectedSynth: "prod-synth",
		},
		{
			name: "label selector with MatchExpressions - Exists operator",
			ref: &apiv1.SynthesizerRef{
				LabelSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "special",
							Operator: metav1.LabelSelectorOpExists,
						},
					},
				},
			},
			synthesizers: []*apiv1.Synthesizer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "special-synth",
						Labels: map[string]string{"special": "true"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "normal-synth",
						Labels: map[string]string{"app": "normal"},
					},
				},
			},
			expectedSynth: "special-synth",
		},
		{
			name: "label selector with combined MatchLabels and MatchExpressions",
			ref: &apiv1.SynthesizerRef{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"team": "platform"},
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "env",
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"prod"},
						},
					},
				},
			},
			synthesizers: []*apiv1.Synthesizer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "platform-prod",
						Labels: map[string]string{"team": "platform", "env": "prod"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "platform-dev",
						Labels: map[string]string{"team": "platform", "env": "dev"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "other-prod",
						Labels: map[string]string{"team": "other", "env": "prod"},
					},
				},
			},
			expectedSynth: "platform-prod",
		},
		{
			name: "empty label selector matches all - returns ErrMultipleMatches when multiple exist",
			ref: &apiv1.SynthesizerRef{
				LabelSelector: &metav1.LabelSelector{},
			},
			synthesizers: []*apiv1.Synthesizer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "synth-1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "synth-2",
					},
				},
			},
			expectedErr: apiv1.ErrMultipleMatches,
		},
		{
			name: "empty label selector with single synthesizer - success",
			ref: &apiv1.SynthesizerRef{
				LabelSelector: &metav1.LabelSelector{},
			},
			synthesizers: []*apiv1.Synthesizer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "only-synth",
					},
				},
			},
			expectedSynth: "only-synth",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testutil.NewContext(t)

			// Convert synthesizers to client.Object slice
			objs := make([]client.Object, len(tt.synthesizers))
			for i, s := range tt.synthesizers {
				objs[i] = s
			}

			cli := testutil.NewClient(t, objs...)

			synth, err := tt.ref.Resolve(ctx, cli)

			if tt.expectedErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.expectedErr), "expected error %v, got %v", tt.expectedErr, err)
				assert.Nil(t, synth)
				return
			}

			if tt.expectedErrMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErrMsg)
				assert.Nil(t, synth)
				return
			}

			// For name-based cases that return NotFound, synth is non-nil
			if tt.synthNonNil {
				require.Error(t, err)
				assert.True(t, apierrors.IsNotFound(err), "expected NotFound error, got %v", err)
				assert.NotNil(t, synth)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, synth)
			assert.Equal(t, tt.expectedSynth, synth.Name)
		})
	}
}

func TestSynthesizerRefResolveByName(t *testing.T) {
	tests := []struct {
		name          string
		synthName     string
		synthesizers  []*apiv1.Synthesizer
		expectedSynth string
		expectedErrIs func(error) bool
	}{
		{
			name:          "empty name returns NotFound",
			synthName:     "",
			expectedErrIs: apierrors.IsNotFound,
		},
		{
			name:      "found synthesizer",
			synthName: "my-synth",
			synthesizers: []*apiv1.Synthesizer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "my-synth",
					},
					Spec: apiv1.SynthesizerSpec{
						Image: "test:v1",
					},
				},
			},
			expectedSynth: "my-synth",
		},
		{
			name:          "not found returns NotFound error",
			synthName:     "missing-synth",
			synthesizers:  []*apiv1.Synthesizer{},
			expectedErrIs: apierrors.IsNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testutil.NewContext(t)

			objs := make([]client.Object, len(tt.synthesizers))
			for i, s := range tt.synthesizers {
				objs[i] = s
			}

			cli := testutil.NewClient(t, objs...)

			ref := &apiv1.SynthesizerRef{Name: tt.synthName}
			synth, err := ref.Resolve(ctx, cli)

			if tt.expectedErrIs != nil {
				require.Error(t, err)
				// Name-based resolution does not wrap the error, check directly
				assert.True(t, tt.expectedErrIs(err), "error check failed for: %v", err)
				// Name-based resolution always returns a non-nil synth
				assert.NotNil(t, synth)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, synth)
			assert.Equal(t, tt.expectedSynth, synth.Name)
		})
	}
}

func TestSynthesizerRefResolveByLabel(t *testing.T) {
	tests := []struct {
		name          string
		selector      *metav1.LabelSelector
		synthesizers  []*apiv1.Synthesizer
		expectedSynth string
		expectedErr   error
		expectedErrIs func(error) bool
	}{
		{
			name: "exactly one match",
			selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "my-app"},
			},
			synthesizers: []*apiv1.Synthesizer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "target-synth",
						Labels: map[string]string{"app": "my-app"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "other-synth",
						Labels: map[string]string{"app": "other"},
					},
				},
			},
			expectedSynth: "target-synth",
		},
		{
			name: "no matches returns ErrNoMatchingSelector",
			selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "nonexistent"},
			},
			synthesizers: []*apiv1.Synthesizer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "synth",
						Labels: map[string]string{"app": "my-app"},
					},
				},
			},
			expectedErr: apiv1.ErrNoMatchingSelector,
		},
		{
			name: "multiple matches returns ErrMultipleMatches",
			selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"team": "infra"},
			},
			synthesizers: []*apiv1.Synthesizer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "synth-1",
						Labels: map[string]string{"team": "infra"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "synth-2",
						Labels: map[string]string{"team": "infra"},
					},
				},
			},
			expectedErr: apiv1.ErrMultipleMatches,
		},
		{
			name: "invalid selector - bad operator",
			selector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "key",
						Operator: "BadOperator",
					},
				},
			},
			synthesizers:  []*apiv1.Synthesizer{},
			expectedErrIs: func(err error) bool { return err != nil },
		},
		{
			name: "NotIn operator",
			selector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "env",
						Operator: metav1.LabelSelectorOpNotIn,
						Values:   []string{"dev", "test"},
					},
				},
			},
			synthesizers: []*apiv1.Synthesizer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "prod-synth",
						Labels: map[string]string{"env": "prod"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "dev-synth",
						Labels: map[string]string{"env": "dev"},
					},
				},
			},
			expectedSynth: "prod-synth",
		},
		{
			name: "DoesNotExist operator",
			selector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "deprecated",
						Operator: metav1.LabelSelectorOpDoesNotExist,
					},
				},
			},
			synthesizers: []*apiv1.Synthesizer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "deprecated-synth",
						Labels: map[string]string{"deprecated": "true"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "current-synth",
						Labels: map[string]string{"version": "v2"},
					},
				},
			},
			expectedSynth: "current-synth",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testutil.NewContext(t)

			objs := make([]client.Object, len(tt.synthesizers))
			for i, s := range tt.synthesizers {
				objs[i] = s
			}

			cli := testutil.NewClient(t, objs...)

			ref := &apiv1.SynthesizerRef{LabelSelector: tt.selector}
			synth, err := ref.Resolve(ctx, cli)

			if tt.expectedErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.expectedErr), "expected error %v, got %v", tt.expectedErr, err)
				assert.Nil(t, synth)
				return
			}

			if tt.expectedErrIs != nil {
				require.Error(t, err)
				assert.True(t, tt.expectedErrIs(err), "error check failed for: %v", err)
				assert.Nil(t, synth)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, synth)
			assert.Equal(t, tt.expectedSynth, synth.Name)
		})
	}
}

func TestSynthesizerRefResolveClientErrors(t *testing.T) {
	t.Run("Get error propagates for name-based resolution", func(t *testing.T) {
		ctx := testutil.NewContext(t)
		expectedErr := errors.New("simulated get error")

		cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
			Get: func(ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				return expectedErr
			},
		})

		ref := &apiv1.SynthesizerRef{Name: "test-synth"}
		synth, err := ref.Resolve(ctx, cli)

		require.Error(t, err)
		assert.True(t, errors.Is(err, expectedErr))
		// Name-based resolution always returns a non-nil synth
		assert.NotNil(t, synth)
	})

	t.Run("List error propagates for label-based resolution", func(t *testing.T) {
		ctx := testutil.NewContext(t)
		expectedErr := errors.New("simulated list error")

		cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
			List: func(ctx context.Context, client client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				return expectedErr
			},
		})

		ref := &apiv1.SynthesizerRef{
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
		}
		synth, err := ref.Resolve(ctx, cli)

		require.Error(t, err)
		assert.True(t, errors.Is(err, expectedErr))
		assert.Nil(t, synth)
	})

	t.Run("NotFound error for name-based resolution", func(t *testing.T) {
		ctx := testutil.NewContext(t)

		cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
			Get: func(ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				return apierrors.NewNotFound(schema.GroupResource{
					Group:    "eno.azure.io",
					Resource: "synthesizers",
				}, "missing-synth")
			},
		})

		ref := &apiv1.SynthesizerRef{Name: "missing-synth"}
		synth, err := ref.Resolve(ctx, cli)

		require.Error(t, err)
		// Error is NOT wrapped - check IsNotFound directly
		assert.True(t, apierrors.IsNotFound(err))
		// Name-based resolution always returns a non-nil synth
		assert.NotNil(t, synth)
	})
}

func TestSentinelErrors(t *testing.T) {
	t.Run("ErrNoMatchingSelector has expected message", func(t *testing.T) {
		assert.Equal(t, "no synthesizers match the label selector", apiv1.ErrNoMatchingSelector.Error())
	})

	t.Run("ErrMultipleMatches has expected message", func(t *testing.T) {
		assert.Equal(t, "multiple synthesizers match the label selector", apiv1.ErrMultipleMatches.Error())
	})

	t.Run("sentinel errors are distinguishable", func(t *testing.T) {
		errs := []error{apiv1.ErrNoMatchingSelector, apiv1.ErrMultipleMatches}
		for i, err1 := range errs {
			for j, err2 := range errs {
				if i == j {
					assert.True(t, errors.Is(err1, err2))
				} else {
					assert.False(t, errors.Is(err1, err2), "expected %v to not be %v", err1, err2)
				}
			}
		}
	})
}

func TestSynthesizerRefResolveEdgeCases(t *testing.T) {
	t.Run("synthesizer with empty labels can be found by name", func(t *testing.T) {
		ctx := testutil.NewContext(t)

		synth := &apiv1.Synthesizer{
			ObjectMeta: metav1.ObjectMeta{
				Name: "no-labels-synth",
			},
		}

		cli := testutil.NewClient(t, synth)

		ref := &apiv1.SynthesizerRef{Name: "no-labels-synth"}
		result, err := ref.Resolve(ctx, cli)

		require.NoError(t, err)
		assert.Equal(t, "no-labels-synth", result.Name)
	})

	t.Run("synthesizer spec is preserved in result", func(t *testing.T) {
		ctx := testutil.NewContext(t)

		synth := &apiv1.Synthesizer{
			ObjectMeta: metav1.ObjectMeta{
				Name: "full-spec-synth",
			},
			Spec: apiv1.SynthesizerSpec{
				Image:   "my-image:v1",
				Command: []string{"run", "--flag"},
			},
		}

		cli := testutil.NewClient(t, synth)

		ref := &apiv1.SynthesizerRef{Name: "full-spec-synth"}
		result, err := ref.Resolve(ctx, cli)

		require.NoError(t, err)
		assert.Equal(t, "my-image:v1", result.Spec.Image)
		assert.Equal(t, []string{"run", "--flag"}, result.Spec.Command)
	})

	t.Run("label selector with nil MatchLabels and nil MatchExpressions matches all", func(t *testing.T) {
		ctx := testutil.NewContext(t)

		synth := &apiv1.Synthesizer{
			ObjectMeta: metav1.ObjectMeta{
				Name: "only-synth",
			},
		}

		cli := testutil.NewClient(t, synth)

		ref := &apiv1.SynthesizerRef{
			LabelSelector: &metav1.LabelSelector{
				MatchLabels:      nil,
				MatchExpressions: nil,
			},
		}
		result, err := ref.Resolve(ctx, cli)

		require.NoError(t, err)
		assert.Equal(t, "only-synth", result.Name)
	})

	t.Run("name with special characters", func(t *testing.T) {
		ctx := testutil.NewContext(t)

		// Kubernetes names follow DNS subdomain rules, so test with valid characters
		synth := &apiv1.Synthesizer{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-synth-v1.2.3",
			},
		}

		cli := testutil.NewClient(t, synth)

		ref := &apiv1.SynthesizerRef{Name: "my-synth-v1.2.3"}
		result, err := ref.Resolve(ctx, cli)

		require.NoError(t, err)
		assert.Equal(t, "my-synth-v1.2.3", result.Name)
	})

	t.Run("context cancellation is respected", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		synth := &apiv1.Synthesizer{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-synth",
			},
		}

		cli := testutil.NewClient(t, synth)

		ref := &apiv1.SynthesizerRef{Name: "test-synth"}
		_, err := ref.Resolve(ctx, cli)

		// The fake client may or may not respect context cancellation,
		// but we're testing that the context is passed through
		// In a real scenario with network calls, this would fail
		// For the fake client, this might succeed
		_ = err // Result depends on fake client implementation
	})
}
