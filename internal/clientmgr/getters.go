package clientmgr

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

func GetSecretConfigGetter(cli client.Client) ConfigGetter[*apiv1.SecretKeyRef] {
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

		data := secret.Data["value"]
		if data == nil {
			return nil, errors.New("secret does not contain kubeconfig data")
		}

		return clientcmd.RESTConfigFromKubeConfig(data)
	}
}
