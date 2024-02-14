package helm

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func RenderChart(path string, vals map[string]interface{}, opts ...RenderOption) ([]*unstructured.Unstructured, error) {
	a := getInstallAction(opts...)
	c, err := loader.Load(path)
	if err != nil {
		return nil, errors.Join(ErrChartNotFound, err)
	}

	rel, err := a.Run(c, vals)
	if err != nil {
		return nil, errors.Join(ErrRenderAction, err)
	}

	res := []*unstructured.Unstructured{}
	b := bytes.NewBufferString(strings.TrimSpace(rel.Manifest))
	dec := yaml.NewDecoder(b)
	for {
		m := map[string]interface{}{}
		err := dec.Decode(&m)
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, errors.Join(ErrCannotParseChart, err)
		}
		res = append(res, &unstructured.Unstructured{Object: m})
	}
	return res, nil
}

func getInstallAction(opts ...RenderOption) *action.Install {
	a := action.NewInstall(&action.Configuration{})
	a.ReleaseName = "release-name"
	a.Namespace = "default"
	a.DryRun = true
	a.Replace = true
	a.ClientOnly = true
	a.IncludeCRDs = true

	for _, opt := range opts {
		opt.apply(a)
	}

	return a
}
