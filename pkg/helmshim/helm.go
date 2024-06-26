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
	o := function.NewDefaultOutputWriter()
	i, err := function.NewDefaultInputReader()
	if err != nil {
		return err
	}

	if err := renderChart(i, o, opts...); err != nil {
		return err
	}
	return o.Write()
}

func renderChart(r *function.InputReader, w *function.OutputWriter, opts ...RenderOption) error {
	a := action.NewInstall(&action.Configuration{})
	a.ReleaseName = "eno-helm-shim"
	a.Namespace = "default"
	a.DryRun = true
	a.Replace = true
	a.ClientOnly = true
	a.IncludeCRDs = true

	o := &options{Action: a, ValuesFunc: inputsToValues, ChartPath: "."}
	for _, opt := range opts {
		opt.apply(o)
	}

	c, err := loader.Load(o.ChartPath)
	if err != nil {
		return errors.Join(ErrChartNotFound, err)
	}

	vals, err := o.ValuesFunc(r)
	if err != nil {
		return errors.Join(ErrConstructingValues, err)
	}

	rel, err := a.Run(c, vals)
	if err != nil {
		return errors.Join(ErrRenderAction, err)
	}

	b := bytes.NewBufferString(rel.Manifest)
	d := yaml.NewYAMLToJSONDecoder(b)
	for {
		m := &unstructured.Unstructured{}
		err = d.Decode(m)
		if err == io.EOF {
			break
		} else if err != nil {
			return errors.Join(ErrCannotParseChart, err)
		}
		w.Add(m)
	}

	return nil
}

func inputsToValues(i *function.InputReader) (map[string]any, error) {
	m := map[string]any{}
	for k, o := range i.All() {
		m[k] = o
	}
	return m, nil
}
