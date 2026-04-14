package hooks

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadHooksFromFile(t *testing.T) {
	dir := t.TempDir()
	data := `{"hooks":[
		{"event":"after:tool_call","match":"edit_file","command":"echo edited"},
		{"event":"before:tool_call","command":"echo before"}
	]}`
	path := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Runner{cwd: dir}
	if err := r.Load(path); err != nil {
		t.Fatal(err)
	}
	if len(r.hooks) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(r.hooks))
	}
	if r.hooks[0].Match != "edit_file" {
		t.Fatalf("expected match edit_file, got %s", r.hooks[0].Match)
	}
}

func TestLoadMissingFileIsNotError(t *testing.T) {
	r := &Runner{cwd: t.TempDir()}
	if err := r.Load(filepath.Join(t.TempDir(), "nonexistent.json")); err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(r.hooks) != 0 {
		t.Fatal("expected no hooks")
	}
}

func TestNormalizeClaudeEvents(t *testing.T) {
	if normalizeEvent("PreToolUse") != "before:tool_call" {
		t.Fatal("PreToolUse should normalize")
	}
	if normalizeEvent("PostToolUse") != "after:tool_call" {
		t.Fatal("PostToolUse should normalize")
	}
	if normalizeEvent("after:patch") != "after:patch" {
		t.Fatal("native events should pass through")
	}
}

func TestMatchesHook(t *testing.T) {
	// Empty match = all tools
	h := Hook{Event: "after:tool_call", Match: ""}
	if !matchesHook(h, "after:tool_call", "read_file") {
		t.Fatal("empty match should match all")
	}
	// Exact match
	h.Match = "edit_file"
	if !matchesHook(h, "after:tool_call", "edit_file") {
		t.Fatal("exact match should work")
	}
	if matchesHook(h, "after:tool_call", "write_file") {
		t.Fatal("should not match different tool")
	}
	// Wrong event
	if matchesHook(h, "before:tool_call", "edit_file") {
		t.Fatal("should not match wrong event")
	}
	// Glob match
	h.Match = "edit_*"
	if !matchesHook(h, "after:tool_call", "edit_file") {
		t.Fatal("glob should match")
	}
}

func TestBeforeHookBlocks(t *testing.T) {
	dir := t.TempDir()
	var failCmd string
	if runtime.GOOS == "windows" {
		failCmd = "exit 1"
	} else {
		failCmd = "exit 1"
	}
	r := &Runner{
		cwd: dir,
		hooks: []Hook{
			{Event: "before:tool_call", Command: failCmd},
		},
	}
	err := r.RunBefore("before:tool_call", "read_file")
	if err == nil {
		t.Fatal("expected before hook to block with error")
	}
}

func TestAfterHookDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	var failCmd string
	if runtime.GOOS == "windows" {
		failCmd = "exit 1"
	} else {
		failCmd = "exit 1"
	}
	r := &Runner{
		cwd: dir,
		hooks: []Hook{
			{Event: "after:tool_call", Command: failCmd},
		},
	}
	// Should not panic or return error
	r.RunAfter("after:tool_call", "read_file", nil)
}

func TestDescribe(t *testing.T) {
	r := &Runner{cwd: t.TempDir()}
	if r.Describe() != "No hooks loaded. Add .forge/hooks.json to configure hooks." {
		t.Fatal("expected empty describe")
	}
	r.hooks = []Hook{{Event: "after:patch", Match: "edit_file", Command: "gofmt"}}
	desc := r.Describe()
	if desc == "" || !contains(desc, "after:patch") {
		t.Fatalf("expected hooks in describe, got %s", desc)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
