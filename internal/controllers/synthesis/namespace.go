package synthesis

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func compositionNamespaceUnavailable(ctx context.Context, reader client.Reader, name string) (bool, error) {
	ns := &corev1.Namespace{}
	err := reader.Get(ctx, client.ObjectKey{Name: name}, ns)
	if errors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return ns.DeletionTimestamp != nil, nil
}
