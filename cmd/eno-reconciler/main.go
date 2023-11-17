package main

import (
	"fmt"
	"os"
	"time"

	"net/http"
	_ "net/http/pprof"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/flowcontrol"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/go-logr/zapr"
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/sync"
	"github.com/Azure/eno/internal/reconstitution"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func run() error {
	go func() {
		// For pprof
		panic(http.ListenAndServe(":6060", nil)) // TODO: Disable by default
	}()

	config := &Config{}
	if err := envconfig.Process("", config); err != nil {
		panic(err)
	}

	// TODO: Production ready logging
	zapLog, err := zap.NewDevelopment(zap.IncreaseLevel(zapcore.DebugLevel))
	if err != nil {
		panic(err)
	}

	opts := ctrl.Options{
		Logger: zapr.NewLogger(zapLog),
	}
	if config.Namespace != "" {
		opts.Cache.DefaultNamespaces = map[string]cache.Config{
			config.Namespace: {
				LabelSelector: labels.Everything(),
				FieldSelector: fields.ParseSelectorOrDie(fmt.Sprintf("metadata.namespace=%s", config.Namespace)),
			},
		}
	}

	rc := ctrl.GetConfigOrDie()
	rc.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(100, 5) // TODO

	mgr, err := ctrl.NewManager(rc, opts)
	if err != nil {
		return fmt.Errorf("constructing controller manager: %w", err)
	}
	if err := apiv1.SchemeBuilder.AddToScheme(mgr.GetScheme()); err != nil {
		return fmt.Errorf("adding scheme: %w", err)
	}
	if err := mgr.AddHealthzCheck("running", healthz.Ping); err != nil {
		return fmt.Errorf("adding ping healthz check: %w", err)
	}

	reMgr, err := reconstitution.New(mgr, config.WriteBatchInterval)
	if err != nil {
		return err
	}
	if err := sync.New(reMgr, nil); err != nil { // TODO
		return err
	}

	return mgr.Start(ctrl.SetupSignalHandler())
}

type Config struct {
	Namespace          string        `split_words:"true" default:""`
	WriteBatchInterval time.Duration `split_words:"true" default:"2s"`
}
