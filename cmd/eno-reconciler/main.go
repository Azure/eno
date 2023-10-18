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
	"github.com/Azure/eno/internal/clientmgr"
	"github.com/Azure/eno/internal/conf"
	"github.com/Azure/eno/internal/controllers/readiness"
	"github.com/Azure/eno/internal/controllers/reconciliation"
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

	rc := ctrl.GetConfigOrDie()

	mgr, err := ctrl.NewManager(rc, opts)
	if err != nil {
		return fmt.Errorf("constructing controller manager: %w", err)
	}
	if err := apiv1.SchemeBuilder.AddToScheme(mgr.GetScheme()); err != nil {
		return fmt.Errorf("adding scheme: %w", err)
	}
	cli := mgr.GetClient()
	cmgr := clientmgr.New(cli, clientmgr.GetSecretConfigGetter(cli))

	if err := mgr.AddHealthzCheck("running", healthz.Ping); err != nil {
		return fmt.Errorf("adding ping healthz check: %w", err)
	}
	if err := reconciliation.NewController(mgr, cmgr, config); err != nil {
		return err
	}
	if err := readiness.NewController(mgr, cmgr, config); err != nil {
		return fmt.Errorf("adding readiness controller: %w", err)
	}

	return mgr.Start(ctrl.SetupSignalHandler())
}
