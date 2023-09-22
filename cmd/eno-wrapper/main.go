package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
)

// TODO: Generated resource aren't deleted when the composition is for some reason?

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

	name, rv := os.Getenv("COMPOSITION_NAME"), os.Getenv("COMPOSITION_RESOURCE_VERSION")
	if name == "" || rv == "" {
		return errors.New("composition resource name and version are required")
	}

	// Resource loading
	cli, err := client.New(ctrl.GetConfigOrDie(), client.Options{})
	if err != nil {
		return fmt.Errorf("constructing new k8s client: %w", err)
	}
	if err := apiv1.SchemeBuilder.AddToScheme(cli.Scheme()); err != nil {
		return fmt.Errorf("adding scheme to client: %w", err)
	}

	comp := &apiv1.Composition{}
	comp.Name = name
	err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp, &client.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting composition resource: %w", err)
	}

	// Input munging
	inputJson := &bytes.Buffer{}
	inputJsonEnc := json.NewEncoder(inputJson)
	for _, input := range comp.Spec.Inputs {
		ref := &unstructured.Unstructured{}
		ref.SetAPIVersion(input.APIVersion)
		ref.SetKind(input.Kind)
		ref.SetName(input.Name)
		ref.SetNamespace(input.Namespace)
		if err := cli.Get(ctx, client.ObjectKeyFromObject(ref), ref); err != nil {
			// TODO: Write this to a condition
			return fmt.Errorf("getting input resource: %w", err)
		}
		if err := inputJsonEnc.Encode(ref); err != nil {
			return fmt.Errorf("encoding input resource: %w", err)
		}
	}

	// Command execution
	cmd := exec.CommandContext(ctx, "generate")
	cmd.Stdin = bytes.NewBuffer(inputJson.Bytes())

	outputBuf := &bytes.Buffer{}
	cmd.Stdout = outputBuf
	cmd.Stderr = os.Stderr

	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("command execution failure %q - stdout: %s", err, outputBuf.String())
	}

	// Output decoding
	currentResources := map[string]*apiv1.GeneratedResource{}
	dec := json.NewDecoder(outputBuf)
	for {
		raw := &unstructured.Unstructured{}
		if err := dec.Decode(raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("decoding generated resource json: %w", err)
		}

		if raw.GetName() == "" || raw.GetKind() == "" {
			continue
		}

		hash := sha256.Sum256([]byte(raw.GetName() + raw.GetKind()))
		hashStr := hex.EncodeToString(hash[:])[:7]

		res := &apiv1.GeneratedResource{}
		res.Name = fmt.Sprintf("%s-%s", comp.Name, hashStr)
		res.Labels = map[string]string{"composition": comp.Name}

		// Fetch current state
		err = cli.Get(ctx, client.ObjectKeyFromObject(res), res)
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("getting current generated resource state: %w", err)
		}
		if res.Status.DerivedGeneration == comp.Generation {
			continue // already in sync
		}

		// Make changes
		if err := controllerutil.SetControllerReference(res, comp, cli.Scheme()); err != nil {
			return fmt.Errorf("setting owner reference: %w", err)
		}
		js, err := json.Marshal(raw)
		if err != nil {
			return fmt.Errorf("encoding generated resource as json: %w", err)
		}
		res.Spec.Manifest = string(js)
		res.Status.DerivedGeneration = comp.Generation
		currentResources[res.Name] = res
	}

	// Remove orphaned resources
	all := &apiv1.GeneratedResourceList{}
	err = cli.List(ctx, all, client.MatchingLabels{"composition": comp.Name})
	if err != nil {
		return fmt.Errorf("listng current generated resource state: %w", err)
	}
	for _, res := range all.Items {
		if _, ok := currentResources[res.Name]; ok {
			continue
		}
		if res.DeletionTimestamp != nil {
			continue
		}
		err = cli.Delete(ctx, &res)
		if err != nil {
			return fmt.Errorf("deleting orphaned resources: %w", err)
		}
		// TODO: Log
	}

	// Write changes
	for _, res := range currentResources {
		if res.ResourceVersion == "" {
			err = cli.Create(ctx, res)
		} else {
			err = cli.Update(ctx, res)
		}
		if err != nil {
			return fmt.Errorf("storing generated resource: %w", err)
		}
		// TODO: Log
	}

	meta.SetStatusCondition(&comp.Status.Conditions, metav1.Condition{
		Type:               apiv1.CompositionGeneratedConditionType,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: comp.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             "JobCompleted",
		Message:            "the resource generation job completed successfully",
	})

	return cli.Status().Update(ctx, comp)
}
