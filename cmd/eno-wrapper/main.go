// eno-wrapper is a small wrapper around the generator process to handle interactions with apiserver.
// Essentially it proxies between apiserver and the generator's stdio.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/wrapper"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		installPath    = flag.String("install", "", "install the wrapper")
		shouldGenerate = flag.Bool("generate", false, "run the generator")
		wait           = flag.Bool("wait", false, "sleep forever")
	)
	flag.Parse()
	rand.Seed(time.Now().UnixNano())

	if *shouldGenerate {
		return generate()
	}
	if *installPath != "" {
		return install(*installPath)
	}
	if *wait {
		<-context.Background().Done()
	}

	return nil
}

func install(path string) error {
	self, err := os.Open(os.Args[0])
	if err != nil {
		return err
	}
	defer self.Close()

	dest, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0777)
	if err != nil {
		return err
	}
	defer dest.Close()

	_, err = io.Copy(dest, self)
	return err
}

func generate() error {
	ctx := ctrl.SetupSignalHandler()
	log, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}

	name, ns, genStr := os.Getenv("COMPOSITION_NAME"), os.Getenv("COMPOSITION_NAMESPACE"), os.Getenv("COMPOSITION_GENERATION")
	if name == "" || genStr == "" {
		return errors.New("composition resource name, namespace, and generation are required")
	}
	gen, _ := strconv.ParseInt(genStr, 10, 0)

	// TODO(jordan): Allow rate limiting to be configured
	cli, err := client.New(ctrl.GetConfigOrDie(), client.Options{})
	if err != nil {
		return fmt.Errorf("constructing new k8s client: %w", err)
	}
	if err := apiv1.SchemeBuilder.AddToScheme(cli.Scheme()); err != nil {
		return fmt.Errorf("adding scheme to client: %w", err)
	}

	g := &wrapper.Generator{
		Client:                cli,
		Logger:                zapr.NewLogger(log),
		CompositionName:       name,
		CompositionNamespace:  ns,
		CompositionGeneration: gen,
		Exec:                  spawnChild,
	}
	return g.Generate(ctx)
}

func spawnChild(ctx context.Context, buf []byte) ([]byte, error) {
	// TODO(jordan): Some process sandboxing (no network access, etc.)
	cmd := exec.CommandContext(ctx, "generate")
	cmd.Stdin = bytes.NewBuffer(buf)

	outputBuf := &bytes.Buffer{}
	cmd.Stdout = outputBuf
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("command execution failure: %w", err)
	}
	return outputBuf.Bytes(), nil
}
