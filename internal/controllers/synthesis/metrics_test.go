package synthesis

import (
	"context"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
	prometheustestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetSynthesizerName(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"present", map[string]string{synthesizerNameLabelKey: "my-syn"}, "my-syn"},
		{"empty-value", map[string]string{synthesizerNameLabelKey: ""}, "unknown"},
		{"missing-key", map[string]string{"other": "x"}, "unknown"},
		{"nil-labels", nil, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{}
			pod.Labels = tc.labels
			assert.Equal(t, tc.want, getSynthesizerName(pod))
		})
	}
}

func TestDeletePodEmitsMetrics(t *testing.T) {
	synthesisResults.Reset()
	synthesisDuration.Reset()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, apiv1.SchemeBuilder.AddToScheme(scheme))

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-pod",
			Namespace:         "default",
			Labels:            map[string]string{synthesizerNameLabelKey: "my-syn"},
			CreationTimestamp: metav1.Now(),
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	gc := &podGarbageCollector{client: cli}

	require.NoError(t, gc.deletePod(context.Background(), pod, getSynthesizerName(pod), reasonSuccess, logr.Discard()))

	got := prometheustestutil.ToFloat64(synthesisResults.WithLabelValues("my-syn", string(reasonSuccess)))
	assert.Equal(t, 1.0, got)

	// Histogram observation recorded (one series, one sample).
	assert.Equal(t, 1, prometheustestutil.CollectAndCount(synthesisDuration))
}

func TestDeletePodUnknownSynthesizer(t *testing.T) {
	synthesisResults.Reset()
	synthesisDuration.Reset()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-pod",
			Namespace:         "default",
			CreationTimestamp: metav1.Now(),
			// No synthesizer-name label.
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	gc := &podGarbageCollector{client: cli}

	require.NoError(t, gc.deletePod(context.Background(), pod, getSynthesizerName(pod), reasonOrphaned, logr.Discard()))

	// "unknown" fallback fires; empty-string label is never emitted.
	assert.Equal(t, 1.0, prometheustestutil.ToFloat64(synthesisResults.WithLabelValues("unknown", string(reasonOrphaned))))
	assert.Equal(t, 0.0, prometheustestutil.ToFloat64(synthesisResults.WithLabelValues("", string(reasonOrphaned))))
}
