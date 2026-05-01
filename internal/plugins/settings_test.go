package plugins

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func writeJSON(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSettingsParsesPermissionsAndEnv(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "settings.json"), `{
  "permissions": {
    "allow": ["Bash(go test *)", "Bash(make build)"],
    "deny":  ["Bash(rm -rf *)"],
    "ask":   ["Bash(curl *)"]
  },
  "env": {
    "FORGE_FLAG": "1",
    "API_BASE":   "https://api.example.com"
  },
  "irrelevant_key": "ignored on purpose"
}`)
	plugin := Plugin{Name: "x", Path: dir}
	settings, err := plugin.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if !reflect.DeepEqual(settings.Permissions.Allow, []string{"Bash(go test *)", "Bash(make build)"}) {
		t.Errorf("allow not parsed: %#v", settings.Permissions.Allow)
	}
	if !reflect.DeepEqual(settings.Permissions.Deny, []string{"Bash(rm -rf *)"}) {
		t.Errorf("deny not parsed: %#v", settings.Permissions.Deny)
	}
	if settings.Env["FORGE_FLAG"] != "1" || settings.Env["API_BASE"] != "https://api.example.com" {
		t.Errorf("env not parsed: %#v", settings.Env)
	}
}

func TestLoadSettingsMissingIsEmpty(t *testing.T) {
	plugin := Plugin{Name: "no-settings", Path: t.TempDir()}
	settings, err := plugin.LoadSettings()
	if err != nil {
		t.Fatalf("missing settings.json should not error, got %v", err)
	}
	if len(settings.Permissions.Allow) != 0 || len(settings.Env) != 0 {
		t.Errorf("expected zero-value settings, got %#v", settings)
	}
}

func TestLoadSettingsMalformedIsError(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "settings.json"), `{not json`)
	plugin := Plugin{Name: "bad", Path: dir}
	if _, err := plugin.LoadSettings(); err == nil {
		t.Fatal("expected error for malformed settings.json")
	}
}

func TestMergePluginSettingsExpandsVarsAndConcatenates(t *testing.T) {
	dirA := t.TempDir()
	writeJSON(t, filepath.Join(dirA, "settings.json"), `{
  "permissions": {"allow": ["Bash(${user_config.CMD} *)"]},
  "env":         {"PATH_LIKE": "${CLAUDE_PLUGIN_ROOT}/bin"}
}`)
	dirB := t.TempDir()
	writeJSON(t, filepath.Join(dirB, "settings.json"), `{
  "permissions": {"allow": ["Bash(other-tool *)"], "deny": ["Bash(rm *)"]},
  "env":         {"PATH_LIKE": "loses-because-A-was-first", "FROM_B": "ok"}
}`)

	pluginA := Plugin{Name: "a", Path: dirA, UserConfig: map[string]string{"CMD": "my-tool"}}
	pluginB := Plugin{Name: "b", Path: dirB}

	merged, errs := MergePluginSettings([]Plugin{pluginA, pluginB})
	if len(errs) != 0 {
		t.Fatalf("unexpected merge errors: %v", errs)
	}

	allow := append([]string(nil), merged.AllowTools...)
	sort.Strings(allow)
	want := []string{"Bash(my-tool *)", "Bash(other-tool *)"}
	sort.Strings(want)
	if !reflect.DeepEqual(allow, want) {
		t.Errorf("allow merge wrong: got %v, want %v", allow, want)
	}
	if !reflect.DeepEqual(merged.DenyTools, []string{"Bash(rm *)"}) {
		t.Errorf("deny merge wrong: got %v", merged.DenyTools)
	}
	// Env: A wins because it comes first.
	if got := merged.Env["PATH_LIKE"]; got != filepath.ToSlash(dirA)+"/bin" && got != dirA+"/bin" {
		// Path may be platform-specific; accept either separator.
		t.Errorf("env A should win and expand CLAUDE_PLUGIN_ROOT, got %q", got)
	}
	if merged.Env["FROM_B"] != "ok" {
		t.Errorf("env from B not merged: %q", merged.Env["FROM_B"])
	}
}

func TestListOutputStylesPicksMdAndJsonOnly(t *testing.T) {
	dir := t.TempDir()
	stylesDir := filepath.Join(dir, "output-styles")
	if err := os.MkdirAll(stylesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"concise.md", "verbose.json", "ignore.txt"} {
		if err := os.WriteFile(filepath.Join(stylesDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plugin := Plugin{Name: "demo", Path: dir}
	styles := plugin.ListOutputStyles()
	if len(styles) != 2 {
		t.Fatalf("expected 2 styles, got %d: %#v", len(styles), styles)
	}
	names := []string{styles[0].Name, styles[1].Name}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"concise", "verbose"}) {
		t.Errorf("unexpected styles: %v", names)
	}
}
