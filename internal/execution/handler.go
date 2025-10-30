package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"

	apiv1 "github.com/Azure/eno/api/v1"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/go-logr/logr"
)

type Env struct {
	CompositionName      string
	CompositionNamespace string
	SynthesisUUID        string
	Image                string
}

func LoadEnv() *Env {
	return &Env{
		CompositionName:      os.Getenv("COMPOSITION_NAME"),
		CompositionNamespace: os.Getenv("COMPOSITION_NAMESPACE"),
		SynthesisUUID:        os.Getenv("SYNTHESIS_UUID"),
		Image:                os.Getenv("IMAGE"),
	}
}

type SynthesizerHandle func(context.Context, *apiv1.Synthesizer, *krmv1.ResourceList) (*krmv1.ResourceList, error)

func NewExecHandler() SynthesizerHandle {
	return func(ctx context.Context, s *apiv1.Synthesizer, rl *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		logger := logr.FromContextOrDiscard(ctx)
		logger.V(1).Info("starting synthesizer execution",
			"synthesizerName", s.Name,
			"synthesizerImage", s.Spec.Image,
			"inputResourceCount", len(rl.Items))

		stdin := &bytes.Buffer{}
		stdout := &bytes.Buffer{}

		logger.V(1).Info("encoding input for synthesizer")
		err := json.NewEncoder(stdin).Encode(rl)
		if err != nil {
			logger.Error(err, "failed to encode inputs before execution")
			return nil, fmt.Errorf("encoding inputs before execution: %s", err)
		}
		logger.V(1).Info("input encoded successfully", "stdinSize", stdin.Len())

		command := s.Spec.Command
		if len(command) == 0 {
			command = []string{"synthesize"}
			logger.V(1).Info("using default command", "command", command)
		} else {
			logger.V(1).Info("using custom command", "command", command)
		}

		logger.V(1).Info("creating command context", "commandArgs", command)
		cmd := exec.CommandContext(ctx, command[0], command[1:]...)
		cmd.Stdin = stdin
		cmd.Stderr = os.Stdout // logger uses stderr, so use stdout to avoid race condition
		cmd.Stdout = stdout

		logger.V(1).Info("executing synthesizer command", "command", command[0], "args", command[1:])
		err = cmd.Run()
		if errors.Is(err, exec.ErrNotFound) {
			logger.Error(err, "synthesizer command not found", "command", command[0])
			return nil, fmt.Errorf("%w (likely a mismatch between the Synthesizer object and container image)", err)
		}
		if err != nil {
			logger.V(0).Info("stdout buffer contents", "stdout", stdout.String())
			logger.Error(err, "synthesizer command execution failed",
				"command", command,
				"stdoutSize", stdout.Len(),
				"exitCode", cmd.ProcessState.ExitCode())
			return nil, fmt.Errorf("%w (see synthesis pod logs for more details)", err)
		}
		logger.V(1).Info("synthesizer command executed successfully",
			"stdoutSize", stdout.Len(),
			"exitCode", cmd.ProcessState.ExitCode())

		logger.V(1).Info("parsing synthesizer output")
		output := &krmv1.ResourceList{}
		err = json.Unmarshal(stdout.Bytes(), output)
		if err != nil {
			logger.Error(err, "invalid json output from synthesizer", "stdout", stdout.String())
			return nil, fmt.Errorf("the synthesizer process wrote invalid json to stdout")
		}

		logger.V(1).Info("synthesizer execution completed successfully",
			"outputResourceCount", len(output.Items),
			"outputResultCount", len(output.Results))
		return output, nil
	}
}
