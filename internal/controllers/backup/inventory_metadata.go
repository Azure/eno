package backup

import (
	"fmt"
	"maps"
	"strings"

	apiv1 "github.com/Azure/eno/api/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metavalidation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

type inventorySourceComposition struct {
	Name        string                   `json:"name"`
	UID         types.UID                `json:"uid"`
	Labels      map[string]string        `json:"labels,omitempty"`
	Annotations map[string]string        `json:"annotations,omitempty"`
	Symphony    *inventorySourceSymphony `json:"symphony,omitempty"`
}

type inventorySourceSymphony struct {
	Name string    `json:"name"`
	UID  types.UID `json:"uid"`
}

func makeInventorySource(comp *apiv1.Composition) (*inventorySourceComposition, error) {
	source := &inventorySourceComposition{
		Name:        comp.Name,
		UID:         comp.UID,
		Labels:      maps.Clone(comp.Labels),
		Annotations: maps.Clone(comp.Annotations),
	}
	if owner := metav1.GetControllerOf(comp); owner != nil && owner.Kind == "Symphony" {
		gv, err := schema.ParseGroupVersion(owner.APIVersion)
		if err != nil {
			return nil, fmt.Errorf("invalid Symphony controller apiVersion %q: %w", owner.APIVersion, err)
		}
		if gv.Group == apiv1.SchemeGroupVersion.Group {
			if errs := validation.IsDNS1035Label(gv.Version); len(errs) != 0 {
				return nil, fmt.Errorf("invalid Symphony controller version %q: %s", gv.Version, strings.Join(errs, "; "))
			}
			source.Symphony = &inventorySourceSymphony{Name: owner.Name, UID: owner.UID}
		}
	}
	if err := source.validate(); err != nil {
		return nil, err
	}
	return source, nil
}

func (source *inventorySourceComposition) validate() error {
	if source == nil {
		return nil
	}
	if err := validateInventorySourceIdentity(source.Name, source.UID); err != nil {
		return err
	}
	if errs := metavalidation.ValidateLabels(source.Labels, field.NewPath("labels")); len(errs) != 0 {
		return errs.ToAggregate()
	}
	if errs := apivalidation.ValidateAnnotations(source.Annotations, field.NewPath("annotations")); len(errs) != 0 {
		return errs.ToAggregate()
	}
	if len(source.Labels) == 0 {
		source.Labels = nil
	}
	if len(source.Annotations) == 0 {
		source.Annotations = nil
	}
	if source.Symphony != nil {
		if err := validateInventorySourceIdentity(source.Symphony.Name, source.Symphony.UID); err != nil {
			return fmt.Errorf("symphony: %w", err)
		}
	}
	return nil
}

func validateInventorySourceIdentity(name string, uid types.UID) error {
	if errs := validation.IsDNS1123Subdomain(name); len(errs) != 0 {
		return fmt.Errorf("invalid name %q: %s", name, strings.Join(errs, "; "))
	}
	if uid == "" {
		return fmt.Errorf("uid must be nonempty")
	}
	return nil
}

func validateInventorySourceWire(source map[string]any) error {
	if source == nil {
		return nil
	}
	for _, key := range []string{"name", "uid"} {
		if _, ok := source[key].(string); !ok {
			return fmt.Errorf("%s must be present and a string", key)
		}
	}
	for _, key := range []string{"labels", "annotations"} {
		if _, err := inventoryStringMap(source, key); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	if value := source["symphony"]; value != nil {
		symphony, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("symphony must be an object")
		}
		for _, key := range []string{"name", "uid"} {
			if _, ok := symphony[key].(string); !ok {
				return fmt.Errorf("symphony.%s must be present and a string", key)
			}
		}
	}
	return nil
}

// Saved ownership only supplies cleanup routing; it never constrains recovery.
func (snapshot *inventorySnapshot) cleanupComposition() (*apiv1.Composition, error) {
	if snapshot == nil || snapshot.Data.SourceComposition == nil || snapshot.Data.SourceComposition.Symphony == nil {
		return nil, nil
	}
	return snapshot.sourceComposition()
}

func (snapshot *inventorySnapshot) sourceComposition() (*apiv1.Composition, error) {
	if snapshot == nil || snapshot.Data.SourceComposition == nil {
		return nil, nil
	}
	source := snapshot.Data.SourceComposition
	if err := source.validate(); err != nil {
		return nil, &invalidInventoryError{fmt.Errorf("sourceComposition: %w", err)}
	}
	if err := validateInventoryLineage(&snapshot.Data); err != nil {
		return nil, &invalidInventoryError{err}
	}
	comp := &apiv1.Composition{
		TypeMeta: metav1.TypeMeta{APIVersion: apiv1.SchemeGroupVersion.String(), Kind: "Composition"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        source.Name,
			Namespace:   snapshot.Data.CompositionNamespace,
			UID:         source.UID,
			Labels:      maps.Clone(source.Labels),
			Annotations: maps.Clone(source.Annotations),
		},
		Spec: apiv1.CompositionSpec{Synthesizer: apiv1.SynthesizerRef{Name: snapshot.Data.SynthesizerName}},
	}
	if source.Symphony != nil {
		comp.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(
			&apiv1.Symphony{ObjectMeta: metav1.ObjectMeta{Name: source.Symphony.Name, UID: source.Symphony.UID}},
			apiv1.SchemeGroupVersion.WithKind("Symphony"),
		)}
	}
	return comp, nil
}
