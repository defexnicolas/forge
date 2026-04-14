package patch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExactReplace(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "file.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := ExactReplace(cwd, "file.txt", "world", "forge")
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Operations[0].NewText; got != "hello forge\n" {
		t.Fatalf("unexpected new text %q", got)
	}
}

func TestExactReplaceRejectsMissingOrRepeated(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "file.txt"), []byte("same same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExactReplace(cwd, "file.txt", "missing", "x"); err == nil {
		t.Fatal("expected missing old_text error")
	}
	if _, err := ExactReplace(cwd, "file.txt", "same", "x"); err == nil {
		t.Fatal("expected repeated old_text error")
	}
}

func TestNewFileRejectsExisting(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "file.txt"), []byte("exists"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFile(cwd, "new.txt", "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFile(cwd, "file.txt", "overwrite"); err == nil {
		t.Fatal("expected overwrite rejection")
	}
}

func TestUnifiedDiffApply(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "file.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := UnifiedDiff(cwd, `--- a/file.txt
+++ b/file.txt
@@ -1,3 +1,3 @@
 one
-two
+deux
 three
`)
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := Apply(cwd, plan)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(cwd, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "one\ndeux\nthree\n" {
		t.Fatalf("unexpected content %q", data)
	}
	if err := Undo(cwd, snapshots); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(cwd, "file.txt"))
	if string(data) != "one\ntwo\nthree\n" {
		t.Fatalf("undo failed: %q", data)
	}
}

func TestUnifiedDiffRejectsMalformed(t *testing.T) {
	cwd := t.TempDir()
	if _, err := UnifiedDiff(cwd, `not a diff`); err == nil {
		t.Fatal("expected malformed diff error")
	}
}

func TestDiffRendering(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "file.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := ExactReplace(cwd, "file.txt", "world", "forge")
	if err != nil {
		t.Fatal(err)
	}
	diff := Diff(plan)
	if !strings.Contains(diff, "-hello world") || !strings.Contains(diff, "+hello forge") {
		t.Fatalf("unexpected diff:\n%s", diff)
	}
}
