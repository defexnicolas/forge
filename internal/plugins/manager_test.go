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
	supported := plugin.SupportedComponents()
	if len(supported) != 5 {
		t.Fatalf("expected 5 supported components, got %#v", supported)
	}
	// skills must be supported now that the skills manager honors plugin dirs.
	wantSupported := map[string]bool{
		"commands": true, "agents": true, "hooks": true, ".mcp.json": true, "skills": true,
	}
	for _, c := range supported {
		if !wantSupported[c] {
			t.Errorf("unexpected supported component: %q", c)
		}
	}
	pending := plugin.PendingComponents()
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending components (.lsp.json, settings.json), got %#v", pending)
	}
}

func TestPluginCompatibilityReadyWhenNoPending(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "ready-plugin")
	for _, rel := range []string{"commands", "skills"} {
		if err := os.MkdirAll(filepath.Join(pluginDir, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	plugin := Plugin{Name: "ready", Path: pluginDir}
	if got := plugin.CompatibilityStatus(); got != "ready" {
		t.Fatalf("CompatibilityStatus() = %q, want ready (only supported components)", got)
	}
}

func TestPluginCompatibilityDiscoveredWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "empty-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}

	plugin := Plugin{Name: "empty", Path: pluginDir}
	if got := plugin.CompatibilityStatus(); got != "discovered" {
		t.Fatalf("CompatibilityStatus() = %q, want discovered", got)
	}
}

func TestPluginSkillsDir(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "with-skills")
	skillsDir := filepath.Join(pluginDir, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plugin := Plugin{Name: "ws", Path: pluginDir}
	if got := plugin.SkillsDir(); got != skillsDir {
		t.Fatalf("SkillsDir() = %q, want %q", got, skillsDir)
	}

	emptyPlugin := Plugin{Name: "noskills", Path: dir}
	if got := emptyPlugin.SkillsDir(); got != "" {
		t.Fatalf("SkillsDir() on plugin without skills/ should be empty, got %q", got)
	}
}

func TestExpandVars(t *testing.T) {
	plugin := Plugin{
		Name: "my-plugin",
		Path: "/abs/path/my-plugin",
		UserConfig: map[string]string{
			"API_KEY":    "secret",
			"BASE_URL":   "https://example.com",
			"EMPTY_KEY": "",
		},
	}

	cases := map[string]string{
		"":                                                "",
		"plain text":                                      "plain text",
		"path is ${CLAUDE_PLUGIN_ROOT}":                   "path is /abs/path/my-plugin",
		"key=${user_config.API_KEY}":                     "key=secret",
		"${CLAUDE_PLUGIN_ROOT}/bin -k ${user_config.API_KEY} -u ${user_config.BASE_URL}": "/abs/path/my-plugin/bin -k secret -u https://example.com",
		"${user_config.MISSING}":                          "",
		"${user_config.EMPTY_KEY}":                        "",
		"${OTHER_SHELL_VAR} should not be touched":        "${OTHER_SHELL_VAR} should not be touched",
		"${CLAUDE_PLUGIN_ROOT} ${CLAUDE_PLUGIN_ROOT}":     "/abs/path/my-plugin /abs/path/my-plugin",
	}

	for input, want := range cases {
		if got := ExpandVars(plugin, input); got != want {
			t.Errorf("ExpandVars(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestExpandVarsEmptyUserConfig(t *testing.T) {
	plugin := Plugin{Name: "x", Path: "/p"}
	if got := ExpandVars(plugin, "${user_config.ANY}"); got != "" {
		t.Errorf("ExpandVars with nil UserConfig should return empty for user_config refs, got %q", got)
	}
	if got := ExpandVars(plugin, "${CLAUDE_PLUGIN_ROOT}"); got != "/p" {
		t.Errorf("ExpandVars with nil UserConfig should still expand CLAUDE_PLUGIN_ROOT, got %q", got)
	}
}
