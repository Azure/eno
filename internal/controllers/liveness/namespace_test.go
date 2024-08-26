package liveness

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestMissingNamespace(t *testing.T) {
	t.Run("symphony", func(t *testing.T) {
		sym := &apiv1.Symphony{}
		sym.Name = "test-symphony"
		sym.Finalizers = []string{"eno.azure.io/cleanup"}
		testMissingNamespace(t, sym)
	})

	t.Run("composition", func(t *testing.T) {
		comp := &apiv1.Composition{}
		comp.Name = "test-composition"
		comp.Finalizers = []string{"eno.azure.io/cleanup"}
		testMissingNamespace(t, comp)
	})

	t.Run("resourceSlice", func(t *testing.T) {
		rs := &apiv1.ResourceSlice{}
		rs.Name = "test-resource-slice"
		rs.Finalizers = []string{"eno.azure.io/cleanup"}
		testMissingNamespace(t, rs)
	})
}

func testMissingNamespace(t *testing.T, orphan client.Object) {
	ns := &corev1.Namespace{}
	ns.Name = "test"

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t, testutil.WithCompositionNamespace(ns.Name))
	cli := mgr.GetClient()

	require.NoError(t, NewNamespaceController(mgr.Manager, time.Millisecond*200))
	mgr.Start(t)

	require.NoError(t, cli.Create(ctx, ns))
	orphan.SetNamespace(ns.Name)
	require.NoError(t, cli.Create(ctx, orphan))

	// Wait for the orphan resource to hit the cache, otherwise the namespace might be deleted first
	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(orphan), orphan)
		if err != nil {
			t.Logf("error while getting orphan resource: %s", err)
			return false
		}
		return true
	})

	// Force delete the namespace
	require.NoError(t, cli.Delete(ctx, ns))

	conf := rest.CopyConfig(mgr.RestConfig)
	conf.GroupVersion = &schema.GroupVersion{Version: "v1"}
	conf.NegotiatedSerializer = serializer.WithoutConversionCodecFactory{CodecFactory: scheme.Codecs}
	rc, err := rest.RESTClientFor(conf)
	require.NoError(t, err)

	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(ns), ns)
		ns.Spec.Finalizers = nil

		_, err = rc.Put().
			AbsPath("/api/v1/namespaces", ns.Name, "/finalize").
			Body(ns).
			Do(ctx).Raw()
		return err
	})
	require.NoError(t, err)

	// The namespace should be completely gone
	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(ns), ns))
	})

	// But we should still be able to eventually remove the orphan's finalizer
	testutil.Eventually(t, func() bool {
		orphan.SetFinalizers(nil)
		err = cli.Update(ctx, orphan)
		if err != nil {
			t.Logf("error while removing finalizer from orphan: %s", err)
		}

		missing := errors.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(orphan), orphan))
		if !missing {
			t.Logf("orphan'd resource still exists")
		}
		return missing
	})

	// Namespace should end up being deleted
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(ns), ns)
		return ns.DeletionTimestamp != nil
	})
}
