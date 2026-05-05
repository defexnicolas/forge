package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSliceFileLinesFullFile(t *testing.T) {
	content := "a\nb\nc\nd\n"
	text, summary := sliceFileLines(content, "f.txt", 0, 0)
	if text != content {
		t.Fatalf("full read should pass content unchanged, got %q", text)
	}
	if summary != "f.txt" {
		t.Fatalf("summary for full read should be plain path, got %q", summary)
	}
}

func TestSliceFileLinesWithOffsetAndLimit(t *testing.T) {
	content := "line1\nline2\nline3\nline4\nline5\n"
	text, summary := sliceFileLines(content, "f.txt", 2, 2)
	if text != "line2\nline3" {
		t.Fatalf("got %q, want lines 2-3 joined", text)
	}
	if !strings.Contains(summary, "lines 2-3 of 5") {
		t.Fatalf("summary should report range, got %q", summary)
	}
}

func TestSliceFileLinesOnlyOffset(t *testing.T) {
	content := "line1\nline2\nline3\n"
	text, summary := sliceFileLines(content, "f.txt", 2, 0)
	if text != "line2\nline3" {
		t.Fatalf("got %q, want lines from offset 2", text)
	}
	if !strings.Contains(summary, "lines 2-3 of 3") {
		t.Fatalf("summary should report range, got %q", summary)
	}
}

func TestSliceFileLinesOnlyLimit(t *testing.T) {
	content := "line1\nline2\nline3\n"
	text, summary := sliceFileLines(content, "f.txt", 0, 2)
	if text != "line1\nline2" {
		t.Fatalf("got %q, want first 2 lines", text)
	}
	if !strings.Contains(summary, "lines 1-2 of 3") {
		t.Fatalf("summary should report range, got %q", summary)
	}
}

func TestSliceFileLinesOffsetPastEnd(t *testing.T) {
	content := "a\nb\nc\n"
	text, summary := sliceFileLines(content, "f.txt", 10, 5)
	if text != "" {
		t.Fatalf("offset past end should return empty, got %q", text)
	}
	if !strings.Contains(summary, "of 3") {
		t.Fatalf("summary should still report total, got %q", summary)
	}
}

func TestReadFilesToolBatch(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("hello "+name+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	tool := readFilesTool{}
	input := json.RawMessage(`{"paths":["a.txt","b.txt","c.txt"]}`)
	res, err := tool.Run(Context{Context: context.Background(), CWD: tmp}, input)
	if err != nil {
		t.Fatalf("read_files: %v", err)
	}
	if !strings.Contains(res.Summary, "read 3/3 files") {
		t.Fatalf("summary should report success count, got %q", res.Summary)
	}
	text := res.Content[0].Text
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if !strings.Contains(text, "=== "+name+" ===") {
			t.Errorf("missing section header for %s", name)
		}
		if !strings.Contains(text, "hello "+name) {
			t.Errorf("missing content for %s", name)
		}
	}
}

func TestReadFilesToolMissingFileContinues(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "real.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := readFilesTool{}
	input := json.RawMessage(`{"paths":["real.txt","nope.txt"]}`)
	res, err := tool.Run(Context{Context: context.Background(), CWD: tmp}, input)
	if err != nil {
		t.Fatalf("partial failure should not return Go error, got %v", err)
	}
	if !strings.Contains(res.Summary, "read 1/2 files") {
		t.Fatalf("summary should report 1/2, got %q", res.Summary)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "=== real.txt ===") || !strings.Contains(text, "ok") {
		t.Error("real.txt content missing")
	}
	if !strings.Contains(text, "=== nope.txt ===") || !strings.Contains(strings.ToLower(text), "error") {
		t.Error("missing file should be reported as ERROR, not silently dropped")
	}
}

func TestReadFilesToolRejectsEmpty(t *testing.T) {
	tool := readFilesTool{}
	input := json.RawMessage(`{"paths":[]}`)
	_, err := tool.Run(Context{Context: context.Background(), CWD: t.TempDir()}, input)
	if err == nil {
		t.Fatal("expected error on empty paths array")
	}
}

func TestReadFileToolWithOffsetLimit(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "big.txt")
	var b strings.Builder
	for i := 1; i <= 200; i++ {
		b.WriteString("line ")
		b.WriteString(string(rune('0'+i%10)))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	tool := readFileTool{}
	input := json.RawMessage(`{"path":"big.txt","offset":50,"limit":10}`)
	res, err := tool.Run(Context{Context: context.Background(), CWD: tmp}, input)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if !strings.Contains(res.Summary, "lines 50-59 of 200") {
		t.Fatalf("summary should report paginated range, got %q", res.Summary)
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected one content block, got %d", len(res.Content))
	}
	got := res.Content[0].Text
	if strings.Count(got, "\n") != 9 {
		t.Fatalf("expected 10 lines (9 newlines), got %d in %q", strings.Count(got, "\n"), got)
	}
}
