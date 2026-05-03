package claw

import (
	"path/filepath"
	"testing"
)

func TestStorePersistsState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claw", "state.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := store.Update(func(state *State) error {
		state.Identity.Name = "OpenClaw"
		state.Enabled = true
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	reloaded, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore reload: %v", err)
	}
	if got := reloaded.Snapshot().Identity.Name; got != "OpenClaw" {
		t.Fatalf("Identity.Name = %q, want OpenClaw", got)
	}
	if !reloaded.Snapshot().Enabled {
		t.Fatal("expected enabled state to persist")
	}
}
