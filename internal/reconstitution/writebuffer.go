package reconstitution

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type writeBuffer struct {
	*reconstituter
	Client client.Client
}

func (w *writeBuffer) Start(ctx context.Context) error {
	return nil
}

func (w *writeBuffer) MarkResourceSynced(ctx context.Context, req *Request, gen int64) error {
	// TODO
	return nil
}
