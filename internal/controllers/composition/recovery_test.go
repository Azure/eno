package composition

import (
	"context"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type recoveryDiagnosticClient struct {
	client.Client // Left nil so any operation other than Get fails the test.
	reader        client.Reader
	reads         []client.ObjectKey
}

func (c *recoveryDiagnosticClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	c.reads = append(c.reads, key)
	return c.reader.Get(ctx, key, obj, opts...)
}

func TestRecoveryNotReadyReferences(t *testing.T) {
	ctx := testutil.NewContext(t)
	comp := &apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{Name: "comp", Namespace: "default"},
		Status: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{
			UUID: "current", TombstoneRecoveryRequired: true,
			ResourceSlices: []*apiv1.ResourceSliceRef{nil, {}, {Name: "missing"}, {Name: "available"}, nil, {}},
		}},
	}
	slice := &apiv1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "available", Namespace: comp.Namespace},
		Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{{
			Manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"not-ready","namespace":"default"}}`,
		}}},
	}
	cli := &recoveryDiagnosticClient{reader: testutil.NewClient(t, comp, slice)}
	before := comp.DeepCopy()
	c := &compositionController{client: cli}
	require.NotPanics(t, func() { c.logNotReadyResources(ctx, comp) })
	assert.Equal(t, []client.ObjectKey{
		{Namespace: comp.Namespace, Name: "missing"},
		{Namespace: comp.Namespace, Name: "available"},
	}, cli.reads, "malformed references must be skipped without preventing later named reads")
	assert.Equal(t, before, comp)
	actual := &apiv1.ResourceSlice{}
	require.NoError(t, cli.reader.Get(ctx, client.ObjectKeyFromObject(slice), actual))
	assert.Equal(t, slice.Spec, actual.Spec)
}
