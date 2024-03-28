package manager

import (
	"flag"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/leaderelection"
)

type Options struct {
	leaderelection.Options
	Rest                    *rest.Config
	HealthProbeAddr         string
	MetricsAddr             string
	SynthesizerPodNamespace string  // set in cmd from synthesis config
	qps                     float64 // flags don't support float32, bind to this value and copy over to Rest.QPS during initialization

	// Only set by cmd in reconciler process
	CompositionNamespace string
	CompositionSelector  labels.Selector
}

func (o *Options) Bind(set *flag.FlagSet) {
	set.StringVar(&o.HealthProbeAddr, "health-probe-addr", ":8081", "Address to serve health probes on")
	set.StringVar(&o.MetricsAddr, "metrics-addr", ":8080", "Address to serve Prometheus metrics on")
	set.IntVar(&o.Rest.Burst, "burst", 50, "apiserver client rate limiter burst configuration")
	set.Float64Var(&o.qps, "qps", 20, "Max requests per second to apiserver")
	set.BoolVar(&o.LeaderElection, "leader-election", false, "Enable leader election")
	set.StringVar(&o.LeaderElectionNamespace, "leader-election-namespace", "", "Determines the namespace in which the leader election resource will be created")
	set.StringVar(&o.LeaderElectionResourceLock, "leader-election-resource-lock", "", "Determines which resource lock to use for leader election")
	set.StringVar(&o.LeaderElectionID, "leader-election-id", "", "Determines the name of the resource that leader election will use for holding the leader lock")
}
