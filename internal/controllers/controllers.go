package controllers

import (
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/Azure/eno/conf"
	"github.com/Azure/eno/internal/clientmgr"
	"github.com/Azure/eno/internal/controllers/generation"
	"github.com/Azure/eno/internal/controllers/reconciliation"
	"github.com/Azure/eno/internal/controllers/status"
	"github.com/Azure/eno/internal/controllers/statusagg"
)

func New(mgr ctrl.Manager, cmgr *clientmgr.Manager[string], config *conf.Config) error {
	if err := generation.NewController(mgr, config); err != nil {
		return fmt.Errorf("adding generation controller: %w", err)
	}
	if err := reconciliation.NewController(mgr, cmgr, config); err != nil {
		return fmt.Errorf("adding reconciliation controller: %w", err)
	}
	if err := status.NewController(mgr, cmgr, config); err != nil {
		return fmt.Errorf("adding status controller: %w", err)
	}
	if err := statusagg.NewController(mgr, config); err != nil {
		return fmt.Errorf("adding status aggregation controller: %w", err)
	}
	return nil
}
