package synthesis

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	apiv1 "github.com/Azure/eno/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/exec"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

type SynthesizerConnection interface {
	Synthesize(ctx context.Context, syn *apiv1.Synthesizer, pod *corev1.Pod, inputsJson []byte) (io.Reader, error)
}

type SynthesizerConnectionFunc func(ctx context.Context, syn *apiv1.Synthesizer, pod *corev1.Pod, inputsJson []byte) (io.Reader, error)

func (s SynthesizerConnectionFunc) Synthesize(ctx context.Context, syn *apiv1.Synthesizer, pod *corev1.Pod, inputsJson []byte) (io.Reader, error) {
	return s(ctx, syn, pod, inputsJson)
}

type SynthesizerPodConnection struct {
	execClient rest.Interface
	scheme     *runtime.Scheme
	restConfig *rest.Config
}

func NewSynthesizerConnection(mgr ctrl.Manager) (*SynthesizerPodConnection, error) {
	gvk := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}
	execClient, err := apiutil.RESTClientForGVK(gvk, false, mgr.GetConfig(), serializer.NewCodecFactory(mgr.GetScheme()), mgr.GetHTTPClient())
	if err != nil {
		return nil, err
	}

	return &SynthesizerPodConnection{
		execClient: execClient,
		scheme:     mgr.GetScheme(),
		restConfig: mgr.GetConfig(),
	}, nil
}

func (s *SynthesizerPodConnection) Synthesize(ctx context.Context, syn *apiv1.Synthesizer, pod *corev1.Pod, inputsJson []byte) (io.Reader, error) {
	req := s.execClient.
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
		}, runtime.NewParameterCodec(s.scheme))

	executor, err := remotecommand.NewSPDYExecutor(s.restConfig, "POST", req.URL())
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
		// TODO: Unit tests
		if errors.As(err, e) {
			msg := truncateString(strings.TrimSpace(stderr.String()), 256)
			return nil, fmt.Errorf("command exited with status %d - stderr: %s", e.Code, msg)
		}
		return nil, fmt.Errorf("starting stream: %w", err)
	}

	return stdout, nil
}
