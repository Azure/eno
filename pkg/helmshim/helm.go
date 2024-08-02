package helmshim

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Azure/eno/pkg/function"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
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

func RenderChart(opts ...RenderOption) error {
	a := action.NewInstall(&action.Configuration{})
	a.ReleaseName = "eno-helm-shim"
	a.Namespace = "default"
	a.DryRun = true
	a.Replace = true
	a.ClientOnly = true
	a.IncludeCRDs = true

	o := &options{
		Action:     a,
		ValuesFunc: inputsToValues,
		ChartPath:  "./chart",
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
	if o.Writer == nil {
		o.Writer = function.NewDefaultOutputWriter()
	}

	c, err := loader.Load(o.ChartPath)
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
		o.Writer.Add(m)
	}

	return o.Writer.Write()
}

func inputsToValues(i *function.InputReader) (map[string]any, error) {
	m := map[string]any{}
	for k, o := range i.All() {
		m[k] = o.Object
	}
	return m, nil
}
