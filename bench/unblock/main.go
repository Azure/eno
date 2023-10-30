package main

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/flowcontrol"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

func main() {
	rc := ctrl.GetConfigOrDie()
	rc.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(100, 5)

	scheme := runtime.NewScheme()
	if err := apiv1.SchemeBuilder.AddToScheme(scheme); err != nil {
		panic(err)
	}

	cli, err := client.New(rc, client.Options{Scheme: scheme})
	if err != nil {
		panic(err)
	}

	ctx := context.Background()

	list := &apiv1.GeneratedResourceList{}
	if err := cli.List(ctx, list); err != nil {
		panic(err)
	}

	items := make(chan *apiv1.GeneratedResource)
	for i := 0; i < 32; i++ {
		go func() {
			for item := range items {
				item.Finalizers = nil
				if err := cli.Update(ctx, item); err != nil {
					panic(err)
				}
				println("hit")
			}
		}()
	}

	for _, item := range list.Items {
		i := item
		items <- &i
	}
}
