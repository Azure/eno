package reconciliation

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/flowcontrol"
	"github.com/Azure/eno/internal/readiness"
	"github.com/Azure/eno/internal/reconstitution"
	"github.com/Azure/eno/internal/resource"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/workqueue"
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
	rateLimiter := workqueue.DefaultTypedItemBasedRateLimiter[resource.Request]()
	queue := workqueue.NewTypedRateLimitingQueue(rateLimiter)
	rswb := flowcontrol.NewResourceSliceWriteBufferForManager(mgr.Manager)

	renv, err := readiness.NewEnv()
	require.NoError(t, err)
	cache := resource.NewCache(renv, queue)

	err = New(mgr.Manager, Options{
		Manager:               mgr.Manager,
		Cache:                 cache,
		WriteBuffer:           rswb,
		Downstream:            mgr.DownstreamRestConfig,
		Queue:                 queue,
		DiscoveryRPS:          5,
		Timeout:               time.Minute,
		ReadinessPollInterval: time.Hour,
	})
	require.NoError(t, err)

	err = reconstitution.New(mgr.Manager, cache, queue)
	require.NoError(t, err)
}
