package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/Azure/eno/internal/controllers/reconciliation"
	"github.com/Azure/eno/internal/manager"
	"github.com/Azure/eno/internal/reconstitution"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

// TODO: Label filters, etc.

func run() error {
	ctx := ctrl.SetupSignalHandler()
	var (
		rediscoverWhenNotFound bool
		writeBatchInterval     time.Duration
		discoveryMaxRPS        float32
	)
	flag.BoolVar(&rediscoverWhenNotFound, "rediscover-when-not-found", true, "Invalidate discovery cache when any type is not found in the openapi spec. Set this to false on <= k8s 1.14")
	flag.DurationVar(&writeBatchInterval, "write-batch-interval", time.Second*5, "The max throughput of composition status updates")
	flag.Parse()

	zl, err := zap.NewProduction()
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
