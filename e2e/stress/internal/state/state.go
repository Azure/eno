package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const SchemaVersion = "v1alpha2"

type State struct {
	SchemaVersion    string                             `json:"schemaVersion"`
	RunID            string                             `json:"runID"`
	PlanName         string                             `json:"planName"`
	PlanPath         string                             `json:"planPath"`
	PlanDigest       string                             `json:"planDigest"`
	CompletionStage  string                             `json:"completionStage"`
	Phase            string                             `json:"phase"`
	CreatedAt        time.Time                          `json:"createdAt"`
	PreparedAt       *time.Time                         `json:"preparedAt,omitempty"`
	WorkloadStart    *time.Time                         `json:"workloadStart,omitempty"`
	CompletedAt      *time.Time                         `json:"completedAt,omitempty"`
	Namespaces       []string                           `json:"namespaces"`
	Resources        []Resource                         `json:"resources"`
	NamespaceInputs  map[string]map[string]*InputTiming `json:"namespaceInputs"`
	CompositionState map[string]*CompositionResult      `json:"compositionState"`
}

type Resource struct {
	LogicalName       string    `json:"logicalName"`
	Phase             string    `json:"phase"`
	APIVersion        string    `json:"apiVersion"`
	Kind              string    `json:"kind"`
	Group             string    `json:"group,omitempty"`
	Version           string    `json:"version"`
	Resource          string    `json:"resource"`
	Scope             string    `json:"scope"`
	Namespace         string    `json:"namespace,omitempty"`
	Name              string    `json:"name"`
	UID               string    `json:"uid"`
	ResourceVersion   string    `json:"resourceVersion"`
	CreationTimestamp time.Time `json:"creationTimestamp"`
	RequestStartedAt  time.Time `json:"requestStartedAt"`
	APIAcknowledgedAt time.Time `json:"apiAcknowledgedAt"`
}

type CompositionResult struct {
	Namespace                      string     `json:"namespace"`
	CompositionName                string     `json:"compositionName"`
	Variation                      string     `json:"variation"`
	InputRevisionObservedAt        *time.Time `json:"inputRevisionObservedAt,omitempty"`
	ExitedMissingInputsAt          *time.Time `json:"exitedMissingInputsAt,omitempty"`
	SynthesisInitializedAt         *time.Time `json:"synthesisInitializedAt,omitempty"`
	SynthesisInitializedObservedAt *time.Time `json:"synthesisInitializedObservedAt,omitempty"`
	SynthesisCompletedAt           *time.Time `json:"synthesisCompletedAt,omitempty"`
	SynthesisCompletedObservedAt   *time.Time `json:"synthesisCompletedObservedAt,omitempty"`
	ReconciledAt                   *time.Time `json:"reconciledAt,omitempty"`
	ReconciledObservedAt           *time.Time `json:"reconciledObservedAt,omitempty"`
	ReadyAt                        *time.Time `json:"readyAt,omitempty"`
	ReadyObservedAt                *time.Time `json:"readyObservedAt,omitempty"`
	OutputObservedAt               *time.Time `json:"outputObservedAt,omitempty"`
	OutputValid                    bool       `json:"outputValid"`
	SynthesisUUID                  string     `json:"synthesisUUID,omitempty"`
	LastStatus                     string     `json:"lastStatus,omitempty"`
	Failure                        string     `json:"failure,omitempty"`
}

type InputTiming struct {
	Name              string    `json:"name"`
	Kind              string    `json:"kind"`
	RequestStartedAt  time.Time `json:"requestStartedAt"`
	APIAcknowledgedAt time.Time `json:"apiAcknowledgedAt"`
	ResourceVersion   string    `json:"resourceVersion"`
	UID               string    `json:"uid"`
}

type Store struct {
	path string
	mu   sync.RWMutex
	data *State
}

func NewStore(path string, data *State) *Store {
	return &Store{path: path, data: data}
}

func Load(path string) (*Store, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading state: %w", err)
	}
	data := &State{}
	if err := json.Unmarshal(raw, data); err != nil {
		return nil, fmt.Errorf("decoding state: %w", err)
	}
	if data.SchemaVersion != SchemaVersion && data.SchemaVersion != "v1alpha1" {
		return nil, fmt.Errorf("unsupported state schema %q", data.SchemaVersion)
	}
	if data.NamespaceInputs == nil {
		data.NamespaceInputs = map[string]map[string]*InputTiming{}
	}
	if data.CompositionState == nil {
		data.CompositionState = map[string]*CompositionResult{}
	}
	return NewStore(path, data), nil
}

func (s *Store) Read(fn func(*State) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fn(s.data)
}

func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fn(s.data); err != nil {
		return err
	}
	return save(s.path, s.data)
}

func (s *Store) Mutate(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(s.data)
}

func (s *Store) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return save(s.path, s.data)
}

func save(path string, data *State) error {
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".eno-stress-state-*")
	if err != nil {
		return fmt.Errorf("creating temporary state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return fmt.Errorf("writing state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("syncing state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing state: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replacing state: %w", err)
	}
	return nil
}

func FileDigest(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
