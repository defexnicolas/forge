package lsp

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigProjectOnly(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, ".lsp.json"), `{
  "servers": {
    "go": {"command": "gopls", "language": "go", "extensions": [".go"]},
    "ts": {"command": "tsserver", "args": ["--stdio"], "language": "typescript", "extensions": [".ts", ".tsx"]}
  }
}`)

	cfg, err := LoadConfig(cwd)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.ByExt) != 3 {
		t.Fatalf("expected 3 extensions, got %v", cfg.ByExt)
	}
	if got := cfg.ByExt[".go"].Command; got != "gopls" {
		t.Errorf(".go server.Command = %q, want gopls", got)
	}
	if got := cfg.ByExt[".tsx"].Command; got != "tsserver" {
		t.Errorf(".tsx server.Command = %q, want tsserver", got)
	}
}

func TestLoadConfigMissingIsEmpty(t *testing.T) {
	cwd := t.TempDir()
	cfg, err := LoadConfig(cwd)
	if err != nil {
		t.Fatalf("missing .lsp.json should not be an error, got %v", err)
	}
	if len(cfg.ByExt) != 0 {
		t.Fatalf("expected empty config, got %v", cfg.ByExt)
	}
}

func TestLoadConfigMalformedIsError(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, ".lsp.json"), `{not json`)
	if _, err := LoadConfig(cwd); err == nil {
		t.Fatal("expected error for malformed .lsp.json")
	}
}

func TestLoadConfigProjectWinsOverPlugin(t *testing.T) {
	cwd := t.TempDir()
	pluginDir := t.TempDir()
	pluginCfg := filepath.Join(pluginDir, ".lsp.json")
	writeFile(t, pluginCfg, `{
  "servers": {"go": {"command": "plugin-gopls", "language": "go", "extensions": [".go"]}}
}`)
	writeFile(t, filepath.Join(cwd, ".lsp.json"), `{
  "servers": {"go": {"command": "project-gopls", "language": "go", "extensions": [".go"]}}
}`)

	cfg, err := LoadConfig(cwd, pluginCfg)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.ByExt[".go"].Command; got != "project-gopls" {
		t.Errorf("project should override plugin, got %q", got)
	}
}

func TestLoadConfigPluginAddsExtensions(t *testing.T) {
	cwd := t.TempDir()
	pluginDir := t.TempDir()
	pluginCfg := filepath.Join(pluginDir, ".lsp.json")
	writeFile(t, pluginCfg, `{
  "servers": {"py": {"command": "pyright", "language": "python", "extensions": [".py"]}}
}`)
	writeFile(t, filepath.Join(cwd, ".lsp.json"), `{
  "servers": {"go": {"command": "gopls", "language": "go", "extensions": [".go"]}}
}`)

	cfg, err := LoadConfig(cwd, pluginCfg)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ByExt[".py"].Command != "pyright" {
		t.Error("plugin extensions not merged in")
	}
	if cfg.ByExt[".go"].Command != "gopls" {
		t.Error("project extensions lost when plugin path is supplied")
	}
}

func TestResolveForFile(t *testing.T) {
	cfg := Config{ByExt: map[string]ServerConfig{".go": {Command: "gopls"}}}
	srv, ok := cfg.ResolveForFile("internal/foo.go")
	if !ok || srv.Command != "gopls" {
		t.Errorf("ResolveForFile(.go) = (%v, %v)", srv, ok)
	}
	if _, ok := cfg.ResolveForFile("README.md"); ok {
		t.Error("ResolveForFile should return ok=false for unknown extension")
	}
	if _, ok := cfg.ResolveForFile("Makefile"); ok {
		t.Error("ResolveForFile should return ok=false when there is no extension")
	}
}

func TestLoadConfigNormalizesExtensions(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, ".lsp.json"), `{
  "servers": {"x": {"command": "x", "extensions": ["GO", " ts ", ".rs"]}}
}`)
	cfg, err := LoadConfig(cwd)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	for _, ext := range []string{".go", ".ts", ".rs"} {
		if _, ok := cfg.ByExt[ext]; !ok {
			t.Errorf("expected normalized extension %q in config: %v", ext, cfg.ByExt)
		}
	}
}
