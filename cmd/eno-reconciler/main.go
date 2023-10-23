package main

import (
	"fmt"
	"math/rand"
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

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/conf"
	"github.com/Azure/eno/internal/controllers/readiness"
	"github.com/Azure/eno/internal/controllers/reconciliation"
)

// TODO: Separate config package per process — reconciler doesn't need wrapper tag

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func run() error {
	rand.Seed(time.Now().UnixNano())

	go func() {
		// For pprof
		panic(http.ListenAndServe(":6060", nil)) // TODO: Disable by default
	}()

	config := &conf.Config{}
	if err := envconfig.Process("", config); err != nil {
		panic(err)
	}

	zapLog, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}

	ok := true
	opts := ctrl.Options{
		Logger: zapr.NewLogger(zapLog),
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				config.Namespace: {
					LabelSelector:         labels.Everything(),
					FieldSelector:         fields.Everything(),
					Transform:             func(in interface{}) (interface{}, error) { return in, nil },
					UnsafeDisableDeepCopy: &ok, // TODO: Make sure the controller honors this
				},
			},
		},
	}

	rc := ctrl.GetConfigOrDie()
	rc.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(100, 5)

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
	if err := reconciliation.NewController(mgr, config); err != nil {
		return err
	}
	if err := readiness.NewController(mgr, config); err != nil {
		return fmt.Errorf("adding readiness controller: %w", err)
	}

	return mgr.Start(ctrl.SetupSignalHandler())
}
