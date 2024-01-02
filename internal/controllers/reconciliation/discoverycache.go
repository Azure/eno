package reconciliation

import (
	"context"
	"sync"

	"github.com/go-logr/logr"
	openapi_v2 "github.com/google/gnostic-models/openapiv2"
	"gopkg.in/yaml.v2"
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
	mut                   sync.Mutex
	client                discovery.DiscoveryInterface
	fillWhenNotFound      bool
	currentResources      openapi.Resources
	currentSupportedTypes map[schema.GroupVersionKind]struct{}
}

func newDicoveryCache(rc *rest.Config, qps float32, fillWhenNotFound bool) (*discoveryCache, error) {
	conf := rest.CopyConfig(rc)
	conf.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(qps, 1) // parsing the spec is expensive, rate limit to be careful

	disc, err := discovery.NewDiscoveryClientForConfig(rc)
	if err != nil {
		return nil, err
	}
	disc.UseLegacyDiscovery = true // don't bother with aggregated APIs since they may be unavailable

	d := &discoveryCache{client: disc, fillWhenNotFound: fillWhenNotFound, currentSupportedTypes: map[schema.GroupVersionKind]struct{}{}}
	return d, nil
}

func (d *discoveryCache) Get(ctx context.Context, gvk schema.GroupVersionKind) (proto.Schema, error) {
	logger := logr.FromContextOrDiscard(ctx)
	d.mut.Lock()
	defer d.mut.Unlock()

	// Older versions of Kubernetes don't include CRDs in the openapi spec, so on those versions we cannot invalidate the cache if a resource is not found.
	// However, on newer versions we expect every resource to exist in the spec so retries are safe and often necessary.
	for i := 0; i < 2; i++ {
		if d.currentResources == nil {
			logger.V(1).Info("filling discovery cache")
			if err := d.fillUnlocked(ctx); err != nil {
				return nil, err
			}
		}

		model := d.currentResources.LookupResource(gvk)
		if model == nil && d.fillWhenNotFound {
			d.currentResources = nil // invalidate cache - retrieve fresh schema on next attempt
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
	d.currentResources = resources
	d.currentSupportedTypes = buildSupportedTypesMap(doc)
	return nil
}

func (d *discoveryCache) checkSupportUnlocked(ctx context.Context, gvk schema.GroupVersionKind, model proto.Schema) (proto.Schema, error) {
	logger := logr.FromContextOrDiscard(ctx)
	if model == nil {
		logger.V(1).Info("type not found in openapi schema")
		return nil, nil
	}

	if _, ok := d.currentSupportedTypes[gvk]; ok {
		return model, nil
	}

	return nil, nil // doesn't support strategic merge
}

func buildSupportedTypesMap(doc *openapi_v2.Document) map[schema.GroupVersionKind]struct{} {
	// This is copied and adapted from the kubectl openapi package
	// Originally it walked the entire tree for every lookup, we have optimized it down to a single map lookup.
	m := make(map[schema.GroupVersionKind]struct{})
	for _, path := range doc.GetPaths().GetPath() {
		for _, ex := range path.GetValue().GetPatch().GetVendorExtension() {
			if ex.GetValue().GetYaml() == "" ||
				ex.GetName() != "x-kubernetes-group-version-kind" {
				continue
			}

			var value map[string]string
			err := yaml.Unmarshal([]byte(ex.GetValue().GetYaml()), &value)
			if err != nil {
				continue
			}

			gvk := schema.GroupVersionKind{
				Group:   value["group"],
				Version: value["version"],
				Kind:    value["kind"],
			}
			var supported bool
			for _, c := range path.GetValue().GetPatch().GetConsumes() {
				if c == string(types.StrategicMergePatchType) {
					supported = true
					break
				}
			}
			if supported {
				m[gvk] = struct{}{}
			}
		}
	}
	return m
}
