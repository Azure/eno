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
	"github.com/Azure/eno/internal/k8s"
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
		remoteKubeconfigFile   string
		remoteQPS              float64

		mgrOpts = &manager.Options{
			Rest: ctrl.GetConfigOrDie(),
		}
	)
	flag.BoolVar(&rediscoverWhenNotFound, "rediscover-when-not-found", true, "Invalidate discovery cache when any type is not found in the openapi spec. Set this to false on <= k8s 1.14")
	flag.DurationVar(&writeBatchInterval, "write-batch-interval", time.Second*5, "The max throughput of composition status updates")
	flag.BoolVar(&debugLogging, "debug", true, "Enable debug logging")
	flag.StringVar(&remoteKubeconfigFile, "remote-kubeconfig", "", "Path to the kubeconfig of the apiserver where the resources will be reconciled. The config from the environment is used if this is not provided")
	flag.Float64Var(&remoteQPS, "remote-qps", 0, "Max requests per second to the remote apiserver")
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

	mgr, err := manager.NewWithoutIndexing(logger, mgrOpts)
	if err != nil {
		return fmt.Errorf("constructing manager: %w", err)
	}

	recmgr, err := reconstitution.New(mgr, writeBatchInterval)
	if err != nil {
		return fmt.Errorf("constructing reconstitution manager: %w", err)
	}

	remoteConfig := mgr.GetConfig()
	if remoteKubeconfigFile != "" {
		if remoteConfig, err = k8s.GetRESTConfig(remoteKubeconfigFile); err != nil {
			return err
		}
		if remoteQPS != 0 {
			remoteConfig.QPS = float32(remoteQPS)
		}
	}
	err = reconciliation.New(recmgr, remoteConfig, discoveryMaxRPS, rediscoverWhenNotFound)
	if err != nil {
		return fmt.Errorf("constructing reconciliation controller: %w", err)
	}

	return mgr.Start(ctx)
}
