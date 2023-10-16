package v1

type SecretKeyRef struct {
	// +required
	Name string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// +optional
	Key string `json:"key,omitempty"`
}

type GeneratorRef struct {
	Name string `json:"name,omitempty"`
}

type InputRef struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name,omitempty"`
}
