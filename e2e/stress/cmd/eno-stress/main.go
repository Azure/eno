// Command eno-stress executes YAML-driven Eno stress plans against a live cluster.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Azure/eno/e2e/stress/internal/runner"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := execute(ctx, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "eno-stress: %v\n", err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, arguments []string) error {
	if len(arguments) == 0 {
		return usageError()
	}
	command := arguments[0]
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	options := runner.Options{Output: func(format string, values ...any) { fmt.Printf(format, values...) }}
	set.StringVar(&options.Kubeconfig, "kubeconfig", "", "path to the kubeconfig for the live cluster (defaults to standard kubeconfig loading)")

	switch command {
	case "validate":
		set.StringVar(&options.PlanPath, "plan", "", "path to the stress test plan")
		set.BoolVar(&options.ServerDryRun, "server-dry-run", false, "send rendered resources to Kubernetes server-side dry-run")
	case "prepare":
		set.StringVar(&options.PlanPath, "plan", "", "path to the stress test plan")
		set.StringVar(&options.StatePath, "state", "state.json", "path to durable run state")
		set.StringVar(&options.RunID, "run-id", "", "optional stable run ID")
	case "run", "status", "cleanup":
		set.StringVar(&options.StatePath, "state", "state.json", "path to durable run state")
	default:
		return usageError()
	}
	set.Usage = func() {
		fmt.Fprintf(set.Output(), "usage: eno-stress %s [flags]\n", command)
		set.PrintDefaults()
	}
	if err := set.Parse(arguments[1:]); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", set.Args())
	}
	if (command == "validate" || command == "prepare") && options.PlanPath == "" {
		return fmt.Errorf("--plan is required")
	}

	switch command {
	case "validate":
		return runner.Validate(ctx, options)
	case "prepare":
		return runner.Prepare(ctx, options)
	case "run":
		return runner.Run(ctx, options)
	case "status":
		return runner.Status(options)
	case "cleanup":
		return runner.Cleanup(ctx, options)
	default:
		return usageError()
	}
}

func usageError() error {
	return fmt.Errorf("usage: eno-stress <validate|prepare|run|status|cleanup> [flags]")
}
