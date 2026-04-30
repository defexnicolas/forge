package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPluginCompatibilityComponents(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "demo-plugin")
	for _, rel := range []string{"commands", "agents", "hooks", ".mcp.json", "skills", ".lsp.json", "settings.json"} {
		target := filepath.Join(pluginDir, rel)
		if filepath.Ext(rel) == ".json" {
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	plugin := Plugin{Name: "demo", Path: pluginDir}
	if got := plugin.CompatibilityStatus(); got != "partial" {
		t.Fatalf("CompatibilityStatus() = %q, want partial", got)
	}
	if len(plugin.SupportedComponents()) != 4 {
		t.Fatalf("expected 4 supported components, got %#v", plugin.SupportedComponents())
	}
	if len(plugin.PendingComponents()) != 3 {
		t.Fatalf("expected 3 pending components, got %#v", plugin.PendingComponents())
	}
}
