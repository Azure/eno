package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	ctrl "sigs.k8s.io/controller-runtime"

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
		debugLogging  bool
		watchdogThres time.Duration
		synconf       = &synthesis.Config{}

		mgrOpts = &manager.Options{
			Rest: ctrl.GetConfigOrDie(),
		}
	)
	flag.Float64Var(&synconf.SliceCreationQPS, "slice-creation-qps", 5, "Max QPS for writing synthesized resources into resource slices")
	flag.StringVar(&synconf.PodNamespace, "synthesizer-pod-namespace", os.Getenv("POD_NAMESPACE"), "Namespace to create synthesizer pods in. Defaults to POD_NAMESPACE.")
	flag.StringVar(&synconf.PodServiceAccount, "synthesizer-pod-service-account", "", "Service account name to be assigned to synthesizer Pods.")
	flag.BoolVar(&debugLogging, "debug", true, "Enable debug logging")
	flag.DurationVar(&watchdogThres, "watchdog-threshold", time.Minute*5, "How long before the watchdog considers a mid-transition resource to be stuck")
	mgrOpts.Bind(flag.CommandLine)
	flag.Parse()

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

	err = synthesis.NewRolloutController(mgr)
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

	return mgr.Start(ctx)
}
