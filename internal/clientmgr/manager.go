package clientmgr

import (
	"context"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ConfigGetter[T comparable] func(context.Context, T) (*rest.Config, error)

type Manager[T comparable] struct {
	defaultClient client.Client
	confGetter    ConfigGetter[T]
}

func New[T comparable](defaultClient client.Client, cg ConfigGetter[T]) *Manager[T] {
	return &Manager[T]{defaultClient: defaultClient, confGetter: cg}
}

func (m *Manager[T]) GetClient(ctx context.Context, key T) (client.Client, error) {
	// TODO(mariano): Cache rest configs?
	rc, err := m.confGetter(ctx, key)
	if err != nil {
		return nil, err
	}

	// TODO(jordan): Add support for pooled external clients

	var cli client.Client
	if rc == nil {
		cli = m.defaultClient
	} else {
		cli, err = client.New(rc, client.Options{})
		if err != nil {
			return nil, err
		}
	}

	return cli, nil
}
