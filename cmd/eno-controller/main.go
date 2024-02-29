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
	"github.com/Azure/eno/internal/manager"
)

// TODO: Expose leader election and other manager options

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := ctrl.SetupSignalHandler()
	var (
		rolloutCooldown  time.Duration
		synthesisTimeout time.Duration
		debugLogging     bool
		synconf          = &synthesis.Config{}
	)
	flag.DurationVar(&synconf.Timeout, "synthesis-pod-timeout", time.Minute, "Maximum lifespan of synthesizer pods")
	flag.Float64Var(&synconf.SliceCreationQPS, "slice-creation-qps", 5, "Max QPS for writing synthesized resources into resource slices")
	flag.DurationVar(&synthesisTimeout, "synthesis-timeout", time.Second*30, "Timeout when executing synthesizer binaries")
	flag.DurationVar(&rolloutCooldown, "rollout-cooldown", time.Second*30, "Minimum period of time between each ensuing composition update after a synthesizer is updated")
	flag.BoolVar(&debugLogging, "debug", true, "Enable debug logging")
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

	mgr, err := manager.New(logger, &manager.Options{
		Rest: ctrl.GetConfigOrDie(),
	})
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

	err = synthesis.NewRolloutController(mgr, rolloutCooldown)
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

	err = synthesis.NewSliceCleanupController(mgr)
	if err != nil {
		return fmt.Errorf("constructing resource slice cleanup controller: %w", err)
	}

	return mgr.Start(ctx)
}
