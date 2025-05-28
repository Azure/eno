package helmshim

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Azure/eno/pkg/function"
	"helm.sh/helm/v3/pkg/action"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
)

var (
	ErrChartNotFound      = errors.New("the requested chart could not be loaded")
	ErrRenderAction       = errors.New("the chart could not be rendered with the given values")
	ErrCannotParseChart   = errors.New("helm produced a set of manifests that is not parseable")
	ErrConstructingValues = errors.New("error while constructing helm values")
)

// MustRenderChart is the entrypoint to the Helm shim.
//
// The most basic shim cmd's main func only needs one line:
// > helmshim.MustRenderChart(helmshim.ParseFlags()...)
func MustRenderChart(opts ...RenderOption) {
	err := RenderChart(opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

// isNullOrEmptyObject checks if the given unstructured object is equivalent to an empty K8S object.
// This is used when then input helm chart includes an empty target (for example: empty yaml file with comments).
func isNullOrEmptyObject(o *unstructured.Unstructured) bool {
	if o == nil {
		return true
	}
	if len(o.Object) > 0 {
		// if the object has any fields, it is not null
		return false
	}
	b, err := json.Marshal(o)
	if err != nil {
		return false
	}
	return string(b) == "null" || string(b) == "{}"
}

func RenderChart(opts ...RenderOption) error {
	a := action.NewInstall(&action.Configuration{})
	a.ReleaseName = "eno-helm-shim"
	a.Namespace = "default"
	a.DryRun = true
	a.Replace = true
	a.ClientOnly = true
	a.IncludeCRDs = true

	o := &options{
		Action:      a,
		ValuesFunc:  inputsToValues,
		ChartLoader: defaultChartLoader,
	}
	for _, opt := range opts {
		opt.apply(o)
	}

	if o.Reader == nil {
		var err error
		o.Reader, err = function.NewDefaultInputReader()
		if err != nil {
			return err
		}
	}

	var usingDefaultWriter bool
	if o.Writer == nil {
		usingDefaultWriter = true
		o.Writer = function.NewDefaultOutputWriter()
	}

	c, err := o.ChartLoader()
	if err != nil {
		return errors.Join(ErrChartNotFound, err)
	}

	vals, err := o.ValuesFunc(o.Reader)
	if err != nil {
		return errors.Join(ErrConstructingValues, err)
	}

	rel, err := a.Run(c, vals)
	if err != nil {
		return errors.Join(ErrRenderAction, err)
	}

	b := bytes.NewBufferString(rel.Manifest)
	// append manifest from hook
	for _, hook := range rel.Hooks {
		fmt.Fprintf(b, "---\n# Source: %s\n%s\n", hook.Name, hook.Manifest)
	}

	d := yaml.NewYAMLToJSONDecoder(b)
	for {
		m := &unstructured.Unstructured{}
		err = d.Decode(m)
		if err == io.EOF {
			break
		} else if err != nil {
			return errors.Join(ErrCannotParseChart, err)
		}
		if isNullOrEmptyObject(m) {
			continue
		}
		if err := o.Writer.Add(m); err != nil {
			return fmt.Errorf("adding object %s to output writer: %w", m, err)
		}
	}

	if usingDefaultWriter {
		return o.Writer.Write()
	}

	return nil
}

func inputsToValues(i function.Reader) (map[string]any, error) {
	m := map[string]any{}
	for k, o := range i.All() {
		m[k] = o.Object
	}
	return m, nil
}
