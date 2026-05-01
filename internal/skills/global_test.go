package skills

import (
	"path/filepath"
	"testing"
)

func TestNewGlobalManagerUsesGlobalCacheDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FORGE_GLOBAL_HOME", dir)

	mgr := NewGlobalManager(Options{CLI: "npx"})
	got := mgr.cacheBaseDir()
	want := filepath.Join(dir, "cache", "skills")
	if got != want {
		t.Errorf("cacheBaseDir = %q, want %q", got, want)
	}
}

func TestNewGlobalManagerForcesUserScope(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	mgr := NewGlobalManager(Options{InstallScope: "project"})
	if mgr.options.InstallScope != "user" {
		t.Errorf("global manager must use install_scope=user, got %q", mgr.options.InstallScope)
	}
}

func TestNewGlobalManagerStripsPluginDirs(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	mgr := NewGlobalManager(Options{PluginSkillDirs: []string{"/some/plugin/skills"}})
	if len(mgr.options.PluginSkillDirs) != 0 {
		t.Errorf("global manager must clear PluginSkillDirs, got %v", mgr.options.PluginSkillDirs)
	}
}

func TestNewGlobalManagerCwdIsInstallDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FORGE_GLOBAL_HOME", dir)
	mgr := NewGlobalManager(Options{})
	want := filepath.Join(dir, "skills")
	if mgr.cwd != want {
		t.Errorf("global manager cwd = %q, want %q", mgr.cwd, want)
	}
}

func TestWorkspaceManagerKeepsLocalCachePath(t *testing.T) {
	cwd := t.TempDir()
	mgr := NewManager(cwd, Options{})
	want := filepath.Join(cwd, ".forge", "cache", "skills")
	if got := mgr.cacheBaseDir(); got != want {
		t.Errorf("workspace cacheBaseDir = %q, want %q", got, want)
	}
}

func TestOptionsCacheDirOverride(t *testing.T) {
	cwd := t.TempDir()
	override := filepath.Join(cwd, "alt-cache")
	mgr := NewManager(cwd, Options{CacheDir: override})
	if got := mgr.cacheBaseDir(); got != override {
		t.Errorf("explicit CacheDir override ignored: got %q, want %q", got, override)
	}
}
