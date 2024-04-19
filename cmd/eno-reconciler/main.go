package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/Azure/eno/internal/controllers/aggregation"
	"github.com/Azure/eno/internal/controllers/reconciliation"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/flowcontrol"
	"github.com/Azure/eno/internal/k8s"
	"github.com/Azure/eno/internal/manager"
	"github.com/Azure/eno/internal/reconstitution"
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
		writeBatchInterval   time.Duration
		debugLogging         bool
		remoteKubeconfigFile string
		remoteQPS            float64
		compositionSelector  string
		compositionNamespace string

		mgrOpts = &manager.Options{
			Rest: ctrl.GetConfigOrDie(),
		}

		recOpts = reconciliation.Options{
			DiscoveryRPS: 2,
		}
	)
	flag.BoolVar(&recOpts.RediscoverWhenNotFound, "rediscover-when-not-found", true, "Invalidate discovery cache when any type is not found in the openapi spec. Set this to false on <= k8s 1.14")
	flag.DurationVar(&writeBatchInterval, "write-batch-interval", time.Second*5, "The max throughput of composition status updates")
	flag.BoolVar(&debugLogging, "debug", true, "Enable debug logging")
	flag.StringVar(&remoteKubeconfigFile, "remote-kubeconfig", "", "Path to the kubeconfig of the apiserver where the resources will be reconciled. The config from the environment is used if this is not provided")
	flag.Float64Var(&remoteQPS, "remote-qps", 50, "Max requests per second to the remote apiserver")
	flag.DurationVar(&recOpts.Timeout, "timeout", time.Minute, "Per-resource reconciliation timeout. Avoids cases where client retries/timeouts are configured poorly and the loop gets blocked")
	flag.DurationVar(&recOpts.ReadinessPollInterval, "readiness-poll-interval", time.Second*5, "Interval at which non-ready resources will be checked for readiness")
	flag.StringVar(&compositionSelector, "composition-label-selector", labels.Everything().String(), "Optional label selector for compositions to be reconciled")
	flag.StringVar(&compositionNamespace, "composition-namespace", metav1.NamespaceAll, "Optional namespace to limit compositions that will be reconciled")
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

	mgrOpts.CompositionNamespace = compositionNamespace
	if compositionSelector != "" {
		var err error
		mgrOpts.CompositionSelector, err = labels.Parse(compositionSelector)
		if err != nil {
			return fmt.Errorf("invalid composition label selector: %w", err)
		}
	} else {
		mgrOpts.CompositionSelector = labels.Everything()
	}

	mgr, err := manager.NewReconciler(logger, mgrOpts)
	if err != nil {
		return fmt.Errorf("constructing manager: %w", err)
	}

	err = aggregation.NewSliceController(mgr)
	if err != nil {
		return fmt.Errorf("constructing status aggregation controller: %w", err)
	}

	err = synthesis.NewSliceCleanupController(mgr)
	if err != nil {
		return fmt.Errorf("constructing resource slice cleanup controller: %w", err)
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

	// Burst of 1 allows the first write to happen immediately, while subsequent writes are debounced/batched at writeBatchInterval.
	// This provides quick feedback in cases where only a few resources have changed.
	writeBuffer := flowcontrol.NewResourceSliceWriteBufferForManager(mgr, writeBatchInterval, 1)

	rCache := reconstitution.NewCache(mgr.GetClient())
	recOpts.Manager = mgr
	recOpts.Cache = rCache
	recOpts.WriteBuffer = writeBuffer
	recOpts.Downstream = remoteConfig
	reconciler, err := reconciliation.New(recOpts)
	if err != nil {
		return fmt.Errorf("constructing reconciliation controller: %w", err)
	}
	err = reconstitution.New(mgr, rCache, reconciler)
	if err != nil {
		return fmt.Errorf("constructing reconstitution manager: %w", err)
	}

	return mgr.Start(ctx)
}
