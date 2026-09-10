package execution

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const recoveryNamespace = "recovery"

func recoveryObject(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": name, "namespace": recoveryNamespace},
		"data":       map[string]any{"value": name},
	}}
}

func recoverySlice(t *testing.T, name string, objects ...*unstructured.Unstructured) *apiv1.ResourceSlice {
	t.Helper()
	slice := &apiv1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: recoveryNamespace},
		Spec:       apiv1.ResourceSliceSpec{SynthesisUUID: "baseline-uuid"},
	}
	for _, obj := range objects {
		js, err := obj.MarshalJSON()
		require.NoError(t, err)
		slice.Spec.Resources = append(slice.Spec.Resources, apiv1.Manifest{Manifest: string(js)})
		slice.Status.Resources = append(slice.Status.Resources, apiv1.ResourceState{})
	}
	return slice
}

func recoveryRefs(names ...string) []*apiv1.ResourceSliceRef {
	refs := make([]*apiv1.ResourceSliceRef, len(names))
	for i, name := range names {
		refs[i] = &apiv1.ResourceSliceRef{Name: name}
	}
	return refs
}

func recoveryClient(t *testing.T, funcs interceptor.Funcs, objects ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.SchemeBuilder.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&apiv1.Composition{}, &apiv1.ResourceSlice{}).
		WithInterceptorFuncs(funcs).
		Build()
}

func recoveryAssertHistoryUnchanged(t *testing.T, cli client.Client, history ...*apiv1.ResourceSlice) {
	t.Helper()
	stored := &apiv1.ResourceSliceList{}
	require.NoError(t, cli.List(context.Background(), stored))
	byKey := map[client.ObjectKey]apiv1.ResourceSliceSpec{}
	for _, slice := range stored.Items {
		byKey[client.ObjectKeyFromObject(&slice)] = slice.Spec
	}
	for _, slice := range history {
		spec, ok := byKey[client.ObjectKeyFromObject(slice)]
		require.True(t, ok, "historical slice %s/%s was removed", slice.Namespace, slice.Name)
		assert.Equal(t, slice.Spec, spec, "historical slice %s/%s was mutated", slice.Namespace, slice.Name)
	}
}

func TestRecoveryHistoryFetch(t *testing.T) {
	type testCase struct {
		name      string
		noCurrent bool
		refs      []*apiv1.ResourceSliceRef
		inherited bool
		wantFlag  bool
		wantNames []string
		wantReads []string
		readError error
	}
	tests := []testCase{
		{name: "no-current", noCurrent: true, wantFlag: true},
		{name: "nil-reference-list"},
		{name: "empty-reference-list", refs: []*apiv1.ResourceSliceRef{}},
		{name: "empty-inherited", inherited: true, wantFlag: true},
		{name: "healthy", refs: recoveryRefs("a", "b"), wantNames: []string{"a", "b"}, wantReads: []string{"a", "b"}},
		{name: "healthy-inherited", refs: recoveryRefs("a", "b"), inherited: true, wantFlag: true, wantNames: []string{"a", "b"}, wantReads: []string{"a", "b"}},
		{name: "nil-entry-only", refs: []*apiv1.ResourceSliceRef{nil}, wantFlag: true},
		{name: "empty-name-only", refs: recoveryRefs(""), wantFlag: true},
		{name: "named-not-found", refs: recoveryRefs("missing"), wantFlag: true, wantReads: []string{"missing"}},
		{name: "other-namespace-only", refs: recoveryRefs("elsewhere"), wantFlag: true, wantReads: []string{"elsewhere"}},
		{
			name:     "mixed-valid-before-and-after",
			refs:     []*apiv1.ResourceSliceRef{{Name: "b"}, nil, {}, {Name: "missing"}, {Name: "a"}},
			wantFlag: true, wantNames: []string{"b", "a"}, wantReads: []string{"b", "missing", "a"},
		},
	}
	for _, bad := range []struct {
		name string
		ref  *apiv1.ResourceSliceRef
	}{
		{name: "nil", ref: nil},
		{name: "empty", ref: &apiv1.ResourceSliceRef{}},
		{name: "missing", ref: &apiv1.ResourceSliceRef{Name: "missing"}},
	} {
		for position, label := range []string{"first", "middle", "last"} {
			refs := recoveryRefs("a", "b")
			refs = append(refs[:position], append([]*apiv1.ResourceSliceRef{bad.ref}, refs[position:]...)...)
			reads := []string{"a", "b"}
			if bad.name == "missing" {
				reads = append(reads[:position], append([]string{"missing"}, reads[position:]...)...)
			}
			tests = append(tests, testCase{
				name: bad.name + "-" + label, refs: refs, wantFlag: true,
				wantNames: []string{"a", "b"}, wantReads: reads,
			})
		}
	}
	for _, failure := range []struct {
		name string
		err  error
	}{
		{"forbidden", apierrors.NewForbidden(schema.GroupResource{Group: apiv1.SchemeGroupVersion.Group, Resource: "resourceslices"}, "fatal", fmt.Errorf("denied"))},
		{"timeout", apierrors.NewTimeoutError("historical read timed out", 1)},
		{"generic", fmt.Errorf("historical transport failure")},
	} {
		for position, label := range []string{"first", "middle", "last"} {
			refs := recoveryRefs("a", "b")
			refs = append(refs[:position], append(recoveryRefs("fatal"), refs[position:]...)...)
			reads := append([]string{"a", "b"}[:position:position], "fatal")
			tests = append(tests, testCase{
				name: failure.name + "-" + label, refs: refs, inherited: true,
				readError: failure.err, wantReads: reads,
			})
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comp := &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Name: "comp", Namespace: recoveryNamespace},
				Status: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{
					UUID: "baseline-uuid", ResourceSlices: tt.refs, TombstoneRecoveryRequired: tt.inherited,
				}},
			}
			if tt.noCurrent {
				comp.Status.CurrentSynthesis = nil
			}
			before := comp.DeepCopy()
			a, b := recoverySlice(t, "a", recoveryObject("a")), recoverySlice(t, "b", recoveryObject("b"))
			decoy := recoverySlice(t, "a", recoveryObject("foreign-a"))
			decoy.Namespace = "other"
			elsewhere := recoverySlice(t, "elsewhere", recoveryObject("foreign-elsewhere"))
			elsewhere.Namespace = "other"
			var reads []string
			lists := 0
			cli := recoveryClient(t, interceptor.Funcs{
				Get: func(ctx context.Context, reader client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					require.IsType(t, &apiv1.ResourceSlice{}, obj)
					assert.Equal(t, recoveryNamespace, key.Namespace)
					require.NotEmpty(t, key.Name, "malformed references must not become empty-name reads")
					reads = append(reads, key.Name)
					if key.Name == "fatal" {
						return tt.readError
					}
					return reader.Get(ctx, key, obj, opts...)
				},
				List: func(ctx context.Context, reader client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					lists++
					return reader.List(ctx, list, opts...)
				},
			}, a, b, decoy, elsewhere)
			e := &Executor{Reader: cli}
			got, flag, err := e.fetchCurrentSynthesisResSlices(context.Background(), comp)
			if tt.readError != nil {
				require.ErrorIs(t, err, tt.readError)
				assert.Empty(t, got, "fatal reads must not expose partial history")
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantFlag, flag)
				var names []string
				for _, slice := range got {
					names = append(names, slice.Name)
					assert.Equal(t, recoveryNamespace, slice.Namespace)
					expected := map[string]*apiv1.ResourceSlice{"a": a, "b": b}[slice.Name]
					require.NotNil(t, expected)
					assert.Equal(t, expected.Spec, slice.Spec)
				}
				assert.Equal(t, tt.wantNames, names)
			}
			assert.Equal(t, tt.wantReads, reads)
			assert.Zero(t, lists, "history loading must not discover unreferenced slices")
			assert.Equal(t, before, comp, "history loading must not mutate the baseline")
			recoveryAssertHistoryUnchanged(t, cli, a, b, decoy, elsewhere)
		})
	}
}

type recoveryFixture struct {
	comp  *apiv1.Composition
	syn   *apiv1.Synthesizer
	input *corev1.ConfigMap
}

func newRecoveryFixture(refs []*apiv1.ResourceSliceRef, flag bool) *recoveryFixture {
	when := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	input := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "input", Namespace: recoveryNamespace, ResourceVersion: "42",
			Annotations: map[string]string{
				"eno.azure.io/revision": "23", "eno.azure.io/synthesizer-generation": "7",
				"eno.azure.io/composition-generation": "11",
			},
		},
		Data: map[string]string{"value": "input"},
	}
	revs := []apiv1.InputRevisions{*apiv1.NewInputRevisions(input, "config")}
	return &recoveryFixture{
		input: input,
		syn: &apiv1.Synthesizer{
			ObjectMeta: metav1.ObjectMeta{Name: "synth", Generation: 7},
			Spec: apiv1.SynthesizerSpec{Refs: []apiv1.Ref{{
				Key: "config", Resource: apiv1.ResourceRef{Kind: "ConfigMap", Version: "v1"},
			}}},
		},
		comp: &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{Name: "comp", Namespace: recoveryNamespace, UID: "comp-uid", Generation: 11},
			Spec: apiv1.CompositionSpec{
				Synthesizer: apiv1.SynthesizerRef{Name: "synth"},
				Bindings: []apiv1.Binding{{
					Key: "config", Resource: apiv1.ResourceBinding{Name: input.Name, Namespace: input.Namespace},
				}},
			},
			Status: apiv1.CompositionStatus{
				InputRevisions: revs,
				Simplified:     &apiv1.SimplifiedStatus{Status: "Synthesizing"},
				PreviousSynthesis: &apiv1.Synthesis{
					UUID: "older-uuid", Synthesized: when.DeepCopy(), TombstoneRecoveryRequired: true,
				},
				CurrentSynthesis: &apiv1.Synthesis{
					UUID: "baseline-uuid", Synthesized: when.DeepCopy(), ResourceSlices: refs,
					TombstoneRecoveryRequired: flag, ObservedCompositionGeneration: 10,
					ObservedSynthesizerGeneration: 6, InputRevisions: []apiv1.InputRevisions{{Key: "config", ResourceVersion: "41"}},
					Results: []apiv1.Result{{Message: "historical warning", Severity: "warning", Tags: map[string]string{"phase": "old"}}},
				},
				InFlightSynthesis: &apiv1.Synthesis{
					UUID: "next-uuid", Initialized: when.DeepCopy(), Attempts: 2, Deferred: true,
					ObservedCompositionGeneration: 11, ObservedSynthesizerGeneration: 6, InputRevisions: revs,
				},
			},
		},
	}
}

func (f *recoveryFixture) client(t *testing.T, funcs interceptor.Funcs, history ...*apiv1.ResourceSlice) client.WithWatch {
	t.Helper()
	objects := []client.Object{f.comp, f.syn, f.input}
	for _, slice := range history {
		objects = append(objects, slice)
	}
	cli := recoveryClient(t, funcs, objects...)
	f.comp = f.stored(t, cli)
	return cli
}

func (f *recoveryFixture) env() *Env {
	return &Env{
		CompositionName: f.comp.Name, CompositionNamespace: f.comp.Namespace,
		SynthesisUUID: f.comp.Status.InFlightSynthesis.UUID,
	}
}

func (f *recoveryFixture) stored(t *testing.T, cli client.Client) *apiv1.Composition {
	t.Helper()
	comp := &apiv1.Composition{}
	require.NoError(t, cli.Get(context.Background(), client.ObjectKeyFromObject(f.comp), comp))
	return comp
}

func (f *recoveryFixture) assertPublished(t *testing.T, cli client.Client, output *krmv1.ResourceList, flag bool) (*apiv1.Composition, []apiv1.Manifest) {
	t.Helper()
	comp := f.stored(t, cli)
	require.NotNil(t, comp.Status.CurrentSynthesis)
	require.NotNil(t, comp.Status.CurrentSynthesis.Synthesized)
	assert.False(t, comp.Status.CurrentSynthesis.Synthesized.IsZero())
	expected := f.comp.DeepCopy()
	expected.Status.PreviousSynthesis = expected.Status.CurrentSynthesis
	expected.Status.CurrentSynthesis = expected.Status.InFlightSynthesis
	expected.Status.InFlightSynthesis = nil
	current := expected.Status.CurrentSynthesis
	current.Synthesized = comp.Status.CurrentSynthesis.Synthesized
	current.ResourceSlices = comp.Status.CurrentSynthesis.ResourceSlices
	current.ObservedSynthesizerGeneration = f.syn.Generation
	current.InputRevisions = []apiv1.InputRevisions{*apiv1.NewInputRevisions(f.input, "config")}
	current.TombstoneRecoveryRequired = flag
	current.Results = nil
	for _, result := range output.Results {
		current.Results = append(current.Results, apiv1.Result{Message: result.Message, Severity: result.Severity, Tags: result.Tags})
	}
	assert.Equal(t, expected.Status, comp.Status)
	assert.Equal(t, f.comp.Spec, comp.Spec)

	var manifests []apiv1.Manifest
	seen := map[string]bool{}
	desired := map[string]*unstructured.Unstructured{}
	for _, obj := range output.Items {
		desired[obj.GetName()] = obj
	}
	for _, ref := range comp.Status.CurrentSynthesis.ResourceSlices {
		require.NotNil(t, ref)
		require.NotEmpty(t, ref.Name)
		require.False(t, seen[ref.Name], "duplicate published reference")
		seen[ref.Name] = true
		slice := &apiv1.ResourceSlice{}
		require.NoError(t, cli.Get(context.Background(), client.ObjectKey{Namespace: comp.Namespace, Name: ref.Name}, slice))
		assert.Equal(t, comp.Namespace, slice.Namespace)
		owner := metav1.GetControllerOf(slice)
		require.NotNil(t, owner)
		assert.Equal(t, comp.UID, owner.UID)
		assert.Equal(t, comp.Name, owner.Name)
		assert.Equal(t, "Composition", owner.Kind)
		assert.Contains(t, slice.Finalizers, "eno.azure.io/cleanup")
		require.NotEmpty(t, slice.Spec.Resources)
		for _, manifest := range slice.Spec.Resources {
			obj := &unstructured.Unstructured{}
			require.NoError(t, obj.UnmarshalJSON([]byte(manifest.Manifest)))
			assert.Equal(t, "ConfigMap", obj.GetKind())
			assert.Equal(t, "v1", obj.GetAPIVersion())
			assert.Equal(t, recoveryNamespace, obj.GetNamespace())
			require.NotEmpty(t, obj.GetName())
			if !manifest.Deleted {
				want := desired[obj.GetName()]
				require.NotNil(t, want, "published an undesired live resource")
				js, err := want.MarshalJSON()
				require.NoError(t, err)
				assert.JSONEq(t, string(js), manifest.Manifest)
			}
			manifests = append(manifests, manifest)
		}
	}
	return comp, manifests
}

func recoveryOutput(names ...string) *krmv1.ResourceList {
	output := &krmv1.ResourceList{
		Kind: krmv1.ResourceListKind, APIVersion: krmv1.SchemeGroupVersion.String(),
		Results: []*krmv1.Result{{Message: "output warning", Severity: "warning", Tags: map[string]string{"phase": "new"}}},
	}
	for _, name := range names {
		obj := recoveryObject(name)
		obj.Object["data"] = map[string]any{"value": "desired-" + name}
		output.Items = append(output.Items, obj)
	}
	return output
}

func recoveryAssertManifests(t *testing.T, manifests []apiv1.Manifest, want map[string]bool) {
	t.Helper()
	got := map[string]bool{}
	for _, manifest := range manifests {
		obj := &unstructured.Unstructured{}
		require.NoError(t, obj.UnmarshalJSON([]byte(manifest.Manifest)))
		_, duplicate := got[obj.GetName()]
		require.False(t, duplicate, "duplicate manifest for %s", obj.GetName())
		got[obj.GetName()] = manifest.Deleted
	}
	assert.Equal(t, want, got)
}

func TestRecoveryExecutorPublication(t *testing.T) {
	type testCase struct {
		name      string
		noCurrent bool
		refs      []*apiv1.ResourceSliceRef
		inherited bool
		outputs   []string
		wantFlag  bool
		want      map[string]bool
	}
	tests := []testCase{
		{name: "healthy-false", refs: recoveryRefs("history-a", "history-b"), outputs: []string{"a", "b"}, want: map[string]bool{"a": false, "b": false}},
		{name: "healthy-inherited", refs: recoveryRefs("history-a", "history-b"), inherited: true, outputs: []string{"a", "b"}, wantFlag: true, want: map[string]bool{"a": false, "b": false}},
		{name: "new-composition", noCurrent: true, outputs: []string{"d"}, wantFlag: true, want: map[string]bool{"d": false}},
		{name: "all-unavailable", refs: []*apiv1.ResourceSliceRef{nil, {}, {Name: "missing-c"}}, outputs: []string{"d"}, wantFlag: true, want: map[string]bool{"d": false}},
		{name: "zero-output-with-history", refs: recoveryRefs("history-a", "history-b"), want: map[string]bool{"a": true, "b": true}},
		{name: "zero-output-with-missing-history", refs: recoveryRefs("history-a", "missing-c", "history-b"), wantFlag: true, want: map[string]bool{"a": true, "b": true}},
		{name: "zero-output-all-unavailable", refs: []*apiv1.ResourceSliceRef{nil, {}, {Name: "missing-c"}}, wantFlag: true, want: map[string]bool{}},
		{name: "zero-output-no-current", noCurrent: true, wantFlag: true, want: map[string]bool{}},
		{name: "zero-output-nil-reference-list", want: map[string]bool{}},
		{name: "zero-output-empty-reference-list", refs: []*apiv1.ResourceSliceRef{}, want: map[string]bool{}},
		{name: "zero-output-inherited", inherited: true, wantFlag: true, want: map[string]bool{}},
	}
	for position, label := range []string{"first", "middle", "last"} {
		refs := recoveryRefs("history-a", "history-b")
		damaged := []*apiv1.ResourceSliceRef{nil, {}, {Name: "missing-c"}}
		refs = append(refs[:position], append(damaged, refs[position:]...)...)
		tests = append(tests, testCase{
			name: "mixed-" + label, refs: refs, outputs: []string{"a", "d"}, wantFlag: true,
			want: map[string]bool{"a": false, "b": true, "d": false},
		})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRecoveryFixture(tt.refs, tt.inherited)
			if tt.noCurrent {
				f.comp.Status.CurrentSynthesis = nil
			}
			a := recoverySlice(t, "history-a", recoveryObject("a"))
			b := recoverySlice(t, "history-b", recoveryObject("b"))
			decoy := recoverySlice(t, "missing-c", recoveryObject("c"))
			decoy.Namespace = "other"
			creates, handlerCalls := 0, 0
			cli := f.client(t, interceptor.Funcs{
				Create: func(ctx context.Context, writer client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if _, ok := obj.(*apiv1.ResourceSlice); ok {
						creates++
					}
					return writer.Create(ctx, obj, opts...)
				},
			}, a, b, decoy)
			output := recoveryOutput(tt.outputs...)
			e := &Executor{Reader: cli, Writer: cli, Handler: func(_ context.Context, syn *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
				handlerCalls++
				assert.Equal(t, f.syn.Name, syn.Name)
				require.Len(t, input.Items, 1)
				assert.Equal(t, f.input.Name, input.Items[0].GetName())
				assert.Equal(t, "config", input.Items[0].GetAnnotations()["eno.azure.io/input-key"])
				return output, nil
			}}
			require.NoError(t, e.Synthesize(context.Background(), f.env()))
			stored, manifests := f.assertPublished(t, cli, output, tt.wantFlag)
			recoveryAssertManifests(t, manifests, tt.want)
			for _, manifest := range manifests {
				if manifest.Deleted {
					assert.Contains(t, []string{a.Spec.Resources[0].Manifest, b.Spec.Resources[0].Manifest}, manifest.Manifest)
				}
			}
			assert.Equal(t, creates, len(stored.Status.CurrentSynthesis.ResourceSlices))
			assert.Equal(t, 1, handlerCalls)
			if len(tt.want) == 0 {
				assert.Empty(t, stored.Status.CurrentSynthesis.ResourceSlices)
				assert.Zero(t, creates)
			}
			recoveryAssertHistoryUnchanged(t, cli, a, b, decoy)
		})
	}
}

func TestRecoveryExecutorFlagPersistsAcrossSyntheses(t *testing.T) {
	a := recoverySlice(t, "history-a", recoveryObject("a"))
	f := newRecoveryFixture(recoveryRefs("history-a", "missing"), false)
	cli := f.client(t, interceptor.Funcs{}, a)
	output := recoveryOutput("a", "d")
	handlerCalls := 0
	e := &Executor{Reader: cli, Writer: cli, Handler: func(context.Context, *apiv1.Synthesizer, *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		handlerCalls++
		return output, nil
	}}
	require.NoError(t, e.Synthesize(context.Background(), f.env()))
	first, manifests := f.assertPublished(t, cli, output, true)
	recoveryAssertManifests(t, manifests, map[string]bool{"a": false, "d": false})
	assert.False(t, first.Status.PreviousSynthesis.TombstoneRecoveryRequired)
	history := []*apiv1.ResourceSlice{a}
	for _, ref := range first.Status.CurrentSynthesis.ResourceSlices {
		slice := &apiv1.ResourceSlice{}
		require.NoError(t, cli.Get(context.Background(), client.ObjectKey{Namespace: first.Namespace, Name: ref.Name}, slice))
		history = append(history, slice.DeepCopy())
	}
	first.Status.InFlightSynthesis = f.comp.Status.InFlightSynthesis.DeepCopy()
	first.Status.InFlightSynthesis.UUID = "later-uuid"
	require.NoError(t, cli.Status().Update(context.Background(), first))
	f.comp = first.DeepCopy()
	require.NoError(t, e.Synthesize(context.Background(), f.env()))
	second, manifests := f.assertPublished(t, cli, output, true)
	recoveryAssertManifests(t, manifests, map[string]bool{"a": false, "d": false})
	assert.True(t, second.Status.PreviousSynthesis.TombstoneRecoveryRequired)
	assert.Equal(t, 2, handlerCalls)
	recoveryAssertHistoryUnchanged(t, cli, history...)
}

func TestRecoveryExecutorOutputFailureDoesNotPromote(t *testing.T) {
	for _, failure := range []string{"handler", "error-result", "invalid-output"} {
		for _, flag := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/flag-%t", failure, flag), func(t *testing.T) {
				a := recoverySlice(t, "history-a", recoveryObject("a"))
				f := newRecoveryFixture(recoveryRefs("history-a", "missing"), flag)
				reads, creates, updates, handlerCalls := 0, 0, 0, 0
				cli := f.client(t, interceptor.Funcs{
					Get: func(ctx context.Context, reader client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*apiv1.ResourceSlice); ok {
							reads++
						}
						return reader.Get(ctx, key, obj, opts...)
					},
					Create: func(ctx context.Context, writer client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						creates++
						return writer.Create(ctx, obj, opts...)
					},
					SubResourceUpdate: func(ctx context.Context, writer client.Client, name string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						updates++
						return writer.SubResource(name).Update(ctx, obj, opts...)
					},
				}, a)
				output := recoveryOutput("d")
				var handlerErr error
				var wantResults []apiv1.Result
				switch failure {
				case "handler":
					handlerErr = fmt.Errorf("handler failed")
					wantResults = []apiv1.Result{{Message: "Synthesizer error: handler failed", Severity: "error"}}
				case "error-result":
					output.Results[0].Severity = krmv1.ResultSeverityError
					output.Results[0].Message = "rejected output"
					wantResults = []apiv1.Result{{Message: "rejected output", Severity: "error", Tags: output.Results[0].Tags}}
				case "invalid-output":
					output.Items[0].SetKind("")
				}
				e := &Executor{Reader: cli, Writer: cli, Handler: func(context.Context, *apiv1.Synthesizer, *krmv1.ResourceList) (*krmv1.ResourceList, error) {
					handlerCalls++
					return output, handlerErr
				}}
				err := e.Synthesize(context.Background(), f.env())
				require.Error(t, err)
				if handlerErr != nil {
					assert.ErrorIs(t, err, handlerErr)
				}
				assert.Equal(t, 1, handlerCalls)
				assert.Zero(t, reads, "failed output must not be used to diff history")
				assert.Zero(t, creates)
				stored := f.stored(t, cli)
				expected := f.comp.DeepCopy()
				if failure == "invalid-output" {
					assert.Zero(t, updates)
				} else {
					assert.Equal(t, 1, updates)
					require.NotNil(t, stored.Status.InFlightSynthesis)
					require.NotNil(t, stored.Status.InFlightSynthesis.Synthesized)
					expected.Status.InFlightSynthesis.Synthesized = stored.Status.InFlightSynthesis.Synthesized
					expected.Status.InFlightSynthesis.ObservedSynthesizerGeneration = f.syn.Generation
					expected.Status.InFlightSynthesis.InputRevisions = []apiv1.InputRevisions{*apiv1.NewInputRevisions(f.input, "config")}
					expected.Status.InFlightSynthesis.Results = wantResults
				}
				assert.Equal(t, expected.Status, stored.Status)
				assert.Equal(t, flag, stored.Status.CurrentSynthesis.TombstoneRecoveryRequired)
				recoveryAssertHistoryUnchanged(t, cli, a)
			})
		}
	}
}

func TestRecoveryExecutorHistoryFailureDoesNotPublish(t *testing.T) {
	for _, failure := range []struct {
		name string
		err  error
	}{
		{"forbidden", apierrors.NewForbidden(schema.GroupResource{Group: apiv1.SchemeGroupVersion.Group, Resource: "resourceslices"}, "fatal", fmt.Errorf("denied"))},
		{"timeout", apierrors.NewTimeoutError("history timed out", 1)},
		{"generic", fmt.Errorf("historical read failed")},
		{"malformed-json", nil},
	} {
		for position, label := range []string{"first", "middle", "last"} {
			t.Run(failure.name+"/"+label, func(t *testing.T) {
				a := recoverySlice(t, "history-a", recoveryObject("a"))
				b := recoverySlice(t, "history-b", recoveryObject("b"))
				fatal := recoverySlice(t, "fatal", recoveryObject("c"))
				if failure.name == "malformed-json" {
					fatal.Spec.Resources[0].Manifest = `{"apiVersion":`
				}
				refs := recoveryRefs("history-a", "history-b")
				refs = append(refs[:position], append(recoveryRefs("fatal"), refs[position:]...)...)
				refs = append([]*apiv1.ResourceSliceRef{nil}, refs...)
				f := newRecoveryFixture(refs, false)
				creates, updates, handlerCalls := 0, 0, 0
				cli := f.client(t, interceptor.Funcs{
					Get: func(ctx context.Context, reader client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*apiv1.ResourceSlice); ok && key.Name == "fatal" && failure.err != nil {
							return failure.err
						}
						return reader.Get(ctx, key, obj, opts...)
					},
					Create: func(ctx context.Context, writer client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						creates++
						return writer.Create(ctx, obj, opts...)
					},
					SubResourceUpdate: func(ctx context.Context, writer client.Client, name string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						updates++
						return writer.SubResource(name).Update(ctx, obj, opts...)
					},
				}, a, b, fatal)
				e := &Executor{Reader: cli, Writer: cli, Handler: func(context.Context, *apiv1.Synthesizer, *krmv1.ResourceList) (*krmv1.ResourceList, error) {
					handlerCalls++
					return recoveryOutput("a", "d"), nil
				}}
				err := e.Synthesize(context.Background(), f.env())
				require.Error(t, err)
				if failure.err != nil {
					assert.ErrorIs(t, err, failure.err)
				} else {
					assert.ErrorContains(t, err, "decoding resource 0 of slice fatal")
				}
				assert.Equal(t, 1, handlerCalls)
				assert.Zero(t, creates, "a fatal baseline must not publish partial output")
				assert.Zero(t, updates)
				assert.Equal(t, f.comp.Status, f.stored(t, cli).Status)
				recoveryAssertHistoryUnchanged(t, cli, a, b, fatal)
			})
		}
	}
}

func TestRecoveryExecutorWriteFailureDoesNotPublish(t *testing.T) {
	for _, tt := range []struct {
		name        string
		failAt      int
		terminating bool
	}{
		{name: "first-create", failAt: 1},
		{name: "second-create", failAt: 2},
		{name: "namespace-terminating", failAt: 1, terminating: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := recoverySlice(t, "history-a", recoveryObject("a"))
			f := newRecoveryFixture(recoveryRefs("history-a", "missing"), false)
			failure := fmt.Errorf("slice create failed")
			if tt.terminating {
				failure = &apierrors.StatusError{ErrStatus: metav1.Status{
					Status: metav1.StatusFailure, Reason: metav1.StatusReasonForbidden,
					Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{Type: corev1.NamespaceTerminatingCause}}},
				}}
			}
			creates, successes, updates, handlerCalls := 0, 0, 0, 0
			cli := f.client(t, interceptor.Funcs{
				Create: func(ctx context.Context, writer client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					require.IsType(t, &apiv1.ResourceSlice{}, obj)
					creates++
					if creates == tt.failAt {
						return failure
					}
					err := writer.Create(ctx, obj, opts...)
					if err == nil {
						successes++
					}
					return err
				},
				SubResourceUpdate: func(ctx context.Context, writer client.Client, name string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
					updates++
					return writer.SubResource(name).Update(ctx, obj, opts...)
				},
			}, a)
			output := recoveryOutput("a", "d")
			// The first ConfigMap fits the Kubernetes size limit but forces a second ResourceSlice.
			require.NoError(t, unstructured.SetNestedField(output.Items[0].Object, strings.Repeat("x", maxSliceJsonBytes), "data", "payload"))
			e := &Executor{Reader: cli, Writer: cli, Handler: func(context.Context, *apiv1.Synthesizer, *krmv1.ResourceList) (*krmv1.ResourceList, error) {
				handlerCalls++
				return output, nil
			}}
			err := e.Synthesize(context.Background(), f.env())
			if tt.terminating {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, failure)
				assert.ErrorContains(t, err, fmt.Sprintf("creating resource slice %d", tt.failAt-1))
			}
			assert.Equal(t, tt.failAt, creates)
			assert.Equal(t, tt.failAt-1, successes)
			assert.Equal(t, 1, handlerCalls)
			assert.Zero(t, updates)
			assert.Equal(t, f.comp.Status, f.stored(t, cli).Status)
			// Already-created slices need not be rolled back; only publication is atomic.
			recoveryAssertHistoryUnchanged(t, cli, a)
		})
	}
}

func TestRecoveryExecutorStatusPublication(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		name := "conflict-once"
		if permanent {
			name = "permanent-status-error"
		}
		t.Run(name, func(t *testing.T) {
			a := recoverySlice(t, "history-a", recoveryObject("a"))
			b := recoverySlice(t, "history-b", recoveryObject("b"))
			f := newRecoveryFixture(recoveryRefs("history-a", "missing", "history-b"), false)
			gr := schema.GroupResource{Group: apiv1.SchemeGroupVersion.Group, Resource: "compositions"}
			failure := apierrors.NewForbidden(gr, f.comp.Name, fmt.Errorf("status denied"))
			var attempts []*apiv1.Composition
			creates, handlerCalls := 0, 0
			cli := f.client(t, interceptor.Funcs{
				Create: func(ctx context.Context, writer client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					require.IsType(t, &apiv1.ResourceSlice{}, obj)
					creates++
					return writer.Create(ctx, obj, opts...)
				},
				SubResourceUpdate: func(ctx context.Context, writer client.Client, name string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
					require.Equal(t, "status", name)
					comp, ok := obj.(*apiv1.Composition)
					require.True(t, ok)
					attempts = append(attempts, comp.DeepCopy())
					if permanent {
						return failure
					}
					if len(attempts) == 1 {
						return apierrors.NewConflict(gr, comp.Name, fmt.Errorf("resource version conflict"))
					}
					return writer.SubResource(name).Update(ctx, obj, opts...)
				},
			}, a, b)
			output := recoveryOutput("a", "d")
			e := &Executor{Reader: cli, Writer: cli, Handler: func(context.Context, *apiv1.Synthesizer, *krmv1.ResourceList) (*krmv1.ResourceList, error) {
				handlerCalls++
				return output, nil
			}}
			err := e.Synthesize(context.Background(), f.env())
			if permanent {
				require.ErrorIs(t, err, failure)
				require.Len(t, attempts, 1)
				assert.Equal(t, f.comp.Status, f.stored(t, cli).Status)
			} else {
				require.NoError(t, err)
				require.Len(t, attempts, 2)
				_, manifests := f.assertPublished(t, cli, output, true)
				recoveryAssertManifests(t, manifests, map[string]bool{"a": false, "b": true, "d": false})
				assert.Equal(t, attempts[0].Status.CurrentSynthesis.ResourceSlices, attempts[1].Status.CurrentSynthesis.ResourceSlices)
			}
			assert.Equal(t, 1, handlerCalls)
			assert.Equal(t, 1, creates)
			for _, attempt := range attempts {
				assert.Nil(t, attempt.Status.InFlightSynthesis)
				assert.True(t, attempt.Status.CurrentSynthesis.TombstoneRecoveryRequired)
				assert.Equal(t, f.comp.Status.CurrentSynthesis, attempt.Status.PreviousSynthesis)
			}
			recoveryAssertHistoryUnchanged(t, cli, a, b)
		})
	}
}

func TestRecoveryExecutorSkippedPreservesFlag(t *testing.T) {
	for _, reason := range []string{"canceled", "uuid-mismatch"} {
		for _, during := range []bool{false, true} {
			for _, flag := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/during-%t/flag-%t", reason, during, flag), func(t *testing.T) {
					a := recoverySlice(t, "history-a", recoveryObject("a"))
					f := newRecoveryFixture(recoveryRefs("history-a", "missing"), flag)
					env := f.env()
					change := func(comp *apiv1.Composition) {
						if reason == "canceled" {
							now := metav1.Now()
							comp.Status.InFlightSynthesis.Canceled = &now
						} else {
							comp.Status.InFlightSynthesis.UUID = "different-uuid"
						}
					}
					if !during {
						change(f.comp)
					}
					cli := f.client(t, interceptor.Funcs{}, a)
					expected := f.comp.DeepCopy()
					handlerCalls := 0
					e := &Executor{Reader: cli, Writer: cli, Handler: func(context.Context, *apiv1.Synthesizer, *krmv1.ResourceList) (*krmv1.ResourceList, error) {
						handlerCalls++
						stored := f.stored(t, cli)
						change(stored)
						require.NoError(t, cli.Status().Update(context.Background(), stored))
						expected = stored.DeepCopy()
						return recoveryOutput("a", "d"), nil
					}}
					require.NoError(t, e.Synthesize(context.Background(), env))
					if during {
						assert.Equal(t, 1, handlerCalls)
					} else {
						assert.Zero(t, handlerCalls)
					}
					stored := f.stored(t, cli)
					assert.Equal(t, expected.Status, stored.Status)
					assert.Equal(t, flag, stored.Status.CurrentSynthesis.TombstoneRecoveryRequired)
					recoveryAssertHistoryUnchanged(t, cli, a)
				})
			}
		}
	}
}
