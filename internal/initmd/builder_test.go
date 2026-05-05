package initmd

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge/internal/projectstate"

	_ "modernc.org/sqlite"
)

// newTestService spins up a projectstate.Service backed by an in-memory
// SQLite db so the test never touches the user's real cache.
func newTestService(t *testing.T) *projectstate.Service {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS project_state (
		repo_root TEXT PRIMARY KEY,
		snapshot_json TEXT NOT NULL,
		summary TEXT NOT NULL DEFAULT '',
		git_head TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return projectstate.NewService(db)
}

func TestBuildGoRepo(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "go.mod"), "module example.com/foo\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(tmp, "README.md"), "# Foo\n\nFoo is a small CLI for managing widgets.\n")

	svc := newTestService(t)
	out, err := Build(context.Background(), tmp, svc, BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out, "# AGENTS.md") {
		t.Error("missing AGENTS.md heading")
	}
	if !strings.Contains(out, "Foo is a small CLI") {
		t.Error("README first line should land in Project section")
	}
	if !strings.Contains(out, "Languages: Go") {
		t.Errorf("Go language should be detected: %s", out)
	}
	if !strings.Contains(out, "go test ./...") {
		t.Error("Go test command should default")
	}
	if !strings.Contains(out, forgeGeneratedMarker) {
		t.Error("output must include the forge-generated marker")
	}
}

func TestBuildNodeRepoUsesPackageJSONScripts(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "package.json"), `{
		"name": "demo",
		"scripts": {
			"test": "vitest run",
			"build": "vite build",
			"dev": "vite",
			"lint": "eslint ."
		}
	}`)

	svc := newTestService(t)
	out, err := Build(context.Background(), tmp, svc, BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{"npm run test", "npm run build", "npm run dev", "npm run lint"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
}

func TestBuildMakefileOverridesLanguageDefaults(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "go.mod"), "module example.com/foo\n")
	mustWrite(t, filepath.Join(tmp, "Makefile"), "test:\n\tgo test ./internal/...\n\nbuild:\n\tgo build -o bin/foo ./cmd/foo\n")

	svc := newTestService(t)
	out, err := Build(context.Background(), tmp, svc, BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out, "make test") {
		t.Errorf("Makefile should override Go default: %s", out)
	}
	if !strings.Contains(out, "make build") {
		t.Errorf("Makefile build target should override Go default: %s", out)
	}
}

func TestIsForgeGeneratedDetectsMarker(t *testing.T) {
	body := "# AGENTS.md\n\n## Project\n\nx\n\n" + forgeGeneratedMarker + " 2026-05-05T00:00:00Z from snapshot abc -->\n"
	if !IsForgeGenerated(body) {
		t.Error("forge-generated body should be detected")
	}
	if IsForgeGenerated("# AGENTS.md\n\n## Project\n\nuser wrote this\n") {
		t.Error("user-authored body should NOT be detected")
	}
}

func TestBuildEmptyRepoStillProducesValidMarkdown(t *testing.T) {
	tmp := t.TempDir()
	svc := newTestService(t)
	out, err := Build(context.Background(), tmp, svc, BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.HasPrefix(out, "# AGENTS.md") {
		t.Error("output should start with the heading")
	}
	if !strings.Contains(out, "## Rules") {
		t.Error("Rules section should always appear")
	}
	if !strings.Contains(out, forgeGeneratedMarker) {
		t.Error("marker should always be appended")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
