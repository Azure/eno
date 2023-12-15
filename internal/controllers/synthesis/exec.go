package synthesis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/exec"
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
	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace)

	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting synthesizer: %w", err)
	}
	logger = logger.WithValues("synthesizerName", syn.Name)
	ctx = logr.NewContext(ctx, logger)

	if compGen < comp.Generation {
		return ctrl.Result{}, nil // old pod - don't bother synthesizing. The lifecycle controller will delete it
	}

	refs, err := c.synthesize(ctx, syn, comp, pod)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("executing synthesizer: %w", err)
	}

	err = c.writeSuccessStatus(ctx, comp, compGen, refs)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("updating composition status: %w", err)
	}

	return ctrl.Result{}, nil
}

func (c *execController) synthesize(ctx context.Context, syn *apiv1.Synthesizer, comp *apiv1.Composition, pod *corev1.Pod) ([]*apiv1.ResourceSliceRef, error) {
	logger := logr.FromContextOrDiscard(ctx)

	inputsJson, err := c.buildInputsJson(ctx, comp)
	if err != nil {
		return nil, fmt.Errorf("building inputs: %w", err)
	}

	logger.V(1).Info("starting up the synthesizer")
	req := c.execClient.
		Post().
		Namespace(pod.Namespace).
		Resource("pods").
		Name(pod.Name).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "synthesizer",
			Command:   syn.Spec.Command,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, runtime.NewParameterCodec(c.scheme))

	executor, err := remotecommand.NewSPDYExecutor(c.restConfig, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("creating remote command executor: %w", err)
	}

	streamCtx, cancel := context.WithTimeout(ctx, syn.Spec.Timeout.Duration)
	defer cancel()

	stderr := &bytes.Buffer{}
	stdout := &bytes.Buffer{}
	err = executor.StreamWithContext(streamCtx, remotecommand.StreamOptions{
		Stdin:  bytes.NewBuffer(append(inputsJson, '\x00')),
		Stdout: stdout,
		Stderr: stderr,
	})
	if err != nil {
		e := &exec.CodeExitError{}
		// TODO: How to test? Publish events? Status?
		if errors.As(err, e) {
			msg := truncateString(strings.TrimSpace(stderr.String()), 256)
			return nil, fmt.Errorf("command exited with status %d - stderr: %s", e.Code, msg)
		}
		return nil, fmt.Errorf("starting stream: %w", err)
	}

	return c.writeOutputToSlices(ctx, comp, stdout)
}

func (c *execController) buildInputsJson(ctx context.Context, comp *apiv1.Composition) ([]byte, error) {
	logger := logr.FromContextOrDiscard(ctx)

	inputs := make([]*unstructured.Unstructured, len(comp.Spec.Inputs))
	for i, ref := range comp.Spec.Inputs {
		if ref.Resource == nil {
			continue // not a k8s resource
		}
		input := &unstructured.Unstructured{}
		input.SetName(ref.Resource.Name)
		input.SetNamespace(ref.Resource.Namespace)
		input.SetKind(ref.Resource.Kind)
		input.SetAPIVersion(ref.Resource.APIVersion)
		appendInputNameAnnotation(&ref, input)

		// TODO: Cache inputs?

		start := time.Now()
		err := c.client.Get(ctx, client.ObjectKeyFromObject(input), input)
		if err != nil {
			// TODO: Handle 40x carefully
			return nil, fmt.Errorf("getting resource %s/%s: %w", input.GetKind(), input.GetName(), err)
		}

		logger.V(1).Info("retrieved input resource", "resourceName", input.GetName(), "resourceNamespace", input.GetNamespace(), "resourceKind", input.GetKind(), "latency", time.Since(start).Milliseconds())
		inputs[i] = input
	}

	return json.Marshal(&inputs)
}

func (c *execController) writeOutputToSlices(ctx context.Context, comp *apiv1.Composition, stdout io.Reader) ([]*apiv1.ResourceSliceRef, error) {
	logger := logr.FromContextOrDiscard(ctx)

	slice := &apiv1.ResourceSlice{}
	slice.GenerateName = comp.Name + "-"
	slice.Namespace = comp.Namespace
	slices := []*apiv1.ResourceSlice{slice} // TODO: Paginate slices

	outputs := []*unstructured.Unstructured{}
	err := json.NewDecoder(stdout).Decode(&outputs)
	if err != nil {
		return nil, fmt.Errorf("parsing outputs: %w", err)
	}
	for _, output := range outputs {
		js, err := output.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("encoding outputs: %w", err)
		}
		slice.Spec.Resources = append(slice.Spec.Resources, apiv1.Manifest{Manifest: string(js)}) // TODO: Set reconcile interval
	}

	sliceRefs := make([]*apiv1.ResourceSliceRef, len(slices))
	for i, slice := range slices { // TODO: Maybe don't buffer?
		start := time.Now()
		err = c.client.Create(ctx, slice)
		if err != nil {
			// TODO: Retries? Separate rate limit?
			return nil, fmt.Errorf("creating resource slice %d: %w", i, err)
		}
		logger.V(1).Info("wrote resource slice", "resourceSliceName", slice.Name, "latency", time.Since(start).Milliseconds())
		sliceRefs[i] = &apiv1.ResourceSliceRef{Name: slice.Name}
	}

	return sliceRefs, nil
}

func (c *execController) writeSuccessStatus(ctx context.Context, comp *apiv1.Composition, compGen int64, refs []*apiv1.ResourceSliceRef) error {
	logger := logr.FromContextOrDiscard(ctx)
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		err := c.client.Get(ctx, client.ObjectKeyFromObject(comp), comp)
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
		comp.Status.CurrentState.ResourceSlices = refs

		err = c.client.Status().Update(ctx, comp)
		if err != nil {
			return err
		}

		logger.V(1).Info("finished synthesizing the composition")
		return nil
	})
}

func appendInputNameAnnotation(ref *apiv1.InputRef, input *unstructured.Unstructured) {
	anno := input.GetAnnotations()
	if anno == nil {
		anno = map[string]string{}
	}
	anno["eno.azure.io/input-name"] = ref.Name
	input.SetAnnotations(anno)
}

func truncateString(str string, length int) (out string) {
	if length <= 0 {
		return ""
	}

	count := 0
	for _, char := range str {
		out += string(char)
		count++
		if count >= length {
			break
		}
	}
	return out + "[truncated]"
}
