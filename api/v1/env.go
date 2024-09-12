package v1

// +kubebuilder:validation:XValidation:message="name must match [a-zA-Z_][a-zA-Z0-9_]*",rule="self.name.matches('^[a-zA-Z_][a-zA-Z0-9_]*$')"
type EnvVar struct {
	// +required
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}
