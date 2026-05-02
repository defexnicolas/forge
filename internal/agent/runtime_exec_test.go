package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/tools"
)

func TestSummarizeResultCompactsLargeContent(t *testing.T) {
	large := strings.Repeat("a", 5000) + "\nMIDDLE\n" + strings.Repeat("z", 5000)
	got := summarizeResult(tools.Result{
		Title:   "read_file",
		Summary: strings.Repeat("s", 2000),
		Content: []tools.ContentBlock{{Type: "text", Text: large}},
	})
	if len(got) > 15000 {
		t.Fatalf("summarized payload too large: %d chars", len(got))
	}
	if !strings.Contains(got, "[...truncated...]") {
		t.Fatalf("expected summarized payload to include truncation marker, got:\n%s", got)
	}
}

func TestSummarizeResultKeepsLargerReadFilePayload(t *testing.T) {
	large := strings.Repeat("a", 6000) + "\nCENTER\n" + strings.Repeat("z", 6000)
	got := summarizeResult(tools.Result{
		Title:   "Read file",
		Summary: "game.js",
		Content: []tools.ContentBlock{{Type: "text", Text: large, Path: "C:\\repo\\game.js"}},
	})
	if len(got) < 9000 {
		t.Fatalf("expected read_file summary to preserve more context, got %d chars", len(got))
	}
	if !strings.Contains(got, "game.js") {
		t.Fatalf("expected path/summary to survive compaction, got:\n%s", got)
	}
}

func TestReadFileCacheServesRepeatedReads(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "game.js")
	if err := os.WriteFile(target, []byte("const v = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	cfg := config.Defaults()
	r := newTestRuntime(t, cwd, cfg, registry, llm.NewRegistry())
	r.resetReadCache()

	input := json.RawMessage(`{"path":"game.js"}`)

	first, _ := r.executeTool(context.Background(), ToolCall{Name: "read_file", Input: input}, nil)
	if first == nil || len(first.Content) == 0 || !strings.Contains(first.Content[0].Text, "const v = 1") {
		t.Fatalf("first read returned unexpected result: %#v", first)
	}
	if hits := r.readCacheHits(); hits != 0 {
		t.Errorf("first read should not register a cache hit, got %d", hits)
	}

	// Mutate disk underneath; cache should still serve the original bytes
	// because invalidation only fires through the agent's mutating tools.
	if err := os.WriteFile(target, []byte("MUTATED\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	second, _ := r.executeTool(context.Background(), ToolCall{Name: "read_file", Input: input}, nil)
	if second == nil || !strings.Contains(second.Content[0].Text, "const v = 1") {
		t.Fatalf("cache should serve original bytes, got: %#v", second)
	}
	if hits := r.readCacheHits(); hits != 1 {
		t.Errorf("expected exactly 1 cache hit after second read, got %d", hits)
	}

	// Simulating an edit_file via a synthetic ChangedFiles result must
	// invalidate the cached entry for that path.
	r.invalidateReadCachePaths([]string{"game.js"})

	third, _ := r.executeTool(context.Background(), ToolCall{Name: "read_file", Input: input}, nil)
	if third == nil || !strings.Contains(third.Content[0].Text, "MUTATED") {
		t.Fatalf("after invalidation, read should see post-mutation bytes, got: %#v", third)
	}
}

func TestCompactOldToolResultsNamesToolInStub(t *testing.T) {
	messages := []llm.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "reading"},
		{Role: "tool", ToolCallID: "t1", Content: "Tool result for read_file:\n<file content>"},
		{Role: "assistant", Content: "got it"},
		{Role: "tool", ToolCallID: "t2", Content: "Tool result for run_command: ok"},
		{Role: "assistant", Content: "again"},
		{Role: "tool", ToolCallID: "t3", Content: "Tool result for edit_file: applied"},
	}
	compactOldToolResults(messages, 1)
	if !strings.Contains(messages[2].Content, "[compacted] earlier read_file result") {
		t.Errorf("first stub should name read_file: %q", messages[2].Content)
	}
	if !strings.Contains(messages[4].Content, "[compacted] earlier run_command result") {
		t.Errorf("second stub should name run_command: %q", messages[4].Content)
	}
	// The most recent tool result must be preserved verbatim.
	if !strings.Contains(messages[6].Content, "Tool result for edit_file") {
		t.Errorf("last result should remain verbatim: %q", messages[6].Content)
	}
}

func TestKeepLastToolResultsForMode(t *testing.T) {
	if got := keepLastToolResultsForMode("build"); got != 2 {
		t.Errorf("build mode keep = %d, want 2", got)
	}
	if got := keepLastToolResultsForMode("plan"); got != 3 {
		t.Errorf("plan mode keep = %d, want 3", got)
	}
}

func TestReadFileCacheResetsBetweenTurns(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "f.txt")
	if err := os.WriteFile(target, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	r := newTestRuntime(t, cwd, config.Defaults(), registry, llm.NewRegistry())
	r.resetReadCache()
	input := json.RawMessage(`{"path":"f.txt"}`)
	r.executeTool(context.Background(), ToolCall{Name: "read_file", Input: input}, nil)
	r.executeTool(context.Background(), ToolCall{Name: "read_file", Input: input}, nil)
	if r.readCacheHits() != 1 {
		t.Errorf("expected 1 hit before reset, got %d", r.readCacheHits())
	}
	r.resetReadCache()
	if r.readCacheHits() != 0 {
		t.Errorf("reset should clear the hit counter, got %d", r.readCacheHits())
	}
}
