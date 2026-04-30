package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func TestEnsureRepoCreatesBaselineCommit(t *testing.T) {
	requireGit(t)
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "file.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := EnsureRepo(cwd, DefaultBaselineCommitMessage)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Initialized || !result.BaselineCreated || result.BaselineCommitID == "" {
		t.Fatalf("unexpected bootstrap result: %#v", result)
	}
	if !IsRepo(cwd) {
		t.Fatal("expected git repo after bootstrap")
	}
}

func TestSnapshotDirtyWorktreeCommitsPendingChanges(t *testing.T) {
	requireGit(t)
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "file.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureRepo(cwd, DefaultBaselineCommitMessage); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "file.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitID, err := SnapshotDirtyWorktree(cwd, DefaultSnapshotCommitMessage)
	if err != nil {
		t.Fatal(err)
	}
	if commitID == "" {
		t.Fatal("expected snapshot commit id")
	}
	status, err := StatusFor(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if status.Dirty {
		t.Fatalf("expected clean repo after snapshot, got %#v", status)
	}
}

func TestApplyPatchAndReversePatch(t *testing.T) {
	requireGit(t)
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "file.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureRepo(cwd, DefaultBaselineCommitMessage); err != nil {
		t.Fatal(err)
	}
	diff := strings.Join([]string{
		"diff --git a/file.txt b/file.txt",
		"--- a/file.txt",
		"+++ b/file.txt",
		"@@ -1,1 +1,1 @@",
		"-hello world",
		"+hello forge",
	}, "\n") + "\n"
	if err := ApplyPatch(cwd, diff); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(cwd, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello forge\n" {
		t.Fatalf("unexpected patched content %q", data)
	}
	if err := ReversePatch(cwd, diff); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(cwd, "file.txt"))
	if string(data) != "hello world\n" {
		t.Fatalf("unexpected reverted content %q", data)
	}
}
