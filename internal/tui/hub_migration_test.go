package tui

import (
	"os"
	"path/filepath"
	"testing"

	"forge/internal/globalconfig"
)

func writeWorkspaceTOML(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".forge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanWorkspacesDetectsThemeAndModels(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	wsA := t.TempDir()
	writeWorkspaceTOML(t, wsA, `
theme = "dracula"

[models]
chat = "qwen-7b"
planner = "qwen-32b"
`)
	wsB := t.TempDir()
	writeWorkspaceTOML(t, wsB, `
[context.yarn]
profile = "14B"
`)
	wsC := t.TempDir()
	// Empty config -- no proposal should come from this one.
	writeWorkspaceTOML(t, wsC, `default_agent = "build"`)

	state := HubState{
		Pinned: []string{wsA},
		RecentWorkspaces: []RecentWorkspace{
			{Path: wsB},
			{Path: wsC},
		},
	}
	props := scanWorkspacesForMigration(state)
	if len(props) != 2 {
		t.Fatalf("expected 2 proposals (A theme/models, B yarn), got %d: %#v", len(props), props)
	}
	for _, p := range props {
		switch p.WorkspacePath {
		case wsA:
			if p.Theme != "dracula" {
				t.Errorf("wsA theme = %q, want dracula", p.Theme)
			}
			if p.Models["chat"] != "qwen-7b" || p.Models["planner"] != "qwen-32b" {
				t.Errorf("wsA models lost: %v", p.Models)
			}
		case wsB:
			if p.YarnProfile != "14B" {
				t.Errorf("wsB yarn.profile = %q, want 14B", p.YarnProfile)
			}
		default:
			t.Errorf("unexpected proposal for %q", p.WorkspacePath)
		}
	}
}

func TestApplyMigrationWritesGlobalAndScrubsWorkspace(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	ws := t.TempDir()
	writeWorkspaceTOML(t, ws, `
theme = "dracula"

[models]
chat = "qwen-7b"
explorer = "qwen-7b"
`)
	props := scanWorkspacesForMigration(HubState{Pinned: []string{ws}})
	if len(props) != 1 {
		t.Fatalf("expected 1 proposal, got %d", len(props))
	}
	if err := applyMigrationProposals(props); err != nil {
		t.Fatalf("applyMigrationProposals: %v", err)
	}

	g, err := globalconfig.Load()
	if err != nil {
		t.Fatalf("global load: %v", err)
	}
	if g.Theme == nil || *g.Theme != "dracula" {
		t.Errorf("global theme not migrated: %+v", g.Theme)
	}
	if g.Models["chat"] != "qwen-7b" {
		t.Errorf("global models not migrated: %+v", g.Models)
	}

	// Workspace toml should no longer have theme or models.
	data, err := os.ReadFile(filepath.Join(ws, ".forge", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if containsLine(body, "theme") {
		t.Errorf("workspace toml should not contain 'theme' anymore:\n%s", body)
	}
	if containsLine(body, "[models]") {
		t.Errorf("workspace toml should not contain '[models]' anymore:\n%s", body)
	}
}

func TestApplyMigrationEmptyProposalsIsNoop(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	if err := applyMigrationProposals(nil); err != nil {
		t.Fatalf("empty migration should be noop, got %v", err)
	}
}

func TestDismissMigrationFlipsFlag(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	store := &memoryHubStateStore{}
	m := &shellModel{
		hubState:           HubState{},
		options:            ShellOptions{StateStore: store},
		mode:               modeHub,
		activeView:         viewMigration,
		migrationProposals: []migrationProposal{{WorkspacePath: "/x", Theme: "dracula"}},
	}
	m.dismissMigration()
	if !m.hubState.MigrationDone {
		t.Error("MigrationDone should be true after dismiss")
	}
	if !store.state.MigrationDone {
		t.Error("MigrationDone must be persisted on dismiss")
	}
	if m.activeView != viewExplorer {
		t.Errorf("activeView should snap back to Explorer, got %v", m.activeView)
	}
}

// containsLine reports whether s has any line that contains the substring
// `needle` after trimming. Lazy text comparison kept here so the tests
// don't have to do the toml->map dance themselves.
func containsLine(s, needle string) bool {
	for _, line := range splitLines(s) {
		if line == "" {
			continue
		}
		if startsWith(trimSpace(line), needle) {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
