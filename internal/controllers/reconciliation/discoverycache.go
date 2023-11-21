package reconciliation

import (
	"context"
	"sync"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/kube-openapi/pkg/util/proto"
	"k8s.io/kubectl/pkg/util/openapi"
)

// TODO: Refresh at interval instead

// discoveryCache is useful to prevent excessive QPS to the discovery APIs while
// still allowing dynamic refresh of the openapi spec on cache misses.
type discoveryCache struct {
	mut     sync.Mutex
	client  discovery.DiscoveryInterface
	current openapi.Resources
}

func newDicoveryCache(rc *rest.Config, qps float32) (*discoveryCache, error) {
	conf := rest.CopyConfig(rc)
	conf.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(qps, 1)

	disc, err := discovery.NewDiscoveryClientForConfig(rc)
	if err != nil {
		return nil, err
	}
	disc.UseLegacyDiscovery = true // don't bother with aggregated APIs since they may be unavailable

	d := &discoveryCache{client: disc}
	return d, nil
}

func (d *discoveryCache) Get(ctx context.Context, gvk schema.GroupVersionKind) (proto.Schema, error) {
	d.mut.Lock()
	defer d.mut.Unlock()

	if d.current == nil {
		if err := d.fillUnlocked(ctx); err != nil {
			return nil, err
		}
	}

	model := d.current.LookupResource(gvk)
	return model, nil
}

func (d *discoveryCache) fillUnlocked(ctx context.Context) error {
	logr.FromContextOrDiscard(ctx).V(1).Info("filling discovery cache")

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
