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
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// maxSliceJsonBytes is the max sum of a resource slice's manifests.
const maxSliceJsonBytes = 1024 * 512

type execController struct {
	client           client.Client
	noCacheClient    client.Reader
	conn             SynthesizerConnection
	createSliceLimit flowcontrol.RateLimiter
}

func NewExecController(mgr ctrl.Manager, cfg *Config, conn SynthesizerConnection) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "execController")).
		Complete(&execController{
			client:           mgr.GetClient(),
			noCacheClient:    mgr.GetAPIReader(),
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

	wait, ok, err := c.shouldDebounce(ctx, pod)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ok {
		logger.V(1).Info(fmt.Sprintf("tried to re-exec too soon - will wait %dms before retrying", wait.Milliseconds()))
		return ctrl.Result{RequeueAfter: wait}, nil
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

// shouldDebounce defers exec attempts that occur within a hardcoded debounce period.
// The idea is to protect against accidentally invoking a synthesizer process a second time in cases where
// the pod changed during the initial synthesis. The composition status write is unlikely to hit the informer
// cache before the next pod reconcile, so this controller will always run a second synthesis.
// The actual debounce time isn't important - it just needs to be greater than informer latency most of the time.
//
// A nice side effect of this approach: the update coordinates with apiserver i.e. it will fail if the
// pod has changed since our cached version. So if the lifecycle controller has already deleted it we will
// re-enter the loop and skip synthesis.
func (c *execController) shouldDebounce(ctx context.Context, pod *corev1.Pod) (time.Duration, bool, error) {
	const key = "eno.azure.io/exec-start-time"
	const min = time.Millisecond * 250

	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}

	// Defer the operation if within the debounce period
	last, err := time.Parse(time.RFC3339, pod.Annotations[key])
	if err == nil {
		delta := time.Since(last)
		if delta < min {
			return min - delta, false, nil
		}
	}

	// Set the annotation and continue
	pod.Annotations[key] = time.Now().Format(time.RFC3339)
	err = c.client.Update(ctx, pod)
	if err != nil {
		return 0, false, fmt.Errorf("writing annotation: %w", err)
	}

	return 0, true, nil
}

func (c *execController) synthesize(ctx context.Context, syn *apiv1.Synthesizer, comp *apiv1.Composition, pod *corev1.Pod) ([]*apiv1.ResourceSliceRef, error) {
	logger := logr.FromContextOrDiscard(ctx)

	input, err := buildPodInput(comp, syn)
	if err != nil {
		return nil, fmt.Errorf("building synthesis Pod input: %w", err)
	}

	start := time.Now()
	stdout, err := c.conn.Synthesize(ctx, syn, pod, input)
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
		err := c.noCacheClient.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		if errors.IsNotFound(err) {
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
