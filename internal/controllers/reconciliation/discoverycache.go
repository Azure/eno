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
)

// discoveryCache is useful to prevent excessive QPS to the discovery APIs while
// still allowing dynamic refresh of the openapi spec on cache misses.
type discoveryCache struct {
	mut              sync.Mutex
	client           discovery.DiscoveryInterface
	fillWhenNotFound bool
	current          map[schema.GroupVersionKind]proto.Schema
}

func newDicoveryCache(rc *rest.Config, qps float32, fillWhenNotFound bool) (*discoveryCache, error) {
	conf := rest.CopyConfig(rc)
	conf.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(qps, 1) // parsing the spec is expensive, rate limit to be careful

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

	// Older versions of Kubernetes don't include CRDs in the openapi spec, so on those versions we cannot invalidate the cache if a resource is not found.
	// However, on newer versions we expect every resource to exist in the spec so retries are safe and often necessary.
	for i := 0; i < 2; i++ {
		if d.current == nil {
			logger.V(1).Info("filling discovery cache")
			if err := d.fillUnlocked(ctx); err != nil {
				return nil, err
			}
		}

		model, ok := d.current[gvk]
		if !ok && d.fillWhenNotFound {
			d.current = nil // invalidate cache - retrieve fresh schema on next attempt
			continue
		}
		if ok && model == nil {
			logger.V(1).Info("type does not support strategic merge")
		} else if model == nil {
			logger.V(1).Info("type not found in openapi schema")
		}
		return model, nil
	}
	return nil, nil
}

func (d *discoveryCache) fillUnlocked(ctx context.Context) error {
	doc, err := d.client.OpenAPISchema()
	if err != nil {
		return err
	}
	d.current, err = buildCurrentSchemaMap(doc)
	if err != nil {
		return err
	}
	return nil
}
