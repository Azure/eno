package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/Azure/eno/internal/controllers/aggregation"
	"github.com/Azure/eno/internal/controllers/replication"
	"github.com/Azure/eno/internal/controllers/rollout"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/controllers/watchdog"
	"github.com/Azure/eno/internal/manager"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := ctrl.SetupSignalHandler()
	var (
		debugLogging    bool
		watchdogThres   time.Duration
		rolloutCooldown time.Duration
		taintToleration string
		nodeAffinity    string
		synconf         = &synthesis.Config{}

		mgrOpts = &manager.Options{
			Rest: ctrl.GetConfigOrDie(),
		}
	)
	flag.Float64Var(&synconf.SliceCreationQPS, "slice-creation-qps", 5, "Max QPS for writing synthesized resources into resource slices")
	flag.StringVar(&synconf.PodNamespace, "synthesizer-pod-namespace", os.Getenv("POD_NAMESPACE"), "Namespace to create synthesizer pods in. Defaults to POD_NAMESPACE.")
	flag.StringVar(&synconf.PodServiceAccount, "synthesizer-pod-service-account", "", "Service account name to be assigned to synthesizer Pods.")
	flag.BoolVar(&debugLogging, "debug", true, "Enable debug logging")
	flag.DurationVar(&watchdogThres, "watchdog-threshold", time.Minute*5, "How long before the watchdog considers a mid-transition resource to be stuck")
	flag.DurationVar(&rolloutCooldown, "rollout-cooldown", time.Second*2, "How long before an update to related resource (synthesizer, bindings, etc.) will trigger a composition's re-synthesis")
	flag.StringVar(&taintToleration, "taint-toleration", "", "Node NoSchedule taint to be tolerated by synthesizer pods e.g. taintKey=taintValue to match on value, just taintKey to match on presence of the taint")
	flag.StringVar(&nodeAffinity, "node-affinity", "", "Synthesizer pods will be created with this required node affinity expression e.g. labelKey=labelValue to match on value, just labelKey to match on presence of the label")
	mgrOpts.Bind(flag.CommandLine)
	flag.Parse()

	synconf.NodeAffinityKey, synconf.NodeAffinityValue = parseKeyValue(nodeAffinity)
	synconf.TaintTolerationKey, synconf.TaintTolerationValue = parseKeyValue(taintToleration)

	if synconf.PodNamespace == "" {
		return fmt.Errorf("a value is required in --synthesizer-pod-namespace or POD_NAMESPACE")
	}
	mgrOpts.SynthesizerPodNamespace = synconf.PodNamespace

	zapCfg := zap.NewProductionConfig()
	if debugLogging {
		zapCfg.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
	}
	zl, err := zapCfg.Build()
	if err != nil {
		return err
	}
	logger := zapr.NewLogger(zl)

	mgrOpts.Rest.UserAgent = "eno-controller"
	mgr, err := manager.New(logger, mgrOpts)
	if err != nil {
		return fmt.Errorf("constructing manager: %w", err)
	}

	synconn, err := synthesis.NewSynthesizerConnection(mgr)
	if err != nil {
		return fmt.Errorf("constructing synthesizer connection: %w", err)
	}

	err = synthesis.NewExecController(mgr, synconf, synconn)
	if err != nil {
		return fmt.Errorf("constructing execution controller: %w", err)
	}

	err = rollout.NewController(mgr, rolloutCooldown)
	if err != nil {
		return fmt.Errorf("constructing rollout controller: %w", err)
	}

	err = synthesis.NewStatusController(mgr)
	if err != nil {
		return fmt.Errorf("constructing status controller: %w", err)
	}

	err = synthesis.NewPodLifecycleController(mgr, synconf)
	if err != nil {
		return fmt.Errorf("constructing pod lifecycle controller: %w", err)
	}

	err = watchdog.NewController(mgr, watchdogThres)
	if err != nil {
		return fmt.Errorf("constructing watchdog controller: %w", err)
	}

	err = replication.NewSymphonyController(mgr)
	if err != nil {
		return fmt.Errorf("constructing symphony replication controller: %w", err)
	}

	err = aggregation.NewSymphonyController(mgr)
	if err != nil {
		return fmt.Errorf("constructing symphony aggregation controller: %w", err)
	}

	return mgr.Start(ctx)
}

func parseKeyValue(input string) (key, val string) {
	chunks := strings.SplitN(input, "=", 2)
	key = chunks[0]
	if len(chunks) > 1 {
		val = chunks[1]
	}
	return
}
