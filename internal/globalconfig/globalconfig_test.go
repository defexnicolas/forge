package globalconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingReturnsEmpty(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	g, err := Load()
	if err != nil {
		t.Fatalf("missing global.toml should not error, got %v", err)
	}
	if g.Theme != nil || g.Models != nil || g.Yarn != nil {
		t.Errorf("expected zero-value GlobalConfig, got %#v", g)
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	theme := "dracula"
	scope := "user"
	src := GlobalConfig{
		Theme:  &theme,
		Models: map[string]string{"chat": "qwen-7b", "planner": "qwen-32b"},
		Skills: &SkillsDefaults{InstallScope: &scope},
	}
	if err := Save(src); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Theme == nil || *got.Theme != "dracula" {
		t.Errorf("theme not roundtripped: %+v", got.Theme)
	}
	if got.Models["chat"] != "qwen-7b" || got.Models["planner"] != "qwen-32b" {
		t.Errorf("models not roundtripped: %+v", got.Models)
	}
	if got.Skills == nil || got.Skills.InstallScope == nil || *got.Skills.InstallScope != "user" {
		t.Errorf("skills.install_scope not roundtripped: %+v", got.Skills)
	}
}

func TestPathRespectsEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FORGE_GLOBAL_HOME", dir)
	want := filepath.Join(dir, "global.toml")
	if got := Path(); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestCacheDirRespectsEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FORGE_GLOBAL_HOME", dir)
	want := filepath.Join(dir, "cache", "skills")
	if got := CacheDir(); got != want {
		t.Errorf("CacheDir() = %q, want %q", got, want)
	}
}

func TestLoadMalformedReturnsError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FORGE_GLOBAL_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "global.toml"), []byte("not valid toml ===="), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("expected error for malformed global.toml")
	}
}

func TestSetThemeUpdatesExistingFile(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	scope := "user"
	if err := Save(GlobalConfig{Skills: &SkillsDefaults{InstallScope: &scope}}); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	if err := SetTheme("solarized"); err != nil {
		t.Fatalf("SetTheme: %v", err)
	}
	g, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if g.Theme == nil || *g.Theme != "solarized" {
		t.Errorf("SetTheme did not persist: %+v", g.Theme)
	}
	// And the previous skills field must still be there.
	if g.Skills == nil || g.Skills.InstallScope == nil || *g.Skills.InstallScope != "user" {
		t.Errorf("SetTheme clobbered skills: %+v", g.Skills)
	}
}
