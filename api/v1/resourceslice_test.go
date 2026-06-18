package v1

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestResourceStateEqual(t *testing.T) {
	var tests = []struct {
		Name     string
		Expected bool
		A, B     *ResourceState
	}{
		{
			Name:     "nil",
			Expected: true,
		},
		{
			Name:     "empty",
			Expected: true,
			A:        &ResourceState{},
			B:        &ResourceState{},
		},
		{
			Name: "nil",
			A:    &ResourceState{},
		},
		{
			Name:     "full",
			Expected: true,
			A: &ResourceState{
				Reconciled: true,
				Ready:      &metav1.Time{},
				Deleted:    true,
			},
			B: &ResourceState{
				Reconciled: true,
				Ready:      &metav1.Time{},
				Deleted:    true,
			},
		},
		{
			Name:     "ready-mismatch",
			Expected: false,
			A: &ResourceState{
				Reconciled: true,
				Ready:      &metav1.Time{},
				Deleted:    true,
			},
			B: &ResourceState{
				Reconciled: true,
				Ready:      ptr.To(metav1.NewTime(time.Now().Add(time.Second))),
				Deleted:    true,
			},
		},
		{
			Name:     "ready-mismatch",
			Expected: false,
			A: &ResourceState{
				Reconciled: true,
				Ready:      &metav1.Time{},
				Deleted:    true,
			},
			B: &ResourceState{
				Reconciled: true,
				Deleted:    true,
			},
		},
		{
			Name:     "reconciled-mismatch",
			Expected: false,
			A: &ResourceState{
				Reconciled: true,
				Ready:      &metav1.Time{},
				Deleted:    true,
			},
			B: &ResourceState{
				Reconciled: false,
				Ready:      &metav1.Time{},
				Deleted:    true,
			},
		},
		{
			Name:     "deleted-mismatch",
			Expected: false,
			A: &ResourceState{
				Reconciled: true,
				Ready:      &metav1.Time{},
				Deleted:    true,
			},
			B: &ResourceState{
				Reconciled: true,
				Ready:      &metav1.Time{},
				Deleted:    false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			assert.Equal(t, tt.Expected, tt.A.Equal(tt.B), "a->b")
			assert.Equal(t, tt.Expected, tt.B.Equal(tt.A), "b->a")
		})
	}
}
