package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	s := NewStore(p)

	if got, err := s.Load(); err != nil || len(got.Forwards) != 0 {
		t.Fatalf("empty load: got=%v err=%v", got, err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	want := Forward{
		ID:         "fwd_0001",
		Kubeconfig: "/tmp/k.config",
		Namespace:  "default",
		Kind:       "Pod",
		Object:     "pod-1",
		Bind:       "127.0.0.1",
		Ports:      []PortMap{{Local: 8080, Remote: 80}},
		StartedAt:  now,
		Status:     "ready",
	}
	if err := s.Mutate(func(st *State) error {
		st.Forwards = append(st.Forwards, want)
		return nil
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	if _, err := os.Stat(p); err != nil {
		t.Errorf("state.json missing: %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(got.Forwards) != 1 {
		t.Fatalf("got %d forwards, want 1", len(got.Forwards))
	}
	if got.Forwards[0].ID != want.ID {
		t.Errorf("ID = %q, want %q", got.Forwards[0].ID, want.ID)
	}
	if got.Forwards[0].Status != "ready" {
		t.Errorf("Status = %q", got.Forwards[0].Status)
	}
}

func TestStoreAtomicDoesNotLeaveTempFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	s := NewStore(p)

	if err := s.Mutate(func(st *State) error {
		st.Forwards = append(st.Forwards, Forward{ID: "x"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Check for any leftover temp files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "state.json" {
			t.Errorf("unexpected file in state dir: %q", e.Name())
		}
	}
}