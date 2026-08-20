package plan

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func Load(path string) (*Plan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading plan: %w", err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)

	result := &Plan{}
	if err := decoder.Decode(result); err != nil {
		return nil, fmt.Errorf("decoding plan: %w", err)
	}
	result.ApplyDefaults()
	if err := result.Validate(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return result, nil
}
