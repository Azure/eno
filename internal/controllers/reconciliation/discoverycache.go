package reconciliation

import (
	"context"
	"sync"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/kube-openapi/pkg/util/proto"
	"k8s.io/kubectl/pkg/util/openapi"
)

// discoveryCache is useful to prevent excessive QPS to the discovery APIs while
// still allowing dynamic refresh of the openapi spec on cache misses.
type discoveryCache struct {
	mut              sync.Mutex
	client           discovery.DiscoveryInterface
	fillWhenNotFound bool
	current          openapi.Resources
}

func newDicoveryCache(rc *rest.Config, qps float32, fillWhenNotFound bool) (*discoveryCache, error) {
	conf := rest.CopyConfig(rc)
	conf.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(qps, 1)

	disc, err := discovery.NewDiscoveryClientForConfig(rc)
	if err != nil {
		return nil, err
	}
	disc.UseLegacyDiscovery = true // don't bother with aggregated APIs since they may be unavailable

	d := &discoveryCache{client: disc, fillWhenNotFound: fillWhenNotFound}
	return d, nil
}

func (d *discoveryCache) Get(ctx context.Context, gvk schema.GroupVersionKind) (proto.Schema, error) {
	logger := logr.FromContextOrDiscard(ctx)
	d.mut.Lock()
	defer d.mut.Unlock()

	for i := 0; i < 2; i++ {
		if d.current == nil {
			logger.V(1).Info("filling discovery cache")
			if err := d.fillUnlocked(ctx); err != nil {
				return nil, err
			}
		}

		model := d.current.LookupResource(gvk)
		if model == nil && d.fillWhenNotFound {
			d.current = nil
			continue
		}
		return d.checkSupportUnlocked(ctx, gvk, model)
	}
	return nil, nil
}

func (d *discoveryCache) fillUnlocked(ctx context.Context) error {
	doc, err := d.client.OpenAPISchema()
	if err != nil {
		return err
	}
	resources, err := openapi.NewOpenAPIData(doc)
	if err != nil {
		return err
	}
	d.current = resources
	return nil
}

func (d *discoveryCache) checkSupportUnlocked(ctx context.Context, gvk schema.GroupVersionKind, model proto.Schema) (proto.Schema, error) {
	logger := logr.FromContextOrDiscard(ctx)
	if model == nil {
		logger.V(1).Info("type not found in openapi schema")
		return nil, nil
	}

	for _, c := range d.current.GetConsumes(gvk, "PATCH") {
		if c == string(types.StrategicMergePatchType) {
			logger.V(1).Info("using strategic merge")
			return model, nil
		}
	}

	logger.V(1).Info("not using strategic merge because it is not supported by the resource")
	return nil, nil // doesn't support strategic merge
}