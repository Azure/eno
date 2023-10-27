package v1

type GeneratorRef struct {
	Name string `json:"name,omitempty"`
}

type InputRef struct {
	Name     string       `json:"name,omitempty"`
	Resource *ResourceRef `json:"resource,omitempty"`
}

type ResourceRef struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name,omitempty"`
}

type GeneratedResourceSliceRef struct {
	Name string `json:"name,omitempty"`
}
