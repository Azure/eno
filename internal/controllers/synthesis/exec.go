package synthesis

import (
	"bytes"
	"context"
	"fmt"
	"strconv"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

type execController struct {
	client     client.Client
	execClient rest.Interface
	scheme     *runtime.Scheme
	restConfig *rest.Config
}

func NewExecController(mgr ctrl.Manager) error {
	gvk := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}
	execClient, err := apiutil.RESTClientForGVK(gvk, false, mgr.GetConfig(), serializer.NewCodecFactory(mgr.GetScheme()), mgr.GetHTTPClient())
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "execController")).
		Complete(&execController{
			client:     mgr.GetClient(),
			execClient: execClient,
			scheme:     mgr.GetScheme(),
			restConfig: mgr.GetConfig(),
		})
}

func (c *execController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	pod := &corev1.Pod{}
	err := c.client.Get(ctx, req.NamespacedName, pod)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("gettting pod: %w", err))
	}
	if len(pod.OwnerReferences) == 0 || pod.OwnerReferences[0].Kind != "Composition" {
		// This shouldn't be common as the informer watch filters on Eno-managed pods using a selector
		return ctrl.Result{}, nil
	}
	if len(pod.Status.ContainerStatuses) == 0 || pod.Status.ContainerStatuses[0].State.Running == nil {
		return ctrl.Result{}, nil // pod isn't ready for exec
	}
	compGen, _ := strconv.ParseInt(pod.Annotations["eno.azure.io/composition-generation"], 10, 0)
	synGen, _ := strconv.ParseInt(pod.Annotations["eno.azure.io/synthesizer-generation"], 10, 0)
	logger = logger.WithValues("compositionGeneration", compGen, "synthesizerGeneration", synGen, "podName", pod.Name)

	comp := &apiv1.Composition{}
	comp.Name = pod.OwnerReferences[0].Name
	comp.Namespace = pod.Namespace
	err = c.client.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting composition resource: %w", err))
	}
	if comp.Status.CurrentState == nil || comp.Status.CurrentState.Synthesized {
		// TODO: Do we need to watch syntheses?
		// TODO: We need to be smarter about synth versions here
		return ctrl.Result{}, nil // nothing to do!
	}
	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace)

	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting synthesizer: %w", err)
	}
	logger = logger.WithValues("synthesizerName", syn.Name)
	ctx = logr.NewContext(ctx, logger)

	if compGen < comp.Generation { // TODO: Remove syn other places too?
		return ctrl.Result{}, nil // old pod - don't bother synthesizing
	}

	err = c.exec(ctx, syn, comp, pod)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("executing synthesizer: %w", err)
	}

	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		err = c.client.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if err != nil {
			return err
		}

		if compGen < comp.Generation {
			logger.V(1).Info("synthesis is no longer relevant - discarding its output")
			return nil
		}

		if comp.Status.CurrentState == nil {
			comp.Status.CurrentState = &apiv1.Synthesis{}
		}
		if comp.Status.CurrentState.Synthesized {
			return nil // no updates needed
		}
		comp.Status.CurrentState.Synthesized = true
		// TODO: Also update slice refs

		err = c.client.Status().Update(ctx, comp)
		if err != nil {
			return err
		}

		logger.V(1).Info("finished synthesizing the composition")
		return nil
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("updating composition status: %w", err)
	}

	return ctrl.Result{}, nil
}

func (c *execController) exec(ctx context.Context, syn *apiv1.Synthesizer, comp *apiv1.Composition, pod *corev1.Pod) error {
	logger := logr.FromContextOrDiscard(ctx)
	logger.V(1).Info("starting up the synthesizer")

	// TODO: Timeout

	req := c.execClient.
		Post().
		Namespace(pod.Namespace).
		Resource("pods").
		Name(pod.Name).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "synthesizer",
			Command:   []string{"/bin/sh", "-c", "sleep 1 && echo done!"}, // TODO
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, runtime.NewParameterCodec(c.scheme))

	stdin := bytes.NewBufferString("\n\n")
	stdout := &bytes.Buffer{} // TODO: Buffer?
	stderr := &bytes.Buffer{}

	exec, err := remotecommand.NewSPDYExecutor(c.restConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("creating remote command executor: %w", err)
	}
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Tty:    true,
	})
	if err != nil {
		return fmt.Errorf("starting stream: %w", err)
	}

	return nil
}
