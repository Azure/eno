package controllers

import (
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/clientmgr"
	"github.com/Azure/eno/internal/conf"
	"github.com/Azure/eno/internal/controllers/generation"
	"github.com/Azure/eno/internal/controllers/readiness"
	"github.com/Azure/eno/internal/controllers/reconciliation"
	"github.com/Azure/eno/internal/controllers/statusagg"
)

func New(mgr ctrl.Manager, cmgr *clientmgr.Manager[*apiv1.SecretKeyRef], config *conf.Config) error {
	if err := generation.NewController(mgr, config); err != nil {
		return fmt.Errorf("adding generation controller: %w", err)
	}
	if err := reconciliation.NewController(mgr, cmgr, config); err != nil {
		return fmt.Errorf("adding reconciliation controller: %w", err)
	}
	if err := readiness.NewController(mgr, cmgr, config); err != nil {
		return fmt.Errorf("adding readiness controller: %w", err)
	}
	if err := statusagg.NewController(mgr, config); err != nil {
		return fmt.Errorf("adding status aggregation controller: %w", err)
	}
	return nil
}
