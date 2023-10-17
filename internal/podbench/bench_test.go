package main

import (
	"context"
	"log"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/flowcontrol"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func BenchmarkSlowClient(b *testing.B) {
	b.SetParallelism(16)

	rc := ctrl.GetConfigOrDie()
	rc.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(50000, 100)

	c, err := client.New(rc, client.Options{})
	if err != nil {
		b.Fatal(err)
	}

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			obj := &corev1.Pod{}
			obj.GenerateName = "test"
			obj.Namespace = "default"
			obj.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
			obj.Spec.Containers = []corev1.Container{{
				Name:            "test",
				Image:           "nginx",
				ImagePullPolicy: corev1.PullNever,
				Command:         []string{"/bin/sh", "-c", "echo hello world"},
			}}

			err := c.Create(context.TODO(), obj)
			if err != nil {
				b.Fatal(err)
			}
			log.Printf("created pod %s", obj.Name)

			readyTime := waitForRunning(c, obj.Name)
			log.Printf("pod %s became ready in %dms or %dms by wallclock time", obj.Name, readyTime.Sub(obj.CreationTimestamp.Time).Milliseconds(), time.Since(obj.CreationTimestamp.Time).Milliseconds())

			err = c.Delete(context.TODO(), obj)
			if err != nil {
				b.Fatal(err)
			}
			log.Printf("deleted pod %s", obj.Name)
		}
	})
}

func waitForRunning(c client.Client, name string) time.Time {
	current := &corev1.Pod{}
	current.Name = name
	current.Namespace = "default"

	for {
		err := c.Get(context.TODO(), client.ObjectKeyFromObject(current), current)
		if err != nil {
			panic(err)
		}

		if t, ok := checkContainerStatus(current); ok {
			return t
		}

		time.Sleep(time.Millisecond * 10)
	}
}

func checkContainerStatus(current *corev1.Pod) (time.Time, bool) {
	for _, cont := range current.Status.ContainerStatuses {
		if cont.State.Terminated != nil && cont.State.Terminated.Reason == "Completed" {
			return cont.State.Terminated.FinishedAt.Time, true
		}
	}
	return time.Time{}, false
}
