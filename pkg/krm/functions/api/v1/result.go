package v1

// Result result
//
// swagger:model Result
type Result struct {
	// Message is a human readable message.
	Message string `json:"message"`

	// TODO: Missing `Field`, avoiding it for now because interface{} fields are not allowed by deepcopy-gen.

	// file
	// +optional
	File *ResultFile `json:"file,omitempty"`

	// resource ref
	// +optional
	ResourceRef *ResultResourceRef `json:"resourceRef,omitempty"`

	// Severity is the severity of a result:
	//
	// "error": indicates an error result.
	// "warning": indicates a warning result.
	// "info": indicates an informational result.
	//
	// Enum: [error warning info]
	// +optional
	Severity string `json:"severity,omitempty"`

	// Tags is an unstructured key value map stored with a result that may be set
	// by external tools to store and retrieve arbitrary metadata.
	// +optional
	Tags map[string]string `json:"tags,omitempty"`
}

const (

	// ResultSeverityError captures enum value "error"
	ResultSeverityError string = "error"

	// ResultSeverityWarning captures enum value "warning"
	ResultSeverityWarning string = "warning"

	// ResultSeverityInfo captures enum value "info"
	ResultSeverityInfo string = "info"
)

// ResultFile File references a file containing the resource.
//
// swagger:model ResultFile
type ResultFile struct {
	// Path is the OS agnostic, slash-delimited, relative path.
	// e.g. `some-dir/some-file.yaml`.
	Path string `json:"path"`

	// Index of the object in a multi-object YAML file.
	// +optional
	Index float64 `json:"index,omitempty"`
}

// ResultResourceRef ResourceRef is the metadata for referencing a Kubernetes object
// associated with a result.
//
// swagger:model ResultResourceRef
type ResultResourceRef struct {

	// APIVersion refers to the `apiVersion` field of the object manifest.
	APIVersion string `json:"apiVersion"`

	// Kind refers to the `kind` field of the object.
	Kind string `json:"kind"`

	// Name refers to the `metadata.name` field of the object manifest.
	Name string `json:"name"`

	// Namespace refers to the `metadata.namespace` field of the object manifest.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}
