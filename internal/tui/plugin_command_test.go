package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPluginCommandAutocompleteSurfacesPluginCommands verifies that a
// plugin's commands/<name>.md becomes a /<plugin>:<name> entry visible to
// the autocomplete engine. The TUI command dispatcher uses the same
// discovery path, so a passing test here means /<plugin>:<command> works
// end-to-end without any plugin-specific code.
func TestPluginCommandAutocompleteSurfacesPluginCommands(t *testing.T) {
	cwd := t.TempDir()
	cmdDir := filepath.Join(cwd, ".forge", "plugins", "demo", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "ping.md"), []byte("Ping the user"), 0o644); err != nil {
		t.Fatal(err)
	}

	suggestions := Suggest("/d", cwd)
	found := false
	for _, s := range suggestions {
		if s == "/demo:ping" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("autocomplete did not surface /demo:ping, got %v", suggestions)
	}
}

// TestPluginCommandPrefixFiltersOnTypedPluginName makes sure the autocomplete
// only suggests plugin commands whose `/<plugin>:<command>` form starts with
// what the user typed — same prefix rule as the static commands.
func TestPluginCommandPrefixFiltersOnTypedPluginName(t *testing.T) {
	cwd := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(cwd, ".forge", "plugins", name, "commands")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "do.md"), []byte("Do."), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	suggestions := Suggest("/alpha", cwd)
	for _, s := range suggestions {
		if strings.HasPrefix(s, "/beta") {
			t.Errorf("typed /alpha but got %q in suggestions", s)
		}
	}
	if !sliceContains(suggestions, "/alpha:do") {
		t.Errorf("missing /alpha:do in %v", suggestions)
	}
}

func sliceContains(s []string, want string) bool {
	for _, x := range s {
		if x == want {
			return true
		}
	}
	return false
}
