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

func TestResourceSliceIdentifierAt(t *testing.T) {
	tests := []struct {
		Name      string
		Resources []Manifest
		Idx       int
		Expected  string
	}{
		{
			Name: "pre-parsed fields preferred",
			Resources: []Manifest{{
				Manifest:   `{"kind":"FromJSON","metadata":{"name":"json-name"}}`,
				ParsedKind: "ConfigMap",
				ParsedName: "my-cm",
			}},
			Idx:      0,
			Expected: "ConfigMap/my-cm",
		},
		{
			Name: "falls back to manifest when pre-parsed fields are empty",
			Resources: []Manifest{{
				Manifest: `{"kind":"Secret","metadata":{"name":"my-secret"}}`,
			}},
			Idx:      0,
			Expected: "Secret/my-secret",
		},
		{
			Name: "falls back to manifest when only parsed kind is set",
			Resources: []Manifest{{
				Manifest:   `{"kind":"Secret","metadata":{"name":"my-secret"}}`,
				ParsedKind: "Secret",
			}},
			Idx:      0,
			Expected: "Secret/my-secret",
		},
		{
			Name: "falls back to manifest when only parsed name is set",
			Resources: []Manifest{{
				Manifest:   `{"kind":"Secret","metadata":{"name":"my-secret"}}`,
				ParsedName: "my-secret",
			}},
			Idx:      0,
			Expected: "Secret/my-secret",
		},
		{
			Name: "picks correct index",
			Resources: []Manifest{
				{ParsedKind: "ConfigMap", ParsedName: "first"},
				{ParsedKind: "Secret", ParsedName: "second"},
				{ParsedKind: "Pod", ParsedName: "third"},
			},
			Idx:      1,
			Expected: "Secret/second",
		},
		{
			Name:      "empty manifest and no pre-parsed fields",
			Resources: []Manifest{{}},
			Idx:       0,
			Expected:  "",
		},
		{
			Name: "invalid JSON",
			Resources: []Manifest{{
				Manifest: `{not json`,
			}},
			Idx:      0,
			Expected: "",
		},
		{
			Name: "manifest missing kind",
			Resources: []Manifest{{
				Manifest: `{"metadata":{"name":"my-secret"}}`,
			}},
			Idx:      0,
			Expected: "",
		},
		{
			Name: "manifest missing name",
			Resources: []Manifest{{
				Manifest: `{"kind":"Secret","metadata":{}}`,
			}},
			Idx:      0,
			Expected: "",
		},
		{
			Name: "negative index",
			Resources: []Manifest{{
				ParsedKind: "ConfigMap",
				ParsedName: "my-cm",
			}},
			Idx:      -1,
			Expected: "",
		},
		{
			Name: "index out of bounds",
			Resources: []Manifest{{
				ParsedKind: "ConfigMap",
				ParsedName: "my-cm",
			}},
			Idx:      1,
			Expected: "",
		},
		{
			Name:      "index out of bounds on empty resources",
			Resources: nil,
			Idx:       0,
			Expected:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			s := &ResourceSlice{
				Spec: ResourceSliceSpec{Resources: tt.Resources},
			}
			assert.Equal(t, tt.Expected, s.IdentifierAt(tt.Idx))
		})
	}
}
