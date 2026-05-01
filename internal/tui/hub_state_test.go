package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHubStatePinIdempotent(t *testing.T) {
	state := HubState{}
	if !state.Pin("/foo") {
		t.Error("Pin /foo should return true on first call")
	}
	if state.Pin("/foo") {
		t.Error("Pin /foo should return false on duplicate")
	}
	if len(state.Pinned) != 1 || state.Pinned[0] != "/foo" {
		t.Errorf("Pinned = %v, want [/foo]", state.Pinned)
	}
}

func TestHubStateUnpinReturnsPresence(t *testing.T) {
	state := HubState{Pinned: []string{"/a", "/b", "/c"}}
	if !state.Unpin("/b") {
		t.Error("Unpin /b should return true")
	}
	if state.Unpin("/b") {
		t.Error("Unpin /b twice should return false the second time")
	}
	if len(state.Pinned) != 2 || state.Pinned[0] != "/a" || state.Pinned[1] != "/c" {
		t.Errorf("Pinned after Unpin = %v, want [/a /c]", state.Pinned)
	}
}

func TestHubStateIsPinned(t *testing.T) {
	state := HubState{Pinned: []string{"/a"}}
	if !state.IsPinned("/a") {
		t.Error("IsPinned /a should be true")
	}
	if state.IsPinned("/b") {
		t.Error("IsPinned /b should be false")
	}
}

func TestHubStateBackwardCompatLoadOldJSON(t *testing.T) {
	// Old format had only LastHubDir + RecentWorkspaces. New code must
	// still load it -- new fields default to zero.
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.json")
	old := `{"last_hub_dir": "/x", "recent_workspaces": [{"path": "/foo", "opened_at": "2025-01-01T00:00:00Z"}]}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewFileHubStateStore(path)
	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if state.LastHubDir != "/x" {
		t.Errorf("LastHubDir = %q", state.LastHubDir)
	}
	if len(state.RecentWorkspaces) != 1 {
		t.Errorf("RecentWorkspaces missing: %v", state.RecentWorkspaces)
	}
	if len(state.Pinned) != 0 {
		t.Errorf("Pinned should default to empty, got %v", state.Pinned)
	}
	if state.MigrationDone {
		t.Error("MigrationDone should default to false")
	}
}

func TestHubStateSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.json")
	store := NewFileHubStateStore(path)

	src := HubState{
		LastHubDir:    "/projects",
		Pinned:        []string{"/projects/a", "/projects/b"},
		MigrationDone: true,
	}
	if err := store.Save(src); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LastHubDir != "/projects" {
		t.Errorf("LastHubDir lost: %q", got.LastHubDir)
	}
	if len(got.Pinned) != 2 || got.Pinned[0] != "/projects/a" {
		t.Errorf("Pinned lost: %v", got.Pinned)
	}
	if !got.MigrationDone {
		t.Error("MigrationDone lost")
	}

	// Sanity: the JSON must contain the new keys for forward debugging.
	raw, _ := os.ReadFile(path)
	var generic map[string]any
	_ = json.Unmarshal(raw, &generic)
	if _, ok := generic["pinned"]; !ok {
		t.Error("'pinned' key missing from saved JSON")
	}
	if _, ok := generic["migration_done"]; !ok {
		t.Error("'migration_done' key missing from saved JSON")
	}
}
