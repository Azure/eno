package helm

import "helm.sh/helm/v3/pkg/action"

type RenderOption func(*action.Install)

func (ro RenderOption) apply(a *action.Install) {
	ro(a)
}

func WithNamespace(ns string) RenderOption {
	return RenderOption(func(a *action.Install) {
		if a == nil {
			return
		}
		a.Namespace = ns
	})
}
