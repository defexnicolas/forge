package updater

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"forge/internal/buildinfo"
)

// initRepo creates a bare-then-clone pair so we can simulate "remote has
// new commits we don't have locally". The returned path is the working
// clone (what we treat as the embedded SourceRepo).
func initRepo(t *testing.T) (clone string, remote string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	clone = filepath.Join(root, "clone")

	mustGit(t, root, "init", "--bare", "--initial-branch=main", remote)

	// Seed the remote with one commit so HEAD resolves.
	seed := filepath.Join(root, "seed")
	mustGit(t, root, "init", "--initial-branch=main", seed)
	mustGit(t, seed, "config", "user.email", "test@example.com")
	mustGit(t, seed, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, seed, "add", ".")
	mustGit(t, seed, "commit", "-m", "init")
	mustGit(t, seed, "remote", "add", "origin", remote)
	mustGit(t, seed, "push", "origin", "main")

	mustGit(t, root, "clone", remote, clone)
	mustGit(t, clone, "config", "user.email", "test@example.com")
	mustGit(t, clone, "config", "user.name", "test")
	return clone, remote
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func TestCheckDisabledWithoutSourceRepo(t *testing.T) {
	buildinfo.Set(buildinfo.Info{SourceRepo: ""})
	st := Check(context.Background())
	if st.State != StateDisabled {
		t.Fatalf("expected StateDisabled, got %s", st.State)
	}
}

func TestCheckUpToDate(t *testing.T) {
	clone, _ := initRepo(t)
	buildinfo.Set(buildinfo.Info{SourceRepo: clone})
	st := Check(context.Background())
	if st.State != StateUpToDate {
		t.Fatalf("expected StateUpToDate, got %s (err=%s)", st.State, st.Error)
	}
	if st.Branch != "main" {
		t.Fatalf("expected branch=main, got %q", st.Branch)
	}
	if st.CommitsBehind != 0 {
		t.Fatalf("expected 0 commits behind, got %d", st.CommitsBehind)
	}
}

func TestCheckBehind(t *testing.T) {
	clone, remote := initRepo(t)
	// Push a new commit to origin from a separate working tree.
	work := filepath.Join(t.TempDir(), "advance")
	mustGit(t, "", "clone", remote, work)
	mustGit(t, work, "config", "user.email", "test@example.com")
	mustGit(t, work, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(work, "advance.txt"), []byte("ahead\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, work, "add", ".")
	mustGit(t, work, "commit", "-m", "advance")
	mustGit(t, work, "push", "origin", "main")

	buildinfo.Set(buildinfo.Info{SourceRepo: clone})
	st := Check(context.Background())
	if st.State != StateBehind {
		t.Fatalf("expected StateBehind, got %s (err=%s)", st.State, st.Error)
	}
	if st.CommitsBehind != 1 {
		t.Fatalf("expected 1 commit behind, got %d", st.CommitsBehind)
	}
	if st.LocalSHA == st.RemoteSHA {
		t.Fatalf("expected divergent SHAs, both = %s", st.LocalSHA)
	}
}

func TestPullRefusesDirtyWorktree(t *testing.T) {
	clone, _ := initRepo(t)
	// Dirty the worktree.
	if err := os.WriteFile(filepath.Join(clone, "dirty.txt"), []byte("oops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buildinfo.Set(buildinfo.Info{SourceRepo: clone})
	res, err := Pull(context.Background())
	if err != nil {
		t.Fatalf("Pull returned error on dirty worktree: %v", err)
	}
	if res.Pulled {
		t.Fatal("Pull reported success on dirty worktree")
	}
	if !strings.Contains(res.DirtyMsg, "dirty.txt") {
		t.Fatalf("expected dirty.txt in DirtyMsg, got %q", res.DirtyMsg)
	}
}

func TestPullDisabledWithoutSourceRepo(t *testing.T) {
	buildinfo.Set(buildinfo.Info{SourceRepo: ""})
	_, err := Pull(context.Background())
	if err == nil {
		t.Fatal("expected error from Pull when SourceRepo is empty")
	}
}

func TestManualCommandReturnsEmptyWithoutSourceRepo(t *testing.T) {
	buildinfo.Set(buildinfo.Info{SourceRepo: ""})
	if cmd := ManualCommand(); cmd != "" {
		t.Fatalf("expected empty manual command, got %q", cmd)
	}
}
