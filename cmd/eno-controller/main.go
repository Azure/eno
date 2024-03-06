package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/manager"
)

// TODO: Expose leader election and other manager options

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

// TODO: Handle case where resource slices are already deleted but comp still references them

func run() error {
	ctx := ctrl.SetupSignalHandler()
	var (
		debugLogging bool
		synconf      = &synthesis.Config{}

		mgrOpts = &manager.Options{
			Rest: ctrl.GetConfigOrDie(),
		}
	)
	flag.Float64Var(&synconf.SliceCreationQPS, "slice-creation-qps", 5, "Max QPS for writing synthesized resources into resource slices")
	flag.BoolVar(&debugLogging, "debug", true, "Enable debug logging")
	mgrOpts.Bind(flag.CommandLine)
	flag.Parse()

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

	return mgr.Start(ctx)
}
