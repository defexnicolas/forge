// Package updater compares the running forge binary against its source
// repository and offers a controlled "git pull + rebuild" flow.
//
// The contract:
//   - The binary's source repo path is embedded at build time via -ldflags
//     (see scripts/build.ps1). When that's empty, every operation reports
//     a "disabled" status — there's no on-disk clone to compare against.
//   - Check() is read-only: it runs `git fetch` then `git rev-list --count`
//     to compute how many commits the embedded SHA is behind origin's
//     default branch. It MUST not modify the worktree.
//   - Pull() refuses to run if the worktree is dirty so users don't lose
//     work. The caller is expected to surface the dirty paths and let the
//     user decide.
//   - Build() invokes `go build` against the source repo and writes to the
//     same path the running binary was launched from. On Windows the
//     running .exe is locked, so Build writes to a sibling temp file and
//     instructs the caller to rename + relaunch on next start. On Unix the
//     in-place replace works.
package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"forge/internal/buildinfo"
)

// State represents the result of a remote check.
type State string

const (
	StateUnknown   State = "unknown"
	StateDisabled  State = "disabled"  // no embedded source repo
	StateUpToDate  State = "up_to_date"
	StateBehind    State = "behind"
	StateError     State = "error"
	StateAhead     State = "ahead" // local has commits origin doesn't (dev case)
	StateDiverged  State = "diverged"
)

// Status is the snapshot returned by Check.
type Status struct {
	State          State
	CommitsBehind  int
	CommitsAhead   int
	LocalSHA       string
	RemoteSHA      string
	Branch         string
	Error          string
	CheckedAt      time.Time
}

// Check fetches origin and computes how many commits the local HEAD is
// behind/ahead. It does NOT modify the worktree. Safe to call from any
// goroutine; callers are expected to fan-in through a channel.
func Check(ctx context.Context) Status {
	st := Status{State: StateUnknown, CheckedAt: time.Now()}
	repo := strings.TrimSpace(buildinfo.Get().SourceRepo)
	if repo == "" {
		st.State = StateDisabled
		return st
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		st.State = StateDisabled
		st.Error = "embedded source repo path is not a git checkout: " + repo
		return st
	}

	branch, err := gitOutput(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		st.State = StateError
		st.Error = "git rev-parse failed: " + err.Error()
		return st
	}
	st.Branch = strings.TrimSpace(branch)

	if _, err := gitOutput(ctx, repo, "fetch", "--quiet", "origin"); err != nil {
		// Network failure should not look like "behind" — leave state
		// unknown so the UI doesn't nag with a stale banner.
		st.State = StateError
		st.Error = "git fetch failed: " + err.Error()
		return st
	}

	if local, err := gitOutput(ctx, repo, "rev-parse", "HEAD"); err == nil {
		st.LocalSHA = strings.TrimSpace(local)
	}
	if remote, err := gitOutput(ctx, repo, "rev-parse", "origin/"+st.Branch); err == nil {
		st.RemoteSHA = strings.TrimSpace(remote)
	}

	behindStr, err := gitOutput(ctx, repo, "rev-list", "--count", "HEAD..origin/"+st.Branch)
	if err != nil {
		st.State = StateError
		st.Error = "git rev-list (behind) failed: " + err.Error()
		return st
	}
	aheadStr, err := gitOutput(ctx, repo, "rev-list", "--count", "origin/"+st.Branch+"..HEAD")
	if err != nil {
		st.State = StateError
		st.Error = "git rev-list (ahead) failed: " + err.Error()
		return st
	}
	st.CommitsBehind, _ = strconv.Atoi(strings.TrimSpace(behindStr))
	st.CommitsAhead, _ = strconv.Atoi(strings.TrimSpace(aheadStr))

	switch {
	case st.CommitsBehind > 0 && st.CommitsAhead > 0:
		st.State = StateDiverged
	case st.CommitsBehind > 0:
		st.State = StateBehind
	case st.CommitsAhead > 0:
		st.State = StateAhead
	default:
		st.State = StateUpToDate
	}
	return st
}

// PullResult reports the outcome of a Pull.
type PullResult struct {
	Pulled   bool
	NewSHA   string
	Output   string
	DirtyMsg string // populated if pull aborted because worktree was dirty
}

// Pull runs `git pull --ff-only` after a worktree-clean check. Returns a
// DirtyMsg (with `git status` output) instead of erroring when the worktree
// is dirty so callers can surface a friendly message.
func Pull(ctx context.Context) (PullResult, error) {
	repo := strings.TrimSpace(buildinfo.Get().SourceRepo)
	if repo == "" {
		return PullResult{}, errors.New("update is disabled: no embedded source repo (rebuild via scripts/build.ps1 or scripts/build.sh)")
	}
	dirty, err := gitOutput(ctx, repo, "status", "--porcelain")
	if err != nil {
		return PullResult{}, fmt.Errorf("git status failed: %w", err)
	}
	if strings.TrimSpace(dirty) != "" {
		return PullResult{DirtyMsg: dirty}, nil
	}
	out, err := gitOutput(ctx, repo, "pull", "--ff-only", "--quiet")
	if err != nil {
		return PullResult{Output: out}, fmt.Errorf("git pull failed: %w", err)
	}
	res := PullResult{Pulled: true, Output: strings.TrimSpace(out)}
	if sha, err := gitOutput(ctx, repo, "rev-parse", "HEAD"); err == nil {
		res.NewSHA = strings.TrimSpace(sha)
	}
	return res, nil
}

// BuildResult reports the outcome of a Build.
type BuildResult struct {
	OutputPath  string // path the new binary was written to
	StagedPath  string // on Windows, the temp path; caller must rename on next launch
	Output      string
	NeedRelaunch bool
}

// Build invokes `go build` against the source repo and writes the result
// to exePath. On Windows, where the running binary is locked, it writes
// to exePath+".new" and the caller must rename on relaunch. On Unix, the
// in-place replace works.
func Build(ctx context.Context, exePath string) (BuildResult, error) {
	repo := strings.TrimSpace(buildinfo.Get().SourceRepo)
	if repo == "" {
		return BuildResult{}, errors.New("update is disabled: no embedded source repo")
	}
	if exePath == "" {
		var err error
		exePath, err = os.Executable()
		if err != nil {
			return BuildResult{}, fmt.Errorf("cannot resolve current executable: %w", err)
		}
	}

	target := exePath
	staged := ""
	if runtime.GOOS == "windows" {
		staged = exePath + ".new"
		target = staged
	}

	cmd := exec.CommandContext(ctx, "go", "build", "-o", target, "./cmd/forge")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		return BuildResult{Output: string(out)}, fmt.Errorf("go build failed: %w", err)
	}
	res := BuildResult{
		OutputPath:   exePath,
		StagedPath:   staged,
		Output:       strings.TrimSpace(string(out)),
		NeedRelaunch: true,
	}
	return res, nil
}

// ManualCommand returns the command string a user can copy-paste to update
// forge by hand when the in-app flow can't run (no `go` on PATH, etc.).
func ManualCommand() string {
	repo := strings.TrimSpace(buildinfo.Get().SourceRepo)
	if repo == "" {
		return ""
	}
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("cd %q; git pull --ff-only; .\\scripts\\build.ps1", repo)
	}
	return fmt.Sprintf("cd %q && git pull --ff-only && ./scripts/build.sh", repo)
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
