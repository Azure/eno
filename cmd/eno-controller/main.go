package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	v1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/aggregation"
	"github.com/Azure/eno/internal/controllers/replication"
	"github.com/Azure/eno/internal/controllers/resourceslice"
	"github.com/Azure/eno/internal/controllers/scheduling"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/controllers/watch"
	"github.com/Azure/eno/internal/execution"
	"github.com/Azure/eno/internal/manager"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "install-executor" {
		installExecutor()
		return
	}
	if strings.HasSuffix(os.Args[0], "executor") {
		runExecutor()
		return
	}

	if err := runController(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func runController() error {
	ctx := ctrl.SetupSignalHandler()
	var (
		debugLogging             bool
		watchdogThres            time.Duration
		rolloutCooldown          time.Duration
		selfHealingGracePeriod   time.Duration
		taintToleration          string
		nodeAffinity             string
		concurrencyLimit         int
		containerCreationTimeout time.Duration
		synconf                  = &synthesis.Config{}

		mgrOpts = &manager.Options{
			Rest: ctrl.GetConfigOrDie(),
		}
	)
	flag.StringVar(&synconf.PodNamespace, "synthesizer-pod-namespace", os.Getenv("POD_NAMESPACE"), "Namespace to create synthesizer pods in. Defaults to POD_NAMESPACE.")
	flag.StringVar(&synconf.ExecutorImage, "executor-image", os.Getenv("EXECUTOR_IMAGE"), "Reference to the image that will be used to execute synthesizers. Defaults to EXECUTOR_IMAGE.")
	flag.StringVar(&synconf.PodServiceAccount, "synthesizer-pod-service-account", "", "Service account name to be assigned to synthesizer Pods.")
	flag.DurationVar(&containerCreationTimeout, "container-creation-ttl", time.Second*3, "Timeout when waiting for kubelet to ack scheduled pods. Protects tail latency from kubelet network partitions")
	flag.BoolVar(&debugLogging, "debug", true, "Enable debug logging")
	flag.DurationVar(&watchdogThres, "watchdog-threshold", time.Minute*3, "How long before the watchdog considers a mid-transition resource to be stuck")
	flag.DurationVar(&rolloutCooldown, "rollout-cooldown", time.Minute, "How long before an update to a related resource (synthesizer, bindings, etc.) will trigger a second composition's re-synthesis")
	flag.StringVar(&taintToleration, "taint-toleration", "", "Node NoSchedule taint to be tolerated by synthesizer pods e.g. taintKey=taintValue to match on value, just taintKey to match on presence of the taint")
	flag.StringVar(&nodeAffinity, "node-affinity", "", "Synthesizer pods will be created with this required node affinity expression e.g. labelKey=labelValue to match on value, just labelKey to match on presence of the label")
	flag.IntVar(&concurrencyLimit, "concurrency-limit", 10, "Upper bound on active syntheses. This effectively limits the number of running synthesizer pods spawned by Eno.")
	flag.DurationVar(&selfHealingGracePeriod, "self-healing-grace-period", time.Minute*5, "How long before the self-healing controllers are allowed to start the resynthesis process.")
	mgrOpts.Bind(flag.CommandLine)
	flag.Parse()

	synconf.NodeAffinityKey, synconf.NodeAffinityValue = parseKeyValue(nodeAffinity)
	synconf.TaintTolerationKey, synconf.TaintTolerationValue = parseKeyValue(taintToleration)

	if synconf.ExecutorImage == "" {
		return fmt.Errorf("a value is required in --executor-image or EXECUTOR_IMAGE")
	}
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

	err = synthesis.NewPodLifecycleController(mgr, synconf)
	if err != nil {
		return fmt.Errorf("constructing pod lifecycle controller: %w", err)
	}

	err = synthesis.NewSliceCleanupController(mgr)
	if err != nil {
		return fmt.Errorf("constructing resource slice cleanup controller: %w", err)
	}

	err = synthesis.NewPodGC(mgr, containerCreationTimeout)
	if err != nil {
		return fmt.Errorf("constructing pod garbage collector: %w", err)
	}

	err = replication.NewSymphonyController(mgr)
	if err != nil {
		return fmt.Errorf("constructing symphony replication controller: %w", err)
	}

	err = aggregation.NewSymphonyController(mgr)
	if err != nil {
		return fmt.Errorf("constructing symphony aggregation controller: %w", err)
	}

	err = aggregation.NewCompositionController(mgr)
	if err != nil {
		return fmt.Errorf("constructing composition status aggregation controller: %w", err)
	}

	err = resourceslice.NewController(mgr)
	if err != nil {
		return fmt.Errorf("constructing resource slice controller: %w", err)
	}

	err = watch.NewController(mgr)
	if err != nil {
		return fmt.Errorf("constructing watch controller: %w", err)
	}

	err = scheduling.NewController(mgr, concurrencyLimit, rolloutCooldown, watchdogThres)
	if err != nil {
		return fmt.Errorf("constructing synthesis scheduling controller: %w", err)
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

func installExecutor() {
	self := os.Args[0]
	file, err := os.Open(self)
	if err != nil {
		panic(err)
	}
	defer file.Close()

	dest, err := os.OpenFile("/eno/executor", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0777)
	if err != nil {
		panic(err)
	}
	defer dest.Close()

	_, err = io.Copy(dest, file)
	if err != nil {
		panic(err)
	}
}

func runExecutor() {
	rc := ctrl.GetConfigOrDie()
	rc.UserAgent = "eno-executor"

	zapCfg := zap.NewProductionConfig()
	zl, err := zapCfg.Build()
	if err != nil {
		panic(err)
	}
	logger := zapr.NewLogger(zl)
	ctx := logr.NewContext(ctrl.SetupSignalHandler(), logger)

	hc, err := rest.HTTPClientFor(rc)
	if err != nil {
		panic(err)
	}
	rm, err := apiutil.NewDynamicRESTMapper(rc, hc)
	if err != nil {
		panic(err)
	}

	scheme, err := v1.SchemeBuilder.Build()
	if err != nil {
		logger.Error(err, "building scheme")
		os.Exit(1)
	}
	client, err := client.New(rc, client.Options{
		Scheme: scheme,
		Mapper: rm,
	})
	if err != nil {
		logger.Error(err, "building client")
		os.Exit(1)
	}

	e := &execution.Executor{
		Reader:  client,
		Writer:  client,
		Handler: execution.NewExecHandler(),
	}
	err = e.Synthesize(ctx, execution.LoadEnv())
	if err != nil {
		logger.Error(err, "synthesizing")
		os.Exit(1)
	}
}
