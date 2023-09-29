package main

import (
	"fmt"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/go-logr/zapr"
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/zap"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/conf"
	"github.com/Azure/eno/controllers/generation"
	"github.com/Azure/eno/controllers/reconciliation"
	"github.com/Azure/eno/controllers/status"
	"github.com/Azure/eno/controllers/statusagg"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func run() error {
	config := &conf.Config{}
	if err := envconfig.Process("", config); err != nil {
		panic(err)
	}

	zapLog, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}

	opts := ctrl.Options{
		Logger: zapr.NewLogger(zapLog),
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), opts)
	if err != nil {
		return fmt.Errorf("constructing controller manager: %w", err)
	}
	if err := mgr.AddHealthzCheck("running", healthz.Ping); err != nil {
		return fmt.Errorf("adding ping healthz check: %w", err)
	}
	if err := apiv1.SchemeBuilder.AddToScheme(mgr.GetScheme()); err != nil {
		return fmt.Errorf("adding scheme: %w", err)
	}
	if err := generation.NewController(mgr, config); err != nil {
		return fmt.Errorf("adding generation controller: %w", err)
	}
	if err := reconciliation.NewController(mgr, config); err != nil {
		return fmt.Errorf("adding reconciliation controller: %w", err)
	}
	if err := status.NewController(mgr, config); err != nil {
		return fmt.Errorf("adding status controller: %w", err)
	}
	if err := statusagg.NewController(mgr, config); err != nil {
		return fmt.Errorf("adding status aggregation controller: %w", err)
	}

	return mgr.Start(ctrl.SetupSignalHandler())
}
