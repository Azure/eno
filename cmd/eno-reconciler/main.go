package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/Azure/eno/internal/cel"
	"github.com/Azure/eno/internal/controllers/liveness"
	"github.com/Azure/eno/internal/controllers/reconciliation"
	"github.com/Azure/eno/internal/flowcontrol"
	"github.com/Azure/eno/internal/k8s"
	"github.com/Azure/eno/internal/logging"
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
		debugLogging                 bool
		remoteKubeconfigFile         string
		remoteQPS                    float64
		compositionSelector          string
		compositionNamespace         string
		resourceFilter               string
		namespaceCreationGracePeriod time.Duration
		namespaceCleanup             bool
		enoBuildVersion              string
		migratingFieldManagers       string

		mgrOpts = &manager.Options{
			Rest: ctrl.GetConfigOrDie(),
		}

		recOpts = reconciliation.Options{}
	)
	flag.BoolVar(&debugLogging, "debug", true, "Enable debug logging")
	flag.StringVar(&remoteKubeconfigFile, "remote-kubeconfig", "", "Path to the kubeconfig of the apiserver where the resources will be reconciled. The config from the environment is used if this is not provided")
	flag.Float64Var(&remoteQPS, "remote-qps", 50, "Max requests per second to the remote apiserver")
	flag.DurationVar(&recOpts.Timeout, "timeout", time.Minute, "Per-resource reconciliation timeout. Avoids cases where client retries/timeouts are configured poorly and the loop gets blocked")
	flag.DurationVar(&recOpts.ReadinessPollInterval, "readiness-poll-interval", time.Second*5, "Interval at which non-ready resources will be checked for readiness")
	flag.DurationVar(&recOpts.MinReconcileInterval, "min-reconcile-interval", time.Second, "Minimum value of eno.azure.com/reconcile-interval that will be honored by the controller")
	flag.BoolVar(&recOpts.DisableServerSideApply, "disable-ssa", false, "Use non-strategic three-way merge patches instead of server-side apply")
	flag.StringVar(&compositionSelector, "composition-label-selector", labels.Everything().String(), "Optional label selector for compositions to be reconciled")
	flag.StringVar(&compositionNamespace, "composition-namespace", metav1.NamespaceAll, "Optional namespace to limit compositions that will be reconciled")
	flag.StringVar(&resourceFilter, "resource-filter", "", "Optional CEL filter expression for resources within compositions to be reconciled")
	flag.DurationVar(&namespaceCreationGracePeriod, "ns-creation-grace-period", time.Second, "A namespace is assumed to be missing if it doesn't exist once one of its resources has existed for this long")
	flag.BoolVar(&namespaceCleanup, "namespace-cleanup", true, "Clean up orphaned resources caused by namespace force-deletions")
	flag.BoolVar(&recOpts.FailOpen, "fail-open", false, "Report that resources are reconciled once they've been seen, even if reconciliation failed. Overridden by individual resources with 'eno.azure.io/fail-open: true|false'")
	flag.StringVar(&migratingFieldManagers, "migrating-field-managers", "", "Comma-separated list of Kubernetes SSA field manager names to take ownership from during migrations")
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
	enoBuildVersion = os.Getenv("ENO_BUILD_VERSION")
	logger := logging.NewLoggerWithBuild(zl, enoBuildVersion)

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

	if resourceFilter != "" {
		var err error
		recOpts.ResourceFilter, err = cel.Parse(resourceFilter)
		if err != nil {
			return fmt.Errorf("invalid resource filter expression: %w", err)
		}
	}

	mgrOpts.Rest.UserAgent = "eno-reconciler"
	mgr, err := manager.NewReconciler(logger, mgrOpts)
	if err != nil {
		return fmt.Errorf("constructing manager: %w", err)
	}

	if namespaceCleanup {
		err = liveness.NewNamespaceController(mgr, 5, namespaceCreationGracePeriod)
		if err != nil {
			return fmt.Errorf("constructing namespace liveness controller: %w", err)
		}
	}

	remoteConfig := rest.CopyConfig(mgr.GetConfig())
	if remoteKubeconfigFile != "" {
		if remoteConfig, err = k8s.GetRESTConfig(remoteKubeconfigFile); err != nil {
			return err
		}
	}
	if remoteQPS >= 0 {
		remoteConfig.QPS = float32(remoteQPS)
	}

	// Burst of 1 allows the first write to happen immediately, while subsequent writes are debounced/batched at writeBatchInterval.
	// This provides quick feedback in cases where only a few resources have changed.
	writeBuffer := flowcontrol.NewResourceSliceWriteBufferForManager(mgr)

	recOpts.Manager = mgr
	recOpts.WriteBuffer = writeBuffer
	recOpts.Downstream = remoteConfig
	if migratingFieldManagers != "" {
		recOpts.MigratingFieldManagers = strings.Split(migratingFieldManagers, ",")
		for i := range recOpts.MigratingFieldManagers {
			recOpts.MigratingFieldManagers[i] = strings.TrimSpace(recOpts.MigratingFieldManagers[i])
		}
	}

	err = reconciliation.New(mgr, recOpts)
	if err != nil {
		return fmt.Errorf("constructing reconciliation controller: %w", err)
	}

	return mgr.Start(ctx)
}
