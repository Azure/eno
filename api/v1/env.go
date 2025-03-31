package v1

type EnvVar struct {
	// +required
	// +kubebuilder:validation:MaxLength:=100
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}
