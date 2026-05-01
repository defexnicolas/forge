package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, root string, tree map[string]string) {
	t.Helper()
	for rel, content := range tree {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func TestRunSearchTextGoFallback(t *testing.T) {
	prev := forgeForceGoSearchBackend
	forgeForceGoSearchBackend = true
	t.Cleanup(func() { forgeForceGoSearchBackend = prev })

	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/main.go":              "package main\n\nfunc main() { println(\"hello\") }\n",
		"docs/notes.md":            "look for needle here\nthe needle is also here\n",
		"node_modules/junk/bad.js": "this should NEVER be searched (skipped dir)\nneedle hides here\n",
		".git/config":              "[core]\nneedle should not be matched\n",
		"build/output.bin":         "needle inside binary build dir -- skipped\n",
		"assets/logo.png":          "binary content with needle should be skipped",
	})

	got, err := runSearchText(context.Background(), root, "needle", 50)
	if err != nil {
		t.Fatalf("runSearchText: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 matches in docs/notes.md, got %d: %v", len(got), got)
	}
	for _, m := range got {
		if !strings.HasPrefix(m, "docs/notes.md:") {
			t.Errorf("unexpected match outside docs/: %q", m)
		}
	}
}

func TestRunSearchTextLimit(t *testing.T) {
	prev := forgeForceGoSearchBackend
	forgeForceGoSearchBackend = true
	t.Cleanup(func() { forgeForceGoSearchBackend = prev })

	root := t.TempDir()
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString("needle\n")
	}
	writeTree(t, root, map[string]string{"big.txt": sb.String()})

	got, err := runSearchText(context.Background(), root, "needle", 5)
	if err != nil {
		t.Fatalf("runSearchText: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("expected limit=5 to cap matches at 5, got %d", len(got))
	}
}

func TestRunSearchFilesSkipsNoiseDirs(t *testing.T) {
	prev := forgeForceGoSearchBackend
	forgeForceGoSearchBackend = true
	t.Cleanup(func() { forgeForceGoSearchBackend = prev })

	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/widget.go":              "package src\n",
		"node_modules/lodash/widget.js": "module.exports = {}\n",
		".git/widget":                "ref\n",
		"vendor/github.com/x/widget.go": "package x\n",
	})

	got, err := runSearchFiles(context.Background(), root, "widget")
	if err != nil {
		t.Fatalf("runSearchFiles: %v", err)
	}
	if len(got) != 1 || got[0] != "src/widget.go" {
		t.Fatalf("expected only src/widget.go, got %v", got)
	}
}

func TestRunSearchTextEmptyQuery(t *testing.T) {
	if _, err := runSearchText(context.Background(), t.TempDir(), "", 10); err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestRipgrepPathHonorsForceFlag(t *testing.T) {
	prev := forgeForceGoSearchBackend
	forgeForceGoSearchBackend = true
	t.Cleanup(func() { forgeForceGoSearchBackend = prev })
	if got := ripgrepPath(); got != "" {
		t.Errorf("forceGoSearchBackend should mask rg, got %q", got)
	}
}
