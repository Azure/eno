package discovery

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	discoveryCacheChanges = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "eno_discovery_cache_miss_total",
			Help: "Discovery cache misses excluding fill events (filling cache on startup, etc.)",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(discoveryCacheChanges)
}
