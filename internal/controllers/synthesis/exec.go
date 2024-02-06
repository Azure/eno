package synthesis

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// maxSliceJsonBytes is the max sum of a resource slice's manifests. It's set to 1mb, which leaves 512kb of space for the resource's status, encoding overhead, etc.
const maxSliceJsonBytes = 1024 * 768

type execController struct {
	client  client.Client
	timeout time.Duration
	conn    SynthesizerConnection
}

func NewExecController(mgr ctrl.Manager, timeout time.Duration, conn SynthesizerConnection) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "execController")).
		Complete(&execController{
			client:  mgr.GetClient(),
			timeout: timeout,
			conn:    conn,
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

	synctx, done := context.WithTimeout(ctx, c.timeout)
	defer done()

	start := time.Now()
	stdout, err := c.conn.Synthesize(synctx, syn, pod, inputsJson)
	if err != nil {
		return nil, err
	}
	logger.V(1).Info("synthesizing is done", "latency", time.Since(start).Milliseconds())

	return c.writeOutputToSlices(ctx, comp, stdout)
}

func (c *execController) buildInputsJson(ctx context.Context, comp *apiv1.Composition) ([]byte, error) {
	logger := logr.FromContextOrDiscard(ctx)

	var inputs []*unstructured.Unstructured
	for _, ref := range comp.Spec.Inputs {
		if ref.Resource == nil {
			continue // not a k8s resource
		}
		input := &unstructured.Unstructured{}
		input.SetName(ref.Resource.Name)
		input.SetNamespace(ref.Resource.Namespace)
		input.SetKind(ref.Resource.Kind)
		input.SetAPIVersion(ref.Resource.APIVersion)
		appendInputNameAnnotation(&ref, input)

		start := time.Now()
		err := c.client.Get(ctx, client.ObjectKeyFromObject(input), input)
		if err != nil {
			// Ideally we could stop retrying eventually here in cases where the resource doesn't exist,
			// but it isn't safe to _never_ retry (informers across types aren't ordered), and controller-runtime
			// doesn't expose the retry count.
			return nil, fmt.Errorf("getting resource %s/%s: %w", input.GetKind(), input.GetName(), err)
		}

		logger.V(1).Info("retrieved input resource", "resourceName", input.GetName(), "resourceNamespace", input.GetNamespace(), "resourceKind", input.GetKind(), "latency", time.Since(start).Milliseconds())
		inputs = append(inputs, input)
	}

	js, err := json.Marshal(&inputs)
	if err != nil {
		return nil, reconcile.TerminalError(err)
	}
	return js, nil
}

func (c *execController) writeOutputToSlices(ctx context.Context, comp *apiv1.Composition, stdout io.Reader) ([]*apiv1.ResourceSliceRef, error) {
	logger := logr.FromContextOrDiscard(ctx)

	outputs := []*unstructured.Unstructured{}
	err := json.NewDecoder(stdout).Decode(&outputs)
	if err != nil {
		return nil, reconcile.TerminalError(fmt.Errorf("parsing outputs: %w", err))
	}

	previous, err := c.fetchPreviousSlices(ctx, comp)
	if err != nil {
		return nil, err
	}

	slices, err := buildResourceSlices(comp, previous, outputs, maxSliceJsonBytes)
	if err != nil {
		return nil, err
	}

	sliceRefs := make([]*apiv1.ResourceSliceRef, len(slices))
	for i, slice := range slices {
		start := time.Now()

		err = c.writeResourceSlice(ctx, slice)
		if err != nil {
			return nil, fmt.Errorf("creating resource slice %d: %w", i, err)
		}

		logger.V(1).Info("wrote resource slice", "resourceSliceName", slice.Name, "latency", time.Since(start).Milliseconds())
		sliceRefs[i] = &apiv1.ResourceSliceRef{Name: slice.Name}
	}

	return sliceRefs, nil
}

func (c *execController) fetchPreviousSlices(ctx context.Context, comp *apiv1.Composition) ([]*apiv1.ResourceSlice, error) {
	if comp.Status.PreviousState == nil {
		return nil, nil // nothing to fetch
	}
	logger := logr.FromContextOrDiscard(ctx)

	slices := []*apiv1.ResourceSlice{}
	for _, ref := range comp.Status.PreviousState.ResourceSlices {
		slice := &apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := c.client.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		if errors.IsNotFound(err) {
			logger.Info("resource slice referenced by composition was not found - skipping", "resourceSliceName", slice.Name)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("fetching current resource slice %q: %w", slice.Name, err)
		}
		slices = append(slices, slice)
	}

	return slices, nil
}

func buildResourceSlices(comp *apiv1.Composition, previous []*apiv1.ResourceSlice, outputs []*unstructured.Unstructured, maxJsonBytes int) ([]*apiv1.ResourceSlice, error) {
	// Encode the given resources into manifest structs
	refs := map[resourceRef]struct{}{}
	manifests := []apiv1.Manifest{}
	for i, output := range outputs {
		reconcileInterval := consumeReconcileIntervalAnnotation(output)
		js, err := output.MarshalJSON()
		if err != nil {
			return nil, reconcile.TerminalError(fmt.Errorf("encoding output %d: %w", i, err))
		}
		manifests = append(manifests, apiv1.Manifest{
			Manifest:          string(js),
			ReconcileInterval: reconcileInterval,
		})
		refs[newResourceRef(output)] = struct{}{}
	}

	// Build tombstones by diffing the new state against the current state
	// Existing tombstones are passed down if they haven't yet been reconciled to avoid orphaning resources
	for _, slice := range previous {
		for i, res := range slice.Spec.Resources {
			res := res
			obj := &unstructured.Unstructured{}
			err := obj.UnmarshalJSON([]byte(res.Manifest))
			if err != nil {
				return nil, reconcile.TerminalError(fmt.Errorf("decoding resource %d of slice %s: %w", i, slice.Name, err))
			}

			if _, ok := refs[newResourceRef(obj)]; ok || (res.Deleted && slice.Status.Resources != nil && slice.Status.Resources[i].Reconciled) {
				continue // still exists or has already been deleted
			}

			res.Deleted = true
			manifests = append(manifests, res)
		}
	}

	// Build the slice resources
	var (
		slices             []*apiv1.ResourceSlice
		sliceBytes         int
		slice              *apiv1.ResourceSlice
		blockOwnerDeletion = true
	)
	for _, manifest := range manifests {
		if slice == nil || sliceBytes >= maxJsonBytes {
			sliceBytes = 0
			slice = &apiv1.ResourceSlice{}
			slice.GenerateName = comp.Name + "-"
			slice.Namespace = comp.Namespace
			// TODO: slice.Finalizers = []string{"eno.azure.io/cleanup"}
			slice.OwnerReferences = []metav1.OwnerReference{{
				APIVersion:         apiv1.SchemeGroupVersion.Identifier(),
				Kind:               "Composition",
				Name:               comp.Name,
				UID:                comp.UID,
				BlockOwnerDeletion: &blockOwnerDeletion, // we need the composition in order to successfully delete its resource slices
				Controller:         &blockOwnerDeletion,
			}}
			slices = append(slices, slice)
		}
		sliceBytes += len(manifest.Manifest)
		slice.Spec.Resources = append(slice.Spec.Resources, manifest)
	}

	return slices, nil
}

func (c *execController) writeResourceSlice(ctx context.Context, slice *apiv1.ResourceSlice) error {
	// We retry on request timeouts to avoid the overhead of re-synthesizing in cases where we're sometimes unable to reach apiserver
	return retry.OnError(retry.DefaultRetry, errors.IsServerTimeout, func() error {
		err := c.client.Create(ctx, slice)
		if err != nil {
			logr.FromContextOrDiscard(ctx).Error(err, "error while creating resource slice - will retry later")
		}
		return err
	})
}

func (c *execController) writeSuccessStatus(ctx context.Context, comp *apiv1.Composition, compGen int64, refs []*apiv1.ResourceSliceRef) error {
	logger := logr.FromContextOrDiscard(ctx)
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		err := c.client.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if err != nil {
			return err
		}

		if compGen < comp.Generation || comp.DeletionTimestamp != nil {
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

func consumeReconcileIntervalAnnotation(obj client.Object) *metav1.Duration {
	const key = "eno.azure.io/reconcile-interval"
	anno := obj.GetAnnotations()
	if anno == nil {
		return nil
	}
	str := anno[key]
	if str == "" {
		return nil
	}
	delete(anno, key)

	if len(anno) == 0 {
		anno = nil // apiserver treats an empty annotation map as nil, we must as well to avoid constant patches
	}
	obj.SetAnnotations(anno)

	dur, err := time.ParseDuration(str)
	if err != nil {
		return nil
	}
	return &metav1.Duration{Duration: dur}
}

type resourceRef struct {
	Name, Namespace, Kind, Group string
}

func newResourceRef(obj *unstructured.Unstructured) resourceRef {
	return resourceRef{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Kind:      obj.GetKind(),
		Group:     obj.GroupVersionKind().Group,
	}
}
