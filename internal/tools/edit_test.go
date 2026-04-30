package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func TestEditFileTool(t *testing.T) {
	requireGit(t)
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "file.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{
		"path":     "file.txt",
		"old_text": "world",
		"new_text": "forge",
	})
	result, err := editFileTool{}.Run(Context{Context: context.Background(), CWD: cwd}, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ChangedFiles) != 1 {
		t.Fatalf("expected changed file, got %#v", result.ChangedFiles)
	}
	data, _ := os.ReadFile(filepath.Join(cwd, "file.txt"))
	if string(data) != "hello forge\n" {
		t.Fatalf("unexpected content %q", data)
	}
}

func TestWriteFileToolRejectsExisting(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "file.txt"), []byte("exists"), 0o644); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{
		"path":    "file.txt",
		"content": "overwrite",
	})
	tool := writeFileTool{}
	if _, err := tool.Run(Context{Context: context.Background(), CWD: cwd}, input); err == nil {
		t.Fatal("expected overwrite rejection")
	}
}

func TestWriteFileToolCreatesNewFile(t *testing.T) {
	requireGit(t)
	cwd := t.TempDir()
	input, _ := json.Marshal(map[string]string{
		"path":    "new.txt",
		"content": "hello forge\n",
	})
	tool := writeFileTool{}
	result, err := tool.Run(Context{Context: context.Background(), CWD: cwd}, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ChangedFiles) != 1 {
		t.Fatalf("expected changed file, got %#v", result.ChangedFiles)
	}
	data, err := os.ReadFile(filepath.Join(cwd, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello forge\n" {
		t.Fatalf("unexpected content %q", data)
	}
}

func TestApplyPatchTool(t *testing.T) {
	requireGit(t)
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "file.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{
		"patch": `--- a/file.txt
+++ b/file.txt
@@ -1,2 +1,2 @@
 one
-two
+deux
`,
	})
	tool := applyPatchTool{}
	result, err := tool.Run(Context{Context: context.Background(), CWD: cwd}, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ChangedFiles) != 1 {
		t.Fatalf("expected changed file, got %#v", result.ChangedFiles)
	}
	data, _ := os.ReadFile(filepath.Join(cwd, "file.txt"))
	if string(data) != "one\ndeux\n" {
		t.Fatalf("unexpected content %q", data)
	}
}

func TestApplyPatchToolRejectsMalformed(t *testing.T) {
	cwd := t.TempDir()
	input, _ := json.Marshal(map[string]string{"patch": "not a diff"})
	tool := applyPatchTool{}
	if _, err := tool.Run(Context{Context: context.Background(), CWD: cwd}, input); err == nil {
		t.Fatal("expected malformed diff rejection")
	}
}
