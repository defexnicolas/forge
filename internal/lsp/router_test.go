package lsp

import (
	"strings"
	"testing"
)

func TestRouterFallsBackToStubForUnknownExtension(t *testing.T) {
	router := NewRouter(t.TempDir(), Config{ByExt: map[string]ServerConfig{".go": {Command: "fake-gopls"}}})

	// .md is not configured; router should return stub error, not try to spawn.
	if _, err := router.Diagnostics("README.md"); err == nil {
		t.Fatal("expected stub error for unconfigured extension")
	} else if !strings.Contains(err.Error(), "LSP not configured") {
		t.Errorf("unexpected error from stub: %v", err)
	}
}

func TestRouterReusesClientForSameExtension(t *testing.T) {
	cfg := Config{ByExt: map[string]ServerConfig{
		".ts":  {Command: "tsserver", Language: "typescript"},
		".tsx": {Command: "tsserver", Language: "typescript"},
	}}
	router := NewRouter(t.TempDir(), cfg)

	// Trigger client creation. We never actually run the binary because
	// these calls use clientFor() directly (no spawn until a method runs).
	c1 := router.clientFor("a.ts")
	c2 := router.clientFor("b.tsx")
	if c1 != c2 {
		t.Errorf("router should reuse the same ProcessClient for tsserver across .ts and .tsx, got %p vs %p", c1, c2)
	}
}

func TestRouterStubForEmptyConfig(t *testing.T) {
	router := NewRouter(t.TempDir(), Config{ByExt: map[string]ServerConfig{}})
	if _, err := router.Definition("any.go", 0, 0); err == nil {
		t.Fatal("empty config should stub-error")
	}
	if _, err := router.Symbols("any.ts"); err == nil {
		t.Fatal("empty config should stub-error")
	}
}
