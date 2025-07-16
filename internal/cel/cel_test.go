package cel

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestEvalCompositionBasics(t *testing.T) {
	p, err := Parse("composition.metadata.name")
	require.NoError(t, err)

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"

	val, err := Eval(t.Context(), p, comp, &unstructured.Unstructured{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "test-comp", val.Value())
}
