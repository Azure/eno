package discovery

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime/schema"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/kube-openapi/pkg/schemaconv"
	"k8s.io/kube-openapi/pkg/util/proto"
	smdschema "sigs.k8s.io/structured-merge-diff/v4/schema"
)

// Cache is useful to prevent excessive QPS to the discovery APIs while
// still allowing dynamic refresh of the openapi spec on cache misses.
type Cache struct {
	mut              sync.Mutex
	client           discovery.DiscoveryInterface
	fillWhenNotFound bool
	lastFill         time.Time
	current          *smdschema.Schema
	typeNameMap      map[runtimeschema.GroupVersionKind]string
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

func (c *Cache) Get(ctx context.Context, gvk schema.GroupVersionKind) (*smdschema.TypeRef, *smdschema.Schema, error) {
	logger := logr.FromContextOrDiscard(ctx)
	c.mut.Lock()
	defer c.mut.Unlock()

	for i := 0; i < 2; i++ {
		if c.current == nil || time.Since(c.lastFill) > time.Hour*24 {
			logger.V(0).Info("filling discovery cache")
			if err := c.fillUnlocked(ctx); err != nil {
				return nil, nil, err
			}
		}

		model, ok := c.typeNameMap[gvk]
		if !ok && c.fillWhenNotFound {
			c.current = nil // invalidate cache - retrieve fresh schema on next attempt
			logger.V(1).Info("filling discovery cache because type was not found")
			continue
		}
		if !ok {
			logger.V(1).Info("type not found in openapi schema")
			return nil, nil, nil
		}
		return &smdschema.TypeRef{NamedType: &model}, c.current, nil
	}
	return nil, nil, nil
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

	models, err := proto.NewOpenAPIData(doc)
	if err != nil {
		return err
	}
	schema, err := schemaconv.ToSchema(models)
	if err != nil {
		return err
	}

	// Index each type name by GVK
	gvks := map[runtimeschema.GroupVersionKind]string{}
	for _, name := range models.ListModels() {
		schema := models.LookupModel(name)
		for _, gvk := range parseGroupVersionKind(schema) {
			gvks[gvk] = name
		}
	}

	c.current = schema
	c.typeNameMap = gvks
	c.lastFill = time.Now()
	return nil
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

func parseGroupVersionKind(s proto.Schema) []schema.GroupVersionKind {
	extensions := s.GetExtensions()
	gvkListResult := []schema.GroupVersionKind{}

	gvkExtension, ok := extensions["x-kubernetes-group-version-kind"]
	if !ok {
		return []schema.GroupVersionKind{}
	}

	gvkList, ok := gvkExtension.([]interface{})
	if !ok {
		return []schema.GroupVersionKind{}
	}

	for _, gvk := range gvkList {
		gvkMap, ok := gvk.(map[interface{}]interface{})
		if !ok {
			continue
		}
		group, ok := gvkMap["group"].(string)
		if !ok {
			continue
		}
		version, ok := gvkMap["version"].(string)
		if !ok {
			continue
		}
		kind, ok := gvkMap["kind"].(string)
		if !ok {
			continue
		}

		gvkListResult = append(gvkListResult, schema.GroupVersionKind{
			Group:   group,
			Version: version,
			Kind:    kind,
		})
	}

	return gvkListResult
}
