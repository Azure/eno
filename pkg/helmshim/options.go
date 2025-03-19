package helmshim

import (
	"flag"

	"github.com/Azure/eno/pkg/function"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

type ValuesFunc func(*function.InputReader) (map[string]any, error)

// ChartLoader is the function for loading a helm chart.
type ChartLoader func() (*chart.Chart, error)

func defaultChartLoader() (*chart.Chart, error) {
	return loader.Load("./chart")
}

type options struct {
	Action      *action.Install
	ValuesFunc  ValuesFunc
	ChartLoader ChartLoader
	Reader      *function.InputReader
	Writer      *function.OutputWriter
}

type RenderOption func(*options)

func (ro RenderOption) apply(o *options) {
	ro(o)
}

func WithNamespace(ns string) RenderOption {
	return RenderOption(func(o *options) {
		if o == nil {
			return
		}
		o.Action.Namespace = ns
	})
}

func WithValuesFunc(fn ValuesFunc) RenderOption {
	return RenderOption(func(o *options) {
		if o == nil {
			return
		}
		o.ValuesFunc = fn
	})
}

func WithChartPath(path string) RenderOption {
	return RenderOption(func(o *options) {
		if o == nil {
			return
		}
		o.ChartLoader = func() (*chart.Chart, error) {
			return loader.Load(path)
		}
	})
}

func WithChartLoader(cl ChartLoader) RenderOption {
	return RenderOption(func(o *options) {
		if o == nil {
			return
		}
		o.ChartLoader = cl
	})
}

func WithInputReader(r *function.InputReader) RenderOption {
	return RenderOption(func(o *options) {
		if o == nil {
			return
		}
		o.Reader = r
	})
}

func WithOutputWriter(w *function.OutputWriter) RenderOption {
	return RenderOption(func(o *options) {
		if o == nil {
			return
		}
		o.Writer = w
	})
}

func WithReleaseName(rn string) RenderOption {
	return RenderOption(func(o *options) {
		if o == nil {
			return
		}
		o.Action.ReleaseName = rn
	})
}

func ParseFlags() []RenderOption {
	ns := flag.String("ns", "default", "Namespace for the Helm release")
	chart := flag.String("chart", ".", "Path to the Helm chart")
	flag.Parse()

	return []RenderOption{WithNamespace(*ns), WithChartPath(*chart)}
}
