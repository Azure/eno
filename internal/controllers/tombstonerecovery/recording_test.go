package tombstonerecovery

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func recordingTestFixture(t *testing.T, names ...string) *controllerTestFixture {
	t.Helper()
	f := newControllerTestFixture(t, false, names...)
	f.finish(reasonNotNeeded)
	f.ready()
	return f
}

func recordingTestLargeSlice(f *controllerTestFixture) {
	f.t.Helper()
	slice := f.slice("desired")
	for _, name := range []string{"first", "second", "third"} {
		res := controllerTestResource(name)
		res.Labels = map[string]string{"padding": strings.Repeat("x", inventoryMaxDataBytes/2)}
		slice.Spec.Resources = append(slice.Spec.Resources, inventoryTestManifest(f.t, res))
	}
	require.NoError(f.t, f.upstream.Update(f.t.Context(), slice))
}

func TestInventoryRecordingEligibility(t *testing.T) {
	for _, mode := range []string{"unready", "reconciled only", "unsynthesized", "unfinished", "wrong decision UUID", "unannotated", "false annotation", "deleting", "wrong namespace", "wrong selector"} {
		t.Run(mode, func(t *testing.T) {
			f := recordingTestFixture(t, "desired")
			comp := f.composition()
			switch mode {
			case "unready":
				f.updateStatus(func(syn *apiv1.Synthesis) {
					syn.Ready = nil
				})
			case "reconciled only":
				f.updateStatus(func(syn *apiv1.Synthesis) {
					syn.Reconciled, syn.Ready = syn.Ready, nil
				})
			case "unsynthesized":
				f.updateStatus(func(syn *apiv1.Synthesis) {
					syn.Synthesized = nil
				})
			case "unfinished", "wrong decision UUID":
				f.updateStatus(func(syn *apiv1.Synthesis) {
					syn.TombstoneRecoveryStatus.Status = mode == "wrong decision UUID"
					syn.TombstoneRecoveryStatus.SynthesisUUID = "old"
				})
			case "unannotated", "false annotation":
				delete(comp.Annotations, "eno.azure.io/recovery-enabled")
				if mode == "false annotation" {
					comp.Annotations["eno.azure.io/recovery-enabled"] = "false"
				}
				require.NoError(t, f.upstream.Update(t.Context(), comp))
			case "deleting":
				require.NoError(t, f.upstream.Delete(t.Context(), comp))
			case "wrong namespace":
				f.controller.namespace = "other"
			case "wrong selector":
				f.controller.compositionSelector = labels.SelectorFromSet(labels.Set{"owner": "other"})
			}
			f.controller.downstream = nil // Neither inventory reads nor writes are allowed in these cases.
			f.reconcileEvent()
			assert.Empty(t, f.inventories())
		})
	}
}

func TestInventoryRecordingReadyAndIdempotent(t *testing.T) {
	for _, names := range [][]string{nil, {"desired"}} {
		t.Run(fmt.Sprint(len(names)), func(t *testing.T) {
			f := newControllerTestFixture(t, false, names...)
			f.ready()
			f.reconcileEvent()
			assert.Empty(t, f.inventories(), "recording waits for a subsequent event with a persisted recovery decision")
			before := f.composition()
			f.reconcileEvent()
			f.assertInventory(2, names...)
			stored := f.inventories()
			f.restart()
			f.reconcileEvent()
			assert.Equal(t, stored, f.inventories(), "identical snapshots must not be rewritten after restart")
			assert.Equal(t, before, f.composition(), "recording must not modify recovery or synthesis status")

			comp := f.composition()
			comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: controllerTestUUID(3)}
			require.NoError(t, f.upstream.Status().Update(t.Context(), comp))
			f.reconcileEvent()
			assert.Equal(t, stored, f.inventories(), "an in-flight successor does not invalidate a Ready current synthesis")
		})
	}
}

func TestInventoryRecordingPartialFailureAndShrink(t *testing.T) {
	for _, updating := range []bool{false, true} {
		t.Run(fmt.Sprintf("update-%t", updating), func(t *testing.T) {
			f := recordingTestFixture(t)
			recordingTestLargeSlice(f)
			if updating {
				old := f.composition()
				old.Status.CurrentSynthesis.UUID = controllerTestUUID(1)
				old.Status.CurrentSynthesis.Synthesized = &metav1.Time{Time: old.Status.CurrentSynthesis.Synthesized.Add(-time.Minute)}
				chunks, err := makeInventory(old, []apiv1.ResourceSlice{*f.slice("desired")})
				require.NoError(t, err)
				for i := range chunks {
					require.NoError(t, f.downstream.Create(t.Context(), &chunks[i]))
				}
			}
			writes := 0
			failure := apierrors.NewServiceUnavailable("second chunk unavailable")
			failSecond := func() error {
				writes++
				if writes == 2 {
					return failure
				}
				return nil
			}
			f.controller.downstream = interceptor.NewClient(f.downstream, interceptor.Funcs{
				Create: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if err := failSecond(); err != nil {
						return err
					}
					return cli.Create(ctx, obj, opts...)
				},
				Update: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if err := failSecond(); err != nil {
						return err
					}
					return cli.Update(ctx, obj, opts...)
				},
			})
			require.ErrorIs(t, f.reconcile(), failure)
			partial, err := selectInventory(f.inventories())
			require.NoError(t, err)
			require.Len(t, partial, 1)
			_, err = decodeInventorySnapshot(f.composition(), partial)
			require.ErrorContains(t, err, "expected 3")
			first := partial[0].DeepCopy()
			f.restart()
			f.reconcileEvent()
			require.Equal(t, 4, writes, "retry must skip the already-persisted first chunk")
			selected, err := selectInventory(f.inventories())
			require.NoError(t, err)
			require.Len(t, selected, 3)
			decoded, err := decodeInventorySnapshot(f.composition(), selected)
			require.NoError(t, err)
			require.Len(t, decoded, 3)
			persisted := &corev1.Secret{}
			require.NoError(t, f.downstream.Get(t.Context(), client.ObjectKeyFromObject(first), persisted))
			assert.Equal(t, first, persisted)

			comp := f.composition()
			comp.Status.CurrentSynthesis.UUID = controllerTestUUID(3)
			comp.Status.CurrentSynthesis.Synthesized = &metav1.Time{Time: comp.Status.CurrentSynthesis.Synthesized.Add(time.Minute)}
			comp.Status.CurrentSynthesis.TombstoneRecoveryStatus.SynthesisUUID = controllerTestUUID(3)
			comp.Status.CurrentSynthesis.ResourceSlices = nil
			require.NoError(t, f.upstream.Status().Update(t.Context(), comp))
			f.reconcileEvent()
			require.Len(t, f.inventories(), 3, "recording does not garbage-collect leftover chunks")
			f.assertInventory(3)
		})
	}
}

func TestInventoryRecordingFreshnessBeforeWrites(t *testing.T) {
	for _, mode := range []string{"synthesis", "UID", "opt-out", "deleting", "not ready", "unfinished", "selector", "synthesizer", "read error"} {
		t.Run(mode, func(t *testing.T) {
			f := recordingTestFixture(t)
			recordingTestLargeSlice(f)
			f.controller.compositionSelector = labels.SelectorFromSet(labels.Set{"owner": "team"})
			writes := 0
			failure := apierrors.NewServiceUnavailable("composition unavailable")
			f.controller.downstream = interceptor.NewClient(f.downstream, interceptor.Funcs{
				Create: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					writes++
					err := cli.Create(ctx, obj, opts...)
					require.NoError(t, err)
					if mode == "read error" {
						f.controller.reader = interceptor.NewClient(f.upstream, interceptor.Funcs{
							Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
								return failure
							},
						})
						return nil
					}
					comp := f.composition()
					switch mode {
					case "synthesis", "not ready", "unfinished":
						if mode == "synthesis" {
							comp.Status.CurrentSynthesis.UUID = controllerTestUUID(3)
						} else if mode == "not ready" {
							comp.Status.CurrentSynthesis.Ready = nil
						} else {
							comp.Status.CurrentSynthesis.TombstoneRecoveryStatus.Status = false
						}
						require.NoError(t, f.upstream.Status().Update(ctx, comp))
					case "deleting":
						require.NoError(t, f.upstream.Delete(ctx, comp))
					default:
						switch mode {
						case "UID":
							comp.UID = "replacement"
						case "opt-out":
							delete(comp.Annotations, "eno.azure.io/recovery-enabled")
						case "selector":
							comp.Labels["owner"] = "other"
						case "synthesizer":
							comp.Spec.Synthesizer.Name = "other"
						}
						require.NoError(t, f.upstream.Update(ctx, comp))
					}
					return nil
				},
			})
			result, err := f.controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: f.key})
			if mode == "read error" {
				require.ErrorIs(t, err, failure)
			} else {
				require.NoError(t, err)
				assert.True(t, result.Requeue)
			}
			assert.Equal(t, 1, writes, "freshness must be checked before every chunk")
			assert.Len(t, f.inventories(), 1)
		})
	}
}

func TestInventoryRecordingConflicts(t *testing.T) {
	for _, mode := range []string{"update conflict", "create AlreadyExists"} {
		t.Run(mode, func(t *testing.T) {
			f := recordingTestFixture(t, "desired")
			var existing *corev1.Secret
			if mode == "update conflict" {
				existing = f.history("old")
			}
			intercepted := false
			f.controller.downstream = interceptor.NewClient(f.downstream, interceptor.Funcs{
				Create: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if !intercepted {
						intercepted = true
						require.NoError(t, cli.Create(ctx, obj.DeepCopyObject().(client.Object)))
					}
					return cli.Create(ctx, obj, opts...)
				},
				Update: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if !intercepted {
						intercepted = true
						existing.Annotations["example.com/concurrent"] = "preserve"
						require.NoError(t, cli.Update(ctx, existing))
					}
					return cli.Update(ctx, obj, opts...)
				},
			})
			err := f.reconcile()
			if mode == "update conflict" {
				require.True(t, apierrors.IsConflict(err), "stale update must be rejected: %v", err)
				f.assertInventory(1, "old")
			} else {
				require.True(t, apierrors.IsAlreadyExists(err), "duplicate create must retry: %v", err)
			}
			f.reconcileEvent()
			f.assertInventory(2, "desired")
			if mode == "update conflict" {
				assert.Equal(t, "preserve", f.inventories()[0].Annotations["example.com/concurrent"])
			}
		})
	}
}

func TestInventoryRecordingInvalidInputs(t *testing.T) {
	for _, mode := range []string{"nil ref", "missing slice", "terminating slice", "invalid manifest", "read error", "secret get error", "forbidden write"} {
		t.Run(mode, func(t *testing.T) {
			f := recordingTestFixture(t, "desired")
			failure := apierrors.NewServiceUnavailable("read unavailable")
			switch mode {
			case "nil ref":
				f.updateStatus(func(syn *apiv1.Synthesis) {
					syn.ResourceSlices = []*apiv1.ResourceSliceRef{nil}
				})
			case "missing slice":
				require.NoError(t, f.upstream.Delete(t.Context(), f.slice("desired")))
			case "terminating slice", "invalid manifest":
				slice := f.slice("desired")
				if mode == "terminating slice" {
					slice.Finalizers = []string{"eno.azure.io/cleanup"}
				} else {
					slice.Spec.Resources[0].Manifest = "{"
				}

				require.NoError(t, f.upstream.Update(t.Context(), slice))
				if mode == "terminating slice" {
					require.NoError(t, f.upstream.Delete(t.Context(), slice))
				}
			case "read error":
				f.controller.reader = interceptor.NewClient(f.upstream, interceptor.Funcs{
					Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*apiv1.ResourceSlice); ok {
							return failure
						}
						return cli.Get(ctx, key, obj, opts...)
					},
				})
			case "secret get error":
				f.controller.downstream = interceptor.NewClient(f.downstream, interceptor.Funcs{
					Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
						return failure
					},
				})
			case "forbidden write":
				failure = apierrors.NewForbidden(corev1.Resource("secrets"), "inventory", fmt.Errorf("denied"))
				f.controller.downstream = interceptor.NewClient(f.downstream, interceptor.Funcs{
					Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
						return failure
					},
				})
			}
			before := f.composition()
			require.Error(t, f.reconcile())
			assert.Equal(t, before, f.composition())
			assert.Empty(t, f.inventories())
			if mode == "read error" || mode == "secret get error" || mode == "forbidden write" {
				require.ErrorIs(t, f.reconcile(), failure)
				f.controller.reader, f.controller.downstream = f.upstream, f.downstream
				f.reconcileEvent()
				f.assertInventory(2, "desired")
			}
		})
	}
}

func TestInventoryRecordingPreservesConflictingSecrets(t *testing.T) {
	for _, mode := range []string{"same UUID different data", "newer snapshot", "wrong lineage", "deleting", "truncated name collision"} {
		t.Run(mode, func(t *testing.T) {
			f := recordingTestFixture(t, "desired")
			secret := f.history("old")
			switch mode {
			case "same UUID different data":
				secret.Annotations[inventorySynthesisUUIDAnnotation] = controllerTestUUID(2)
				secret.Annotations[inventorySynthesizedAnnotation] = f.composition().Status.CurrentSynthesis.Synthesized.Format(time.RFC3339)
			case "newer snapshot":
				secret.Annotations[inventorySynthesizedAnnotation] = f.composition().Status.CurrentSynthesis.Synthesized.Add(time.Minute).Format(time.RFC3339)
			case "wrong lineage":
				secret.Labels[inventoryLineageLabel] = "other"
			case "deleting":
				secret.Finalizers = []string{"test.example/hold"}
			case "truncated name collision":
				comp := f.composition()
				require.NoError(t, f.downstream.Delete(t.Context(), secret))
				name := strings.Repeat("a", 240)
				comp.Name = name + "-current"
				comp.ResourceVersion = ""
				require.NoError(t, f.upstream.Create(t.Context(), comp))
				f.key = client.ObjectKeyFromObject(comp)
				secret.Annotations[inventoryCompositionNameAnnotation] = name + "-other"
				secret.Name = inventoryName(name+"-other", inventoryLineage(comp), 0)
				assert.Equal(t, inventoryName(comp.Name, inventoryLineage(comp), 0), secret.Name)
				secret.ResourceVersion = ""
				require.NoError(t, f.downstream.Create(t.Context(), secret))
			}
			require.NoError(t, f.downstream.Update(t.Context(), secret))
			if mode == "deleting" {
				require.NoError(t, f.downstream.Delete(t.Context(), secret))
			}
			before := f.inventories()
			require.Error(t, f.reconcile())
			assert.Equal(t, before, f.inventories(), "rejected replacement must not change any Secret")
		})
	}
}

func TestInventoryRecordingSupersededBeforeUpdate(t *testing.T) {
	f := recordingTestFixture(t, "desired")
	f.history("old")
	before := f.inventories()
	f.controller.downstream = interceptor.NewClient(f.downstream, interceptor.Funcs{
		Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			err := cli.Get(ctx, key, obj, opts...)
			f.updateStatus(func(syn *apiv1.Synthesis) {
				syn.UUID = controllerTestUUID(3)
			})
			return err
		},
		Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
			t.Error("superseded recording must not update the Secret")
			return fmt.Errorf("unexpected write")
		},
	})
	result, err := f.controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: f.key})
	require.NoError(t, err)
	assert.True(t, result.Requeue)
	assert.Equal(t, before, f.inventories())
}

func TestInventoryRecoverySecretFailureDecisions(t *testing.T) {
	for _, mode := range []string{"missing chunk", "mixed UUID", "mixed timestamp", "tied snapshots", "malformed JSON", "read failure", "ConfigMap only"} {
		t.Run(mode, func(t *testing.T) {
			f := newControllerTestFixture(t, true, "desired")
			chunks := inventoryTestChunkedSnapshot(t, f.composition())
			failure := apierrors.NewServiceUnavailable("inventory unavailable")
			switch mode {
			case "missing chunk":
				chunks = chunks[:1]
			case "mixed UUID":
				chunks[1].Annotations[inventorySynthesisUUIDAnnotation] = "other"
				chunks[1].Annotations[inventorySynthesizedAnnotation] = f.composition().Status.CurrentSynthesis.Synthesized.Add(-time.Minute).Format(time.RFC3339)
			case "mixed timestamp":
				chunks[1].Annotations[inventorySynthesizedAnnotation] = f.composition().Status.CurrentSynthesis.Synthesized.Add(-time.Minute).Format(time.RFC3339)
			case "tied snapshots":
				other := chunks[0].DeepCopy()
				other.Name = inventoryName("other", inventoryLineage(f.composition()), 0)
				other.Annotations[inventoryCompositionNameAnnotation] = "other"
				other.Annotations[inventorySynthesisUUIDAnnotation] = "other"
				chunks = append(chunks, *other)
			case "malformed JSON":
				chunks[0].Data[inventoryDataKey] = []byte("{")
			case "read failure":
				var err error
				chunks, err = makeInventory(f.composition(), nil)
				require.NoError(t, err)
				f.controller.downstream = interceptor.NewClient(f.downstream, interceptor.Funcs{
					List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
						return failure
					},
				})
			case "ConfigMap only":
				require.NoError(t, f.downstream.Create(t.Context(), &corev1.ConfigMap{
					ObjectMeta: *chunks[0].ObjectMeta.DeepCopy(),
					Data: map[string]string{inventoryDataKey: string(chunks[0].Data[inventoryDataKey])},
				}))
				chunks = nil
			}
			for i := range chunks {
				require.NoError(t, f.downstream.Create(t.Context(), &chunks[i]))
			}
			before := f.slice("desired")
			err := f.reconcile()
			status := f.composition().Status.CurrentSynthesis.TombstoneRecoveryStatus
			require.NotNil(t, status)
			if mode == "read failure" {
				require.ErrorIs(t, err, failure)
				assert.False(t, status.Status)
				assert.Equal(t, reasonInventoryGetError, status.Reason)
				f.controller.downstream = f.downstream
				f.finish(reasonFinished)
			} else {
				require.NoError(t, err)
				assert.True(t, status.Status)
				reason := reasonInventoryInvalid
				if mode == "ConfigMap only" {
					reason = reasonInventoryNotFound
				}
				assert.Equal(t, reason, status.Reason)
				assert.Equal(t, before, f.slice("desired"), "invalid or absent inventory must not generate tombstones")
			}
		})
	}
}
