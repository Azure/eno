package liveness

import (
	"testing"

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
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewNamespaceController(mgr.Manager))
	mgr.Start(t)

	ns := &corev1.Namespace{}
	ns.Name = "test"
	require.NoError(t, cli.Create(ctx, ns))

	sym := &apiv1.Symphony{}
	sym.Name = "test-symphony"
	sym.Namespace = ns.Name
	sym.Finalizers = []string{"eno.azure.io/cleanup"} // this would normally be set by another controller
	require.NoError(t, cli.Create(ctx, sym))

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

	// But we should still be able to eventually remove the symphony's finalizer
	require.NoError(t, cli.Delete(ctx, sym))
	testutil.Eventually(t, func() bool {
		if sym.Finalizers != nil {
			sym.Finalizers = nil
			err = cli.Update(ctx, sym)
			if err != nil {
				t.Logf("error while removing finalizer from symphony: %s", err)
			}
		}

		missing := errors.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(sym), sym))
		if !missing {
			t.Logf("symphony still exists")
		}
		return missing
	})

	// Namespace should end up being deleted
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(ns), ns)
		return ns.DeletionTimestamp != nil
	})
}
