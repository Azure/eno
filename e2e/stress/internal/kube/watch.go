// Restartable watch support for live-cluster measurements.
package kube

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
)

type WatchHandler func(*unstructured.Unstructured, time.Time)

func (c *Client) Watch(ctx context.Context, gvr schema.GroupVersionResource, namespace, labelSelector string, ready chan<- struct{}, handler WatchHandler) error {
	var signalReady = ready
	for ctx.Err() == nil {
		resource := c.Dynamic.Resource(gvr)
		var resourceInterface dynamic.ResourceInterface = resource
		if namespace != "" {
			resourceInterface = resource.Namespace(namespace)
		}
		list, err := resourceInterface.List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
		if err != nil {
			if !waitForRetry(ctx) {
				break
			}
			continue
		}
		observedAt := time.Now()
		for index := range list.Items {
			handler(&list.Items[index], observedAt)
		}

		stream, err := resourceInterface.Watch(ctx, metav1.ListOptions{
			LabelSelector:   labelSelector,
			ResourceVersion: list.GetResourceVersion(),
		})
		if err != nil {
			if !waitForRetry(ctx) {
				break
			}
			continue
		}
		if signalReady != nil {
			close(signalReady)
			signalReady = nil
		}
		watchEnded := consumeWatch(ctx, stream, handler)
		stream.Stop()
		if !watchEnded {
			break
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("watch for %s stopped", gvr.String())
}

func consumeWatch(ctx context.Context, stream watch.Interface, handler WatchHandler) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case event, open := <-stream.ResultChan():
			if !open {
				return true
			}
			if event.Type == watch.Error {
				return true
			}
			object, ok := event.Object.(*unstructured.Unstructured)
			if ok {
				handler(object, time.Now())
			}
		}
	}
}

func waitForRetry(ctx context.Context) bool {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
