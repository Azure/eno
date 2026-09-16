package backup

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	gcInterval     = 5 * time.Minute
	gcSweepTimeout = 30 * time.Second
	gcMaxRequests  = 100
	gcMaxDeletions = 20
	gcPageSize     = 100
)

type gcInventoryRef struct {
	name, resourceVersion string
	uid                   types.UID
}

func (ref gcInventoryRef) object() *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: ref.name, Namespace: inventoryNamespace, UID: ref.uid, ResourceVersion: ref.resourceVersion,
	}}
}

type gcCandidate struct {
	ref  gcInventoryRef
	comp *apiv1.Composition
}

type gcLineage struct {
	lineage      string
	source       *apiv1.Composition
	continuation string
	listed       bool
	references   []gcInventoryRef
	seen         map[string]bool
	index        int
	selection    inventorySelection
	validated    bool
	candidates   []gcCandidate
	deleteIndex  int
}

type inventoryGC struct {
	reader     client.Reader
	downstream client.Client
	owns       func(context.Context, *apiv1.Composition) (bool, error)
	logger     logr.Logger

	continuation string
	page         []metav1.PartialObjectMetadata
	scanning     bool
	seenLineages map[string]bool
	work         *gcLineage
}

func newInventoryGC(mgr ctrl.Manager, downstreamConfig *rest.Config, owns func(context.Context, *apiv1.Composition) (bool, error)) (*inventoryGC, error) {
	reader, err := newGCClient(mgr.GetConfig(), mgr.GetScheme())
	if err != nil {
		return nil, fmt.Errorf("constructing inventory GC upstream client: %w", err)
	}
	downstream, err := newGCClient(downstreamConfig, mgr.GetScheme())
	if err != nil {
		return nil, fmt.Errorf("constructing inventory GC downstream client: %w", err)
	}
	return &inventoryGC{
		reader: reader, downstream: downstream, owns: owns,
		logger: mgr.GetLogger().WithValues("operation", "inventoryCleanup", "cleanupTrigger", "periodic"),
	}, nil
}

func (*inventoryGC) NeedLeaderElection() bool { return true }

func (g *inventoryGC) Start(ctx context.Context) error {
	timer := time.NewTimer(time.Duration(rand.Int64N(int64(gcInterval / 5))))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			g.sweep(logr.NewContext(ctx, g.logger))
			timer.Reset(wait.Jitter(gcInterval, 0.2))
		}
	}
}

func (g *inventoryGC) sweep(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, gcSweepTimeout)
	defer cancel()
	budget := &gcBudget{}
	ctx = context.WithValue(ctx, gcBudgetKey{}, budget)
	reader := &gcCandidateReader{
		Reader: g.reader, symphonies: map[types.NamespacedName]gcSymphonyRead{},
		compositions: map[gcPageKey]gcCompositionPage{},
	}
	logger := logr.FromContextOrDiscard(ctx)
	defer func() {
		logger.Info("inventory garbage collection sweep finished", "requests", budget.requestCount(), "deletionsAttempted", budget.deletionCount(),
			"pendingLineage", g.work != nil, "scanInProgress", g.scanning)
	}()
	for {
		if ctx.Err() != nil || budget.requestCount() >= gcMaxRequests || budget.deletionCount() >= gcMaxDeletions {
			logger.Info("deferring remaining inventory garbage collection work", "reason", "sweep budget exhausted")
			return
		}
		if g.work != nil {
			workLogger := logr.FromContextOrDiscard(inventoryCleanupContext(ctx, g.work.source, ""))
			done, err := g.advanceLineage(ctx, reader)
			if gcDeferred(ctx, err) {
				workLogger.Info("deferring inventory lineage cleanup", "reason", err.Error())
				return
			}
			if err != nil {
				workLogger.Error(err, "retaining remaining inventory for lineage until a later scan")
				g.work = nil
			} else if done {
				g.work = nil
			}
			continue
		}
		item, err := g.nextInventory(ctx)
		if err != nil {
			if gcDeferred(ctx, err) {
				logger.Info("deferring inventory discovery", "reason", err.Error())
			} else {
				logger.Error(err, "failed to discover inventory for garbage collection")
			}
			return
		}
		if item == nil {
			return
		}
		err = g.beginLineage(ctx, reader, item)
		if gcDeferred(ctx, err) {
			logger.Info("deferring inventory candidate", "inventoryConfigMap", item.Name, "reason", err.Error())
			return
		}
		g.page = g.page[1:]
	}
}

func gcDeferred(ctx context.Context, err error) bool {
	return err != nil && (errors.Is(err, errGCBudget) || ctx.Err() != nil)
}

func (g *inventoryGC) nextInventory(ctx context.Context) (*metav1.PartialObjectMetadata, error) {
	for len(g.page) == 0 {
		if g.scanning && g.continuation == "" {
			g.scanning = false
			g.seenLineages = nil
			return nil, nil
		}
		if g.seenLineages == nil {
			g.seenLineages = map[string]bool{}
		}
		list, err := g.listMetadata(ctx, g.continuation, client.HasLabels{inventoryLineageLabel})
		if apierrors.IsResourceExpired(err) {
			g.continuation = refreshedGCContinuation(ctx, err)
			g.scanning = false
			continue
		}
		if err != nil {
			return nil, err
		}
		g.scanning = true
		g.page, g.continuation = list.Items, list.Continue
	}
	return &g.page[0], nil
}

func (g *inventoryGC) listMetadata(ctx context.Context, continuation string, selector client.ListOption) (*metav1.PartialObjectMetadataList, error) {
	list := &metav1.PartialObjectMetadataList{}
	list.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("ConfigMapList"))
	err := g.downstream.List(ctx, list, client.InNamespace(inventoryNamespace), selector, client.Limit(gcPageSize), client.Continue(continuation))
	return list, err
}

func refreshedGCContinuation(ctx context.Context, err error) string {
	var status apierrors.APIStatus
	token := ""
	if errors.As(err, &status) {
		token = status.Status().Continue
	}
	logr.FromContextOrDiscard(ctx).Error(err, "inventory metadata continuation expired; resuming discovery", "replacementContinuation", token != "")
	return token
}

func (g *inventoryGC) beginLineage(ctx context.Context, reader client.Reader, item *metav1.PartialObjectMetadata) (err error) {
	lineage := item.Labels[inventoryLineageLabel]
	if g.seenLineages[lineage] {
		return nil
	}
	defer func() {
		if err != nil && !gcDeferred(ctx, err) {
			logr.FromContextOrDiscard(ctx).Error(err, "retaining inventory candidate", "inventoryConfigMap", item.Name, "lineage", lineage)
		}
	}()
	obj := &corev1.ConfigMap{}
	if err := g.downstream.Get(ctx, client.ObjectKeyFromObject(item), obj); err != nil {
		return fmt.Errorf("reading inventory candidate: %w", err)
	}
	if obj.UID != item.UID || obj.ResourceVersion != item.ResourceVersion {
		return fmt.Errorf("inventory candidate changed after discovery")
	}
	if obj.UID == "" || obj.ResourceVersion == "" {
		return fmt.Errorf("inventory candidate has no UID or resourceVersion")
	}
	snapshot, err := decodeInventoryForCleanup(*obj)
	if err != nil {
		g.seenLineages[lineage] = true
		return err
	}
	comp, err := snapshot.cleanupComposition()
	if err != nil {
		return err
	}
	if comp == nil {
		logr.FromContextOrDiscard(ctx).Info("retaining inventory without cleanup ownership metadata", "inventoryConfigMap", obj.Name, "lineage", lineage)
		return nil
	}
	ctx = inventoryCleanupContext(ctx, comp, obj.Name)
	owned, err := g.owns(ctx, comp)
	if err != nil {
		return err
	}
	if !owned {
		logr.FromContextOrDiscard(ctx).V(1).Info("retaining inventory outside the configured ownership scope")
		return nil
	}
	if eligible, err := variationInventoryCanBeDeleted(ctx, reader, comp); err != nil || !eligible {
		return err
	}
	g.seenLineages[lineage] = true
	g.work = &gcLineage{lineage: lineage, source: comp, seen: map[string]bool{}}
	return nil
}

func (g *inventoryGC) advanceLineage(ctx context.Context, reader client.Reader) (bool, error) {
	work := g.work
	ctx = inventoryCleanupContext(ctx, work.source, "")
	if !work.listed {
		list, err := g.listMetadata(ctx, work.continuation, client.MatchingLabels{inventoryLineageLabel: work.lineage})
		if apierrors.IsResourceExpired(err) {
			work.continuation = refreshedGCContinuation(ctx, err)
			if work.continuation == "" {
				work.references = nil
				work.seen = map[string]bool{}
			}
			return false, nil
		}
		if err != nil {
			return false, err
		}
		for _, item := range list.Items {
			if !work.seen[item.Name] {
				work.references = append(work.references, gcInventoryRef{name: item.Name, uid: item.UID, resourceVersion: item.ResourceVersion})
				work.seen[item.Name] = true
			}
		}
		work.continuation, work.listed = list.Continue, list.Continue == ""
		return false, nil
	}
	if !work.validated {
		if work.index < len(work.references) {
			ref := work.references[work.index]
			obj := &corev1.ConfigMap{}
			if err := g.downstream.Get(ctx, client.ObjectKeyFromObject(ref.object()), obj); err != nil {
				return false, fmt.Errorf("reading inventory %q during lineage validation: %w", ref.name, err)
			}
			if obj.UID != ref.uid || obj.ResourceVersion != ref.resourceVersion {
				return false, fmt.Errorf("inventory %q changed during lineage validation", ref.name)
			}
			snapshot, err := decodeInventory(work.source, *obj)
			if err != nil {
				return false, err
			}
			if err := work.selection.add(snapshot); err != nil {
				return false, err
			}
			if len(work.candidates) < gcMaxDeletions {
				if err := g.addCandidate(ctx, reader, snapshot, ref); err != nil {
					return false, err
				}
			}
			work.index++
			return false, nil
		}
		if _, err := work.selection.selected(); err != nil {
			return false, err
		}
		work.validated = true
		work.references, work.seen = nil, nil
		work.selection = inventorySelection{}
	}
	if work.deleteIndex >= len(work.candidates) {
		return true, nil
	}
	candidate := work.candidates[work.deleteIndex]
	ctx = inventoryCleanupContext(ctx, candidate.comp, candidate.ref.name)
	if eligible, err := variationInventoryCanBeDeleted(ctx, g.reader, candidate.comp); err != nil || !eligible {
		if err == nil {
			work.deleteIndex++
		}
		return false, err
	}
	if err := deleteInventorySnapshot(ctx, g.downstream, candidate.ref.object()); err != nil {
		return false, fmt.Errorf("deleting retired inventory %q: %w", candidate.ref.name, err)
	}
	logr.FromContextOrDiscard(ctx).Info("retired variation inventory cleaned up")
	work.deleteIndex++
	return false, nil
}

func (g *inventoryGC) addCandidate(ctx context.Context, reader client.Reader, snapshot *inventorySnapshot, ref gcInventoryRef) error {
	comp, err := snapshot.cleanupComposition()
	if err != nil {
		return err
	}
	if comp == nil {
		logr.FromContextOrDiscard(ctx).Info("retaining inventory without cleanup ownership metadata", "inventoryConfigMap", ref.name)
		return nil
	}
	ctx = inventoryCleanupContext(ctx, comp, ref.name)
	owned, err := g.owns(ctx, comp)
	if err != nil {
		return err
	}
	if !owned {
		logr.FromContextOrDiscard(ctx).V(1).Info("retaining inventory outside the configured ownership scope")
		return nil
	}
	if eligible, err := variationInventoryCanBeDeleted(ctx, reader, comp); err != nil || !eligible {
		return err
	}
	g.work.candidates = append(g.work.candidates, gcCandidate{ref: ref, comp: comp})
	return nil
}
