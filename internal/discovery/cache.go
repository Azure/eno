package discovery

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

// Cache is useful to prevent excessive QPS to the discovery APIs while
// still allowing dynamic refresh of the openapi spec on cache misses.
type Cache struct {
	mut              sync.Mutex
	client           discovery.DiscoveryInterface
	fillWhenNotFound bool
	current          map[schema.GroupVersionKind]proto.Schema
}

func NewCache(rc *rest.Config, qps float32, fillWhenNotFound bool) (*Cache, error) {
	conf := rest.CopyConfig(rc)
	conf.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(qps, 1) // parsing the spec is expensive, rate limit to be careful

	disc, err := discovery.NewDiscoveryClientForConfig(rc)
	if err != nil {
		return nil, err
	}
	disc.UseLegacyDiscovery = true // don't bother with aggregated APIs since they may be unavailable

	d := &Cache{client: disc, fillWhenNotFound: fillWhenNotFound}
	return d, nil
}

func (c *Cache) Get(ctx context.Context, gvk schema.GroupVersionKind) (proto.Schema, error) {
	logger := logr.FromContextOrDiscard(ctx)
	c.mut.Lock()
	defer c.mut.Unlock()

	// Older versions of Kubernetes don't include CRDs in the openapi spec, so on those versions we cannot invalidate the cache if a resource is not found.
	// However, on newer versions we expect every resource to exist in the spec so retries are safe and often necessary.
	for i := 0; i < 2; i++ {
		if c.current == nil {
			logger.V(0).Info("filling discovery cache")
			if err := c.fillUnlocked(); err != nil {
				return nil, err
			}
		}

		model, ok := c.current[gvk]
		if !ok && c.fillWhenNotFound {
			c.current = nil // invalidate cache - retrieve fresh schema on next attempt
			discoveryCacheChanges.Inc()
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

func (c *Cache) fillUnlocked() error {
	doc, err := c.client.OpenAPISchema()
	if err != nil {
		return err
	}
	c.current, err = buildCurrentSchemaMap(doc)
	if err != nil {
		return err
	}
	return nil
}
