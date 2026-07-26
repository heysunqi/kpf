// Package state manages the on-disk JSON files kpf uses to track
// daemon lifecycle and active forwards.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SchemaVersion is the current state.json schema.
const SchemaVersion = 1

// PortMap describes one (local, remote) port pair on a forward.
type PortMap struct {
	Local  int `json:"local"`
	Remote int `json:"remote"`
}

// Forward is the persistent description of a port-forward.
type Forward struct {
	ID            string    `json:"id"`
	Kubeconfig    string    `json:"kubeconfig"`
	KubeconfigSHA string    `json:"kubeconfig_sha256,omitempty"`
	Namespace     string    `json:"namespace"`
	Kind          string    `json:"kind"`
	Object        string    `json:"object"`
	PodName       string    `json:"pod_name,omitempty"`
	Bind          string    `json:"bind"`
	Ports         []PortMap `json:"ports"`
	StartedAt     time.Time `json:"started_at"`
	Status        string    `json:"status"`
	StatusMessage string    `json:"status_message,omitempty"`
}

// State is the top-level document stored at state.json.
type State struct {
	SchemaVersion int       `json:"schema_version"`
	Forwards      []Forward `json:"forwards"`
}

// Store provides mutex-guarded Load / Save for state.json.
type Store struct {
	mu   sync.Mutex
	path string
}

// NewStore returns a Store rooted at path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Path returns the on-disk path the Store reads/writes.
func (s *Store) Path() string { return s.path }

// Load returns the current state. A missing file yields an empty State.
func (s *Store) Load() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return loadUnsafe(s.path)
}

func loadUnsafe(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{SchemaVersion: SchemaVersion, Forwards: []Forward{}}, nil
		}
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("parse state.json: %w", err)
	}
	if st.SchemaVersion == 0 {
		st.SchemaVersion = SchemaVersion
	}
	if st.Forwards == nil {
		st.Forwards = []Forward{}
	}
	return st, nil
}

// Save persists the state atomically (write to temp + rename).
func (s *Store) Save(st State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return saveAtomic(s.path, st)
}

func saveAtomic(path string, st State) error {
	if st.SchemaVersion == 0 {
		st.SchemaVersion = SchemaVersion
	}
	if st.Forwards == nil {
		st.Forwards = []Forward{}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// Mutate loads the state, applies fn, and writes it back atomically.
func (s *Store) Mutate(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := loadUnsafe(s.path)
	if err != nil {
		return err
	}
	if err := fn(&st); err != nil {
		return err
	}
	return saveAtomic(s.path, st)
}
