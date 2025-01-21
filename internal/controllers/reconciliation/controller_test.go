package reconciliation

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/flowcontrol"
	"github.com/Azure/eno/internal/reconstitution"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func mapToResource(t *testing.T, res map[string]any) (*unstructured.Unstructured, *reconstitution.Resource) {
	obj := &unstructured.Unstructured{Object: res}
	js, err := obj.MarshalJSON()
	require.NoError(t, err)

	rr := &reconstitution.Resource{
		Manifest: &apiv1.Manifest{Manifest: string(js)},
		GVK:      obj.GroupVersionKind(),
	}
	return obj, rr
}

func setupTestSubject(t *testing.T, mgr *testutil.Manager) *Controller {
	rswb := flowcontrol.NewResourceSliceWriteBufferForManager(mgr.Manager, time.Millisecond*10, 1)
	cache := reconstitution.NewCache(mgr.GetClient())
	rc, err := New(Options{
		Manager:               mgr.Manager,
		Cache:                 cache,
		WriteBuffer:           rswb,
		Downstream:            mgr.DownstreamRestConfig,
		DiscoveryRPS:          5,
		Timeout:               time.Minute,
		ReadinessPollInterval: time.Hour,
	})
	require.NoError(t, err)

	err = reconstitution.New(mgr.Manager, cache, rc)
	require.NoError(t, err)

	return rc
}
