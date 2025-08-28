package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	apiv1 "github.com/Azure/eno/api/v1"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
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
		stdin := &bytes.Buffer{}
		stdout := &bytes.Buffer{}

		err := json.NewEncoder(stdin).Encode(rl)
		if err != nil {
			return nil, fmt.Errorf("encoding stdin buffer: %w", err)
		}

		command := s.Spec.Command
		if len(command) == 0 {
			command = []string{"synthesize"}
		}

		cmd := exec.CommandContext(ctx, command[0], command[1:]...)
		cmd.Stdin = stdin
		cmd.Stderr = os.Stdout // logger uses stderr, so use stdout to avoid race condition
		cmd.Stdout = stdout
		err = cmd.Run()
		if err != nil {
			return nil, fmt.Errorf("executing command: %w", err)
		}

		output := &krmv1.ResourceList{}
		err = json.Unmarshal(stdout.Bytes(), output)
		if err != nil {
			return nil, fmt.Errorf("error while parsing synthesizer's stdout as json %q - raw output: %s", err, stdout)
		}

		return output, nil
	}
}
