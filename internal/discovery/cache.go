package discovery

import (
	"context"
	"fmt"
	"sync"
	"time"

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
	lastFill         time.Time
	current          map[schema.GroupVersionKind]proto.Schema
}

func NewCache(rc *rest.Config, qps float32) (*Cache, error) {
	conf := rest.CopyConfig(rc)
	conf.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(qps, 1) // parsing the spec is expensive, rate limit to be careful

	disc, err := discovery.NewDiscoveryClientForConfig(rc)
	if err != nil {
		return nil, err
	}
	disc.UseLegacyDiscovery = true // don't bother with aggregated APIs since they may be unavailable

	d := &Cache{client: disc}
	return d, nil
}

func (c *Cache) Get(ctx context.Context, gvk schema.GroupVersionKind) (proto.Schema, error) {
	logger := logr.FromContextOrDiscard(ctx)
	c.mut.Lock()
	defer c.mut.Unlock()

	for i := 0; i < 2; i++ {
		if c.current == nil || time.Since(c.lastFill) > time.Hour*24 {
			logger.V(0).Info("filling discovery cache")
			if err := c.fillUnlocked(ctx); err != nil {
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

func (c *Cache) fillUnlocked(ctx context.Context) error {
	doc, err := c.client.OpenAPISchema()
	if err != nil {
		return err
	}
	if doc.Info == nil {
		c.fillWhenNotFound = true // fail open
	} else {
		c.fillWhenNotFound = c.evalVersion(ctx, doc.Info.Version)
	}
	c.current, err = buildCurrentSchemaMap(doc)
	c.lastFill = time.Now()
	return err
}

// evalVersion returns true if it's safe to rediscover when a type isn't found on the given version of Kubernetes e.g. "v1.20.0".
// This is necessary because older versions of apiserver do not expose CRDs in the openapi spec.
// So we would end up rediscovering the entire schema before each attempt to sync any CRs.
func (*Cache) evalVersion(ctx context.Context, v string) bool {
	logger := logr.FromContextOrDiscard(ctx)

	var major, minor, patch int
	_, err := fmt.Sscanf(v, "v%d.%d.%d", &major, &minor, &patch)
	if err != nil {
		logger.Error(err, "error while parsing the kubernetes version - defaulting to automatically discover new types")
		return true
	}
	logger.V(1).Info(fmt.Sprintf("discovered apiserver's major/minor version: %d.%d", major, minor))

	return major == 1 && minor >= 15
}
