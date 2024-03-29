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
		rediscoverWhenNotFound bool
		writeBatchInterval     time.Duration
		discoveryMaxRPS        float32
		debugLogging           bool
		remoteKubeconfigFile   string
		remoteQPS              float64
		readinessPollInterval  time.Duration
		compositionSelector    string
		compositionNamespace   string

		mgrOpts = &manager.Options{
			Rest: ctrl.GetConfigOrDie(),
		}
	)
	flag.BoolVar(&rediscoverWhenNotFound, "rediscover-when-not-found", true, "Invalidate discovery cache when any type is not found in the openapi spec. Set this to false on <= k8s 1.14")
	flag.DurationVar(&writeBatchInterval, "write-batch-interval", time.Second*5, "The max throughput of composition status updates")
	flag.BoolVar(&debugLogging, "debug", true, "Enable debug logging")
	flag.StringVar(&remoteKubeconfigFile, "remote-kubeconfig", "", "Path to the kubeconfig of the apiserver where the resources will be reconciled. The config from the environment is used if this is not provided")
	flag.Float64Var(&remoteQPS, "remote-qps", 0, "Max requests per second to the remote apiserver")
	flag.DurationVar(&readinessPollInterval, "readiness-poll-interval", time.Second*5, "Interval at which non-ready resources will be checked for readiness")
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

	recmgr, err := reconstitution.New(mgr, writeBatchInterval)
	if err != nil {
		return fmt.Errorf("constructing reconstitution manager: %w", err)
	}

	err = aggregation.NewStatusController(mgr)
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
	err = reconciliation.New(recmgr, remoteConfig, discoveryMaxRPS, rediscoverWhenNotFound, readinessPollInterval)
	if err != nil {
		return fmt.Errorf("constructing reconciliation controller: %w", err)
	}

	return mgr.Start(ctx)
}
