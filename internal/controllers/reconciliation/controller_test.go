package reconciliation

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/flowcontrol"
	"github.com/Azure/eno/internal/resource"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func mapToResource(t *testing.T, res map[string]any) (*unstructured.Unstructured, *resource.Resource) {
	obj := &unstructured.Unstructured{Object: res}
	js, err := obj.MarshalJSON()
	require.NoError(t, err)

	rr := &resource.Resource{
		Manifest: &apiv1.Manifest{Manifest: string(js)},
		GVK:      obj.GroupVersionKind(),
	}
	return obj, rr
}

func setupTestSubject(t *testing.T, mgr *testutil.Manager) {
	rswb := flowcontrol.NewResourceSliceWriteBufferForManager(mgr.Manager)

	err := New(mgr.Manager, Options{
		Manager:               mgr.Manager,
		WriteBuffer:           rswb,
		Downstream:            mgr.DownstreamRestConfig,
		DiscoveryRPS:          5,
		Timeout:               time.Minute,
		ReadinessPollInterval: time.Hour,
	})
	require.NoError(t, err)
}
