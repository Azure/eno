package helmshim

import (
	"flag"

	"github.com/Azure/eno/pkg/function"
	"helm.sh/helm/v3/pkg/action"
)

type ValuesFunc func(*function.InputReader) (map[string]any, error)

type options struct {
	Action     *action.Install
	ValuesFunc ValuesFunc
	ChartPath  string
	Reader     *function.InputReader
	Writer     *function.OutputWriter
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
		o.ChartPath = path
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
