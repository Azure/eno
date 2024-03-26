package synthesis

import (
	"context"
	"fmt"
	"strconv"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// maxSliceJsonBytes is the max sum of a resource slice's manifests. It's set to 1mb, which leaves 512kb of space for the resource's status, encoding overhead, etc.
const maxSliceJsonBytes = 1024 * 768

type execController struct {
	client           client.Client
	conn             SynthesizerConnection
	createSliceLimit flowcontrol.RateLimiter
}

func NewExecController(mgr ctrl.Manager, cfg *Config, conn SynthesizerConnection) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "execController")).
		Complete(&execController{
			client:           mgr.GetClient(),
			conn:             conn,
			createSliceLimit: flowcontrol.NewTokenBucketRateLimiter(float32(cfg.SliceCreationQPS), 1),
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
	if len(pod.Status.ContainerStatuses) == 0 || pod.Status.ContainerStatuses[0].State.Running == nil || pod.DeletionTimestamp != nil {
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

	if compGen < comp.Generation || (comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil) || comp.DeletionTimestamp != nil {
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

	// Let the informers catch up
	// Obviously this isn't ideal, consider a lamport clock in memory
	time.Sleep(time.Millisecond * 100)

	return ctrl.Result{}, nil
}

func (c *execController) synthesize(ctx context.Context, syn *apiv1.Synthesizer, comp *apiv1.Composition, pod *corev1.Pod) ([]*apiv1.ResourceSliceRef, error) {
	logger := logr.FromContextOrDiscard(ctx)

	start := time.Now()
	stdout, err := c.conn.Synthesize(ctx, syn, pod)
	if err != nil {
		return nil, err
	}

	refs, err := c.writeOutputToSlices(ctx, comp, stdout)
	if err != nil {
		return nil, err
	}

	latency := time.Since(start)
	synthesisLatency.Observe(latency.Seconds())
	logger.V(0).Info("synthesis is done", "latency", latency.Milliseconds())
	return refs, nil
}

func (c *execController) fetchPreviousSlices(ctx context.Context, comp *apiv1.Composition) ([]*apiv1.ResourceSlice, error) {
	if comp.Status.PreviousSynthesis == nil {
		return nil, nil // nothing to fetch
	}
	logger := logr.FromContextOrDiscard(ctx)

	slices := []*apiv1.ResourceSlice{}
	for _, ref := range comp.Status.PreviousSynthesis.ResourceSlices {
		slice := &apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := c.client.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		if errors.IsNotFound(err) && comp.Status.PreviousSynthesis.Synthesized != nil && time.Since(comp.Status.PreviousSynthesis.Synthesized.Time) > time.Minute*5 {
			// It's possible that the informer is just stale, set some arbitrary period after synthesis at which the resource slices are expected to exist in cache.
			logger.V(0).Info("resource slice referenced by composition was not found - skipping", "resourceSliceName", slice.Name)
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

			// We don't need a tombstone once the deleted resource has been reconciled
			if _, ok := refs[newResourceRef(obj)]; ok || ((res.Deleted || slice.DeletionTimestamp != nil) && slice.Status.Resources != nil && slice.Status.Resources[i].Reconciled) {
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
			slice.Finalizers = []string{"eno.azure.io/cleanup"}
			slice.OwnerReferences = []metav1.OwnerReference{{
				APIVersion:         apiv1.SchemeGroupVersion.Identifier(),
				Kind:               "Composition",
				Name:               comp.Name,
				UID:                comp.UID,
				BlockOwnerDeletion: &blockOwnerDeletion, // we need the composition in order to successfully delete its resource slices
				Controller:         &blockOwnerDeletion,
			}}
			slice.Spec.CompositionGeneration = comp.Generation
			slices = append(slices, slice)
		}
		sliceBytes += len(manifest.Manifest)
		slice.Spec.Resources = append(slice.Spec.Resources, manifest)
	}

	return slices, nil
}

func (c *execController) writeResourceSlice(ctx context.Context, slice *apiv1.ResourceSlice) error {
	var bytes int
	for _, res := range slice.Spec.Resources {
		bytes += len(res.Manifest)
	}

	// We retry on request timeouts to avoid the overhead of re-synthesizing in cases where we're sometimes unable to reach apiserver
	return retry.OnError(retry.DefaultRetry, errors.IsServerTimeout, func() error {
		err := c.client.Create(ctx, slice)
		if err != nil {
			logr.FromContextOrDiscard(ctx).Error(err, "error while creating resource slice - will retry later")
			return err
		}
		resourceSliceWrittenBytes.Add(float64(bytes))
		return nil
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

		if comp.Status.CurrentSynthesis == nil {
			comp.Status.CurrentSynthesis = &apiv1.Synthesis{}
		}
		if comp.Status.CurrentSynthesis.Synthesized != nil {
			return nil // no updates needed
		}
		now := metav1.Now()
		comp.Status.CurrentSynthesis.Synthesized = &now
		comp.Status.CurrentSynthesis.ResourceSlices = refs

		err = c.client.Status().Update(ctx, comp)
		if err != nil {
			return err
		}

		logger.V(0).Info("composition status has been updated following successful synthesis")
		return nil
	})
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
