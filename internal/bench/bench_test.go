package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

type resourceVersionClient struct {
	rest       *rest.Config
	http       *http.Client
	serializer serializer.CodecFactory
	scheme     *runtime.Scheme
}

func newResourceVersionClient() *resourceVersionClient {
	rc := ctrl.GetConfigOrDie()
	rc.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(50000, 100)

	hc, err := rest.HTTPClientFor(rc)
	if err != nil {
		panic(err)
	}

	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)

	return &resourceVersionClient{
		rest:       rc,
		http:       hc,
		serializer: serializer.NewCodecFactory(scheme),
		scheme:     scheme,
	}
}

var matcher = regexp.MustCompile(`"resourceVersion"\s*:\s*"(\d+)"`)

func (c *resourceVersionClient) Get(ctx context.Context, obj client.Object) (int64, error) {
	start := time.Now()

	gvk, err := apiutil.GVKForObject(obj, c.scheme)
	if err != nil {
		return 0, err
	}

	client, err := apiutil.RESTClientForGVK(gvk, true, c.rest, c.serializer, c.http)
	if err != nil {
		return 0, err
	}

	reader, err := client.Get().
		Namespace(obj.GetNamespace()). // TODO: Discover this
		Resource("configmaps").        // TODO: And this
		Name(obj.GetName()).
		Stream(ctx)
	if err != nil {
		return 0, err
	}

	buf := make([]byte, 512) // assume resource version is in first 0.5kb
	_, err = reader.Read(buf)
	if err != nil {
		reader.Close()
		return 0, err
	}
	reader.Close()

	match := matcher.FindSubmatch(buf)
	if len(match) < 2 {
		return 0, fmt.Errorf("no resource version found")
	}

	version, _ := strconv.ParseInt(string(match[1]), 10, 0)

	latency := time.Since(start)
	log.Printf("latency: %s, version: %s", latency, match[1])
	return version, nil
}

func BenchmarkFastClient(b *testing.B) {
	b.SetParallelism(8)
	c := newResourceVersionClient()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			obj := &corev1.ConfigMap{}
			obj.Name = "test"
			obj.Namespace = "default"

			_, err := c.Get(context.TODO(), obj)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkSlowClient(b *testing.B) {
	b.SetParallelism(8)

	rc := ctrl.GetConfigOrDie()
	rc.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(50000, 100)

	c, err := client.New(rc, client.Options{})
	if err != nil {
		b.Fatal(err)
	}

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			obj := &corev1.ConfigMap{}
			obj.Name = "test"
			obj.Namespace = "default"

			err := c.Get(context.TODO(), client.ObjectKeyFromObject(obj), obj)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
