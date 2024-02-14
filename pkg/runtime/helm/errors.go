package helm

import "errors"

var (
	ErrChartNotFound    = errors.New("the requested chart could not be loaded")
	ErrRenderAction     = errors.New("the chart could not be rendered with the given values")
	ErrCannotParseChart = errors.New("helm produced a set of manifests that is not parseable")
)
