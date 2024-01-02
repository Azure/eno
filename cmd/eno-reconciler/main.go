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

	"github.com/Azure/eno/internal/controllers/reconciliation"
	"github.com/Azure/eno/internal/manager"
	"github.com/Azure/eno/internal/reconstitution"
)

// TODO: Support two rest clients: upstream/downstream

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := ctrl.SetupSignalHandler()
	var (
		rediscoverWhenNotFound bool
		writeBatchInterval     time.Duration
		discoveryMaxRPS        float32
		debugLogging           bool

		mgrOpts = &manager.Options{
			Rest: ctrl.GetConfigOrDie(),
		}
	)
	flag.BoolVar(&rediscoverWhenNotFound, "rediscover-when-not-found", true, "Invalidate discovery cache when any type is not found in the openapi spec. Set this to false on <= k8s 1.14")
	flag.DurationVar(&writeBatchInterval, "write-batch-interval", time.Second*5, "The max throughput of composition status updates")
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

	recmgr, err := reconstitution.New(mgr, writeBatchInterval)
	if err != nil {
		return fmt.Errorf("constructing reconstitution manager: %w", err)
	}

	err = reconciliation.New(recmgr, mgr.GetConfig(), discoveryMaxRPS, rediscoverWhenNotFound)
	if err != nil {
		return fmt.Errorf("constructing reconciliation controller: %w", err)
	}

	return mgr.Start(ctx)
}
