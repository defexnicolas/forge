package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSampleClaudePluginEndToEnd loads testdata/sample-plugin via the
// regular workspace discovery path and asserts that every Claude-compatible
// component the user expects to work, works. Failures here mean a Claude
// Code plugin would arrive in forge and not light up — exactly the gap the
// user wanted closed.
//
// The workspace .forge/plugins/ dir is symlinked (or copied) from
// testdata so the discovery code sees a real on-disk plugin, not a
// fixture-specific code path.
func TestSampleClaudePluginEndToEnd(t *testing.T) {
	cwd := t.TempDir()
	stagePlugin(t, cwd, "sample-plugin")

	mgr := &Manager{cwd: cwd}
	plugins, err := mgr.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	plugin, ok := findPlugin(plugins, "sample-plugin")
	if !ok {
		t.Fatalf("sample-plugin not discovered. got: %#v", plugins)
	}

	t.Run("manifest fields parsed", func(t *testing.T) {
		if plugin.Name != "sample-plugin" {
			t.Errorf("Name = %q", plugin.Name)
		}
		if plugin.Version != "0.1.0" {
			t.Errorf("Version = %q", plugin.Version)
		}
		if plugin.UserConfig["GREETING"] != "hello-from-sample" {
			t.Errorf("UserConfig[GREETING] = %q", plugin.UserConfig["GREETING"])
		}
	})

	t.Run("supported components list includes every shipped subdir", func(t *testing.T) {
		got := plugin.SupportedComponents()
		want := []string{"commands", "agents", "hooks", ".mcp.json", "skills", ".lsp.json", "settings.json", "output-styles"}
		for _, w := range want {
			if !contains(got, w) {
				t.Errorf("missing supported component %q (got %v)", w, got)
			}
		}
	})

	t.Run("compatibility status is ready", func(t *testing.T) {
		if got := plugin.CompatibilityStatus(); got != "ready" {
			t.Errorf("CompatibilityStatus = %q, want ready", got)
		}
	})

	t.Run("commands/ loads markdown content", func(t *testing.T) {
		cmds := LoadCommands(plugin.Path)
		if len(cmds) != 1 || cmds[0].Name != "hello" {
			t.Fatalf("expected exactly hello command, got %#v", cmds)
		}
		if !strings.Contains(cmds[0].Content, "Hello from sample-plugin") {
			t.Errorf("content lost: %q", cmds[0].Content)
		}
		if cmds[0].Source != "sample-plugin" {
			t.Errorf("Source = %q", cmds[0].Source)
		}
	})

	t.Run("agents/ enumerates the .md files", func(t *testing.T) {
		agents := LoadAgents(plugin.Path)
		if len(agents) != 1 || agents[0].Name != "explorer-fast" {
			t.Fatalf("expected explorer-fast agent, got %#v", agents)
		}
	})

	t.Run("hooks.json found at hooks/hooks.json", func(t *testing.T) {
		path := plugin.HooksPath()
		if path == "" {
			t.Fatal("HooksPath empty — hooks/ not detected")
		}
		if !strings.HasSuffix(filepath.ToSlash(path), "hooks/hooks.json") {
			t.Errorf("HooksPath = %q, want suffix hooks/hooks.json", path)
		}
	})

	t.Run("skills/ surfaces the dir to the skills manager", func(t *testing.T) {
		dir := plugin.SkillsDir()
		if dir == "" {
			t.Fatal("SkillsDir empty — skills/ not detected")
		}
		if _, err := os.Stat(filepath.Join(dir, "test-skill", "SKILL.md")); err != nil {
			t.Errorf("skills/test-skill/SKILL.md not found: %v", err)
		}
	})

	t.Run(".mcp.json discoverable", func(t *testing.T) {
		if plugin.MCPConfigPath() == "" {
			t.Error("MCPConfigPath empty — .mcp.json not detected")
		}
	})

	t.Run(".lsp.json discoverable", func(t *testing.T) {
		if plugin.LSPConfigPath() == "" {
			t.Error("LSPConfigPath empty — .lsp.json not detected")
		}
	})

	t.Run("settings.json parses raw, lists tools", func(t *testing.T) {
		settings, err := plugin.LoadSettings()
		if err != nil {
			t.Fatalf("LoadSettings: %v", err)
		}
		if !contains(settings.Permissions.Allow, "echo *") {
			t.Errorf("allow lost: %v", settings.Permissions.Allow)
		}
		if !contains(settings.Permissions.Deny, "rm -rf *") {
			t.Errorf("deny lost: %v", settings.Permissions.Deny)
		}
	})

	t.Run("MergePluginSettings expands ${vars}", func(t *testing.T) {
		merged, errs := MergePluginSettings([]Plugin{plugin})
		if len(errs) > 0 {
			t.Fatalf("MergePluginSettings errors: %v", errs)
		}
		// user_config var must expand.
		if merged.Env["SAMPLE_PLUGIN_GREETING"] != "hello-from-sample" {
			t.Errorf("user_config var not expanded: %q", merged.Env["SAMPLE_PLUGIN_GREETING"])
		}
		// CLAUDE_PLUGIN_ROOT must expand to absolute plugin path.
		root := merged.Env["SAMPLE_PLUGIN_ROOT"]
		if !filepath.IsAbs(root) || !strings.HasSuffix(filepath.ToSlash(root), "sample-plugin") {
			t.Errorf("CLAUDE_PLUGIN_ROOT did not expand to plugin path: %q", root)
		}
		if !contains(merged.AllowTools, "echo *") {
			t.Errorf("allow lost in merged: %v", merged.AllowTools)
		}
	})

	t.Run("output-styles enumerated", func(t *testing.T) {
		styles := plugin.ListOutputStyles()
		if len(styles) != 1 || styles[0].Name != "concise" {
			t.Errorf("expected concise output-style, got %#v", styles)
		}
		if styles[0].Plugin != "sample-plugin" {
			t.Errorf("style Plugin = %q", styles[0].Plugin)
		}
	})
}

// stagePlugin copies testdata/<name> into <cwd>/.forge/plugins/<name>.
// Using a copy (not a symlink) keeps the test portable across Windows and
// CI runners where symlink permissions vary.
func stagePlugin(t *testing.T, cwd, name string) {
	t.Helper()
	src := filepath.Join("testdata", name)
	dst := filepath.Join(cwd, ".forge", "plugins", name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	copyTree(t, src, dst)
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	info, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat %s: %v", src, err)
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			copyTree(t, filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()))
		}
		return
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func findPlugin(list []Plugin, name string) (Plugin, bool) {
	for _, p := range list {
		if p.Name == name {
			return p, true
		}
	}
	return Plugin{}, false
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
