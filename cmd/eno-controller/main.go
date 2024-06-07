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
	"github.com/Azure/eno/internal/controllers/rollout"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/controllers/watchdog"
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
		debugLogging    bool
		watchdogThres   time.Duration
		rolloutCooldown time.Duration
		taintToleration string
		nodeAffinity    string
		synconf         = &synthesis.Config{}

		mgrOpts = &manager.Options{
			Rest: ctrl.GetConfigOrDie(),
		}
	)
	flag.Float64Var(&synconf.SliceCreationQPS, "slice-creation-qps", 5, "Max QPS for writing synthesized resources into resource slices")
	flag.StringVar(&synconf.PodNamespace, "synthesizer-pod-namespace", os.Getenv("POD_NAMESPACE"), "Namespace to create synthesizer pods in. Defaults to POD_NAMESPACE.")
	flag.StringVar(&synconf.ExecutorImage, "executor-image", os.Getenv("EXECUTOR_IMAGE"), "Reference to the image that will be used to execute synthesizers. Defaults to EXECUTOR_IMAGE.")
	flag.StringVar(&synconf.PodServiceAccount, "synthesizer-pod-service-account", "", "Service account name to be assigned to synthesizer Pods.")
	flag.BoolVar(&debugLogging, "debug", true, "Enable debug logging")
	flag.DurationVar(&watchdogThres, "watchdog-threshold", time.Minute*5, "How long before the watchdog considers a mid-transition resource to be stuck")
	flag.DurationVar(&rolloutCooldown, "rollout-cooldown", time.Second*2, "How long before an update to related resource (synthesizer, bindings, etc.) will trigger a composition's re-synthesis")
	flag.StringVar(&taintToleration, "taint-toleration", "", "Node NoSchedule taint to be tolerated by synthesizer pods e.g. taintKey=taintValue to match on value, just taintKey to match on presence of the taint")
	flag.StringVar(&nodeAffinity, "node-affinity", "", "Synthesizer pods will be created with this required node affinity expression e.g. labelKey=labelValue to match on value, just labelKey to match on presence of the label")
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

	err = rollout.NewController(mgr, rolloutCooldown)
	if err != nil {
		return fmt.Errorf("constructing rollout controller: %w", err)
	}

	err = synthesis.NewPodLifecycleController(mgr, synconf)
	if err != nil {
		return fmt.Errorf("constructing pod lifecycle controller: %w", err)
	}

	err = watchdog.NewController(mgr, watchdogThres)
	if err != nil {
		return fmt.Errorf("constructing watchdog controller: %w", err)
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
