package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"

	apiv1 "github.com/Azure/eno/api/v1"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
)

type Env struct {
	CompositionName      string
	CompositionNamespace string
	SynthesisUUID        string
	SynthesisAttempt     int
}

func LoadEnv() *Env {
	attempt, _ := strconv.Atoi(os.Getenv("SYNTHESIS_ATTEMPT"))
	return &Env{
		CompositionName:      os.Getenv("COMPOSITION_NAME"),
		CompositionNamespace: os.Getenv("COMPOSITION_NAMESPACE"),
		SynthesisUUID:        os.Getenv("SYNTHESIS_UUID"),
		SynthesisAttempt:     attempt,
	}
}

type SynthesizerHandle func(context.Context, *apiv1.Synthesizer, *krmv1.ResourceList) (*krmv1.ResourceList, error)

func NewExecHandler() SynthesizerHandle {
	return func(ctx context.Context, s *apiv1.Synthesizer, rl *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		stdin := &bytes.Buffer{}
		stdout := &bytes.Buffer{}

		err := json.NewEncoder(stdin).Encode(rl)
		if err != nil {
			return nil, err
		}

		command := s.Spec.Command
		if len(command) == 0 {
			command = []string{"synthesize"}
		}

		if s.Spec.ExecTimeout != nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, s.Spec.ExecTimeout.Duration)
			defer cancel()
		}

		cmd := exec.CommandContext(ctx, command[0], command[1:]...)
		cmd.Stdin = stdin
		cmd.Stderr = os.Stdout // logger uses stderr, so use stdout to avoid race condition
		cmd.Stdout = stdout
		err = cmd.Run()
		if err != nil {
			return nil, err
		}

		output := &krmv1.ResourceList{}
		err = json.NewDecoder(stdout).Decode(output)
		if err != nil {
			return nil, err
		}

		return output, nil
	}
}
