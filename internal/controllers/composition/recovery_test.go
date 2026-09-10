package composition

import (
	"context"
	"errors"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestRecoveryNotReadyReferences(t *testing.T) {
	for _, test := range []struct {
		name string
		refs []*apiv1.ResourceSliceRef
		want []client.ObjectKey
	}{
		{name: "no references"},
		{name: "only malformed", refs: []*apiv1.ResourceSliceRef{nil, {}}},
		{
			name: "malformed and missing before valid",
			refs: []*apiv1.ResourceSliceRef{nil, {}, {Name: "missing"}, {Name: "available"}, nil, {}},
			want: []client.ObjectKey{{Namespace: "default", Name: "missing"}, {Namespace: "default", Name: "available"}},
		},
		{
			name: "valid before malformed",
			refs: []*apiv1.ResourceSliceRef{{Name: "available"}, nil, {}},
			want: []client.ObjectKey{{Namespace: "default", Name: "available"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := testutil.NewContext(t)
			comp := &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Name: "comp", Namespace: "default"},
				Status: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{
					UUID: "current", ResourceSlices: test.refs, TombstoneRecoveryRequired: true,
				}},
			}
			slice := &apiv1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "available", Namespace: comp.Namespace},
				Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{{
					Manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"not-ready","namespace":"default"}}`,
				}}},
			}
			var reads []client.ObjectKey
			writes := 0
			unexpectedWrite := func() error {
				writes++
				return errors.New("diagnostics must not write")
			}
			cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
				Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					reads = append(reads, key)
					return cli.Get(ctx, key, obj, opts...)
				},
				Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
					return unexpectedWrite()
				},
				Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
					return unexpectedWrite()
				},
				Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
					return unexpectedWrite()
				},
				Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
					return unexpectedWrite()
				},
				DeleteAllOf: func(context.Context, client.WithWatch, client.Object, ...client.DeleteAllOfOption) error {
					return unexpectedWrite()
				},
				SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
					return unexpectedWrite()
				},
				SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
					return unexpectedWrite()
				},
			}, comp, slice)
			before := comp.DeepCopy()
			c := &compositionController{client: cli}
			require.NotPanics(t, func() { c.logNotReadyResources(ctx, comp) })
			assert.Equal(t, test.want, reads)
			for _, key := range reads {
				assert.NotEmpty(t, key.Name)
			}
			assert.Zero(t, writes)
			assert.Equal(t, before, comp)
			actual := &apiv1.ResourceSlice{}
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), actual))
			assert.Equal(t, slice.Spec, actual.Spec)
		})
	}
}
