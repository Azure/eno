package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/go-logr/zapr"
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/zap"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/clientmgr"
	"github.com/Azure/eno/internal/conf"
	"github.com/Azure/eno/internal/controllers"
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
	cmgr := clientmgr.New(cli, getRestConfigGetter(cli))
	if err := mgr.AddHealthzCheck("running", healthz.Ping); err != nil {
		return fmt.Errorf("adding ping healthz check: %w", err)
	}
	if err := controllers.New(mgr, cmgr, config); err != nil {
		return err
	}

	return mgr.Start(ctrl.SetupSignalHandler())
}

func getRestConfigGetter(cli client.Client) clientmgr.ConfigGetter[*apiv1.SecretKeyRef] {
	return func(ctx context.Context, secretRef *apiv1.SecretKeyRef) (*rest.Config, error) {
		if secretRef == nil {
			return nil, nil
		}

		secret := &corev1.Secret{}
		secret.Name = secretRef.Name
		secret.Namespace = secretRef.Namespace
		err := cli.Get(ctx, client.ObjectKeyFromObject(secret), secret)
		if err != nil {
			return nil, err
		}

		var data []byte
		if secretRef.Key != "" {
			data = secret.Data[secretRef.Key]
		} else {
			data = secret.Data["value"]
		}

		if data != nil {
			return nil, errors.New("secret does not contain kubeconfig data")
		}

		return clientcmd.RESTConfigFromKubeConfig(data)
	}
}
