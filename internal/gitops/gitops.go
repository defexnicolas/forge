package gitops

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

const (
	DefaultBaselineCommitMessage = "chore: initialize forge workspace baseline"
	DefaultSnapshotCommitMessage = "chore: snapshot workspace before forge mutation"
	defaultUserName              = "Forge"
	defaultUserEmail             = "forge@local"
)

type Status struct {
	Dirty   bool
	Entries []string
}

type BootstrapResult struct {
	Initialized      bool
	BaselineCreated  bool
	BaselineCommitID string
}

type SessionState struct {
	RepoInitialized              bool
	RepoMissingAtStart           bool
	AutoInitEnabled              bool
	AutoInitialized              bool
	BaselinePresent              bool
	BaselineCreatedThisSession   bool
	BaselineCommitID             string
	DirtyWorktree                bool
	SnapshotRequiredBeforeMutate bool
	DirtyEntries                 []string
}

func InspectSessionState(cwd string, autoInit, requireCleanOrSnapshot bool, baselineMessage string) (SessionState, error) {
	state := SessionState{
		AutoInitEnabled:    autoInit,
		RepoMissingAtStart: !IsRepo(cwd),
	}
	if state.RepoMissingAtStart && autoInit {
		result, err := EnsureRepo(cwd, baselineMessage)
		if err != nil {
			return state, err
		}
		state.AutoInitialized = result.Initialized
		state.BaselineCreatedThisSession = result.BaselineCreated
		state.BaselineCommitID = result.BaselineCommitID
	}
	state.RepoInitialized = IsRepo(cwd)
	if !state.RepoInitialized {
		return state, nil
	}
	if head, err := headCommit(cwd); err == nil && strings.TrimSpace(head) != "" {
		state.BaselinePresent = true
		if state.BaselineCommitID == "" {
			state.BaselineCommitID = head
		}
	}
	if requireCleanOrSnapshot {
		status, err := StatusFor(cwd)
		if err != nil {
			return state, err
		}
		state.DirtyWorktree = status.Dirty
		state.DirtyEntries = append([]string(nil), status.Entries...)
		state.SnapshotRequiredBeforeMutate = status.Dirty
	}
	return state, nil
}

func (s SessionState) PromptFact() string {
	if !s.RepoInitialized {
		if s.AutoInitEnabled {
			return "Git: not initialized at session start; Forge will create a baseline repository before the first mutation."
		}
		return "Git: not initialized. Mutations require repository initialization before editing."
	}
	status := "clean"
	if s.DirtyWorktree {
		status = "dirty worktree"
	}
	baseline := "present"
	if !s.BaselinePresent {
		baseline = "pending"
	}
	if s.AutoInitialized || s.BaselineCreatedThisSession {
		return fmt.Sprintf("Git: initialized during this session, %s. Baseline: %s.", status, baseline)
	}
	return fmt.Sprintf("Git: initialized, %s. Baseline: %s.", status, baseline)
}

func (s SessionState) BannerText() string {
	switch {
	case !s.RepoInitialized && s.AutoInitEnabled:
		return "Git not initialized. Forge will create a baseline repository before the first mutation."
	case !s.RepoInitialized:
		return "Git not initialized. Mutations are blocked until the repository is initialized."
	case s.SnapshotRequiredBeforeMutate:
		return "Dirty worktree detected. Forge will snapshot current changes before the next mutation."
	case s.AutoInitialized || s.BaselineCreatedThisSession:
		return "Git repository was initialized for this session and a baseline commit was created."
	default:
		return ""
	}
}

func IsRepo(cwd string) bool {
	out, err := runGit(cwd, "", "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func EnsureRepo(cwd, baselineMessage string) (BootstrapResult, error) {
	if IsRepo(cwd) {
		return BootstrapResult{}, nil
	}
	if strings.TrimSpace(baselineMessage) == "" {
		baselineMessage = DefaultBaselineCommitMessage
	}
	if _, err := runGit(cwd, "", "init"); err != nil {
		return BootstrapResult{}, err
	}
	if err := ensureIdentity(cwd); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := runGit(cwd, "", "add", "-A"); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := runGit(cwd, "", "commit", "--allow-empty", "-m", baselineMessage); err != nil {
		return BootstrapResult{}, err
	}
	head, err := headCommit(cwd)
	if err != nil {
		return BootstrapResult{}, err
	}
	return BootstrapResult{
		Initialized:      true,
		BaselineCreated:  true,
		BaselineCommitID: head,
	}, nil
}

func StatusFor(cwd string) (Status, error) {
	out, err := runGit(cwd, "", "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return Status{}, err
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return Status{}, nil
	}
	lines := strings.Split(text, "\n")
	entries := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		entries = append(entries, line)
	}
	return Status{Dirty: len(entries) > 0, Entries: entries}, nil
}

func SnapshotDirtyWorktree(cwd, message string) (string, error) {
	if strings.TrimSpace(message) == "" {
		message = DefaultSnapshotCommitMessage
	}
	status, err := StatusFor(cwd)
	if err != nil {
		return "", err
	}
	if !status.Dirty {
		return "", nil
	}
	if err := ensureIdentity(cwd); err != nil {
		return "", err
	}
	if _, err := runGit(cwd, "", "add", "-A"); err != nil {
		return "", err
	}
	if _, err := runGit(cwd, "", "commit", "-m", message); err != nil {
		return "", err
	}
	return headCommit(cwd)
}

func ApplyPatch(cwd, diff string) error {
	if !IsRepo(cwd) {
		if _, err := EnsureRepo(cwd, DefaultBaselineCommitMessage); err != nil {
			return err
		}
	}
	_, err := runGit(cwd, diff, "apply", "--whitespace=nowarn", "--recount", "-")
	return err
}

func ReversePatch(cwd, diff string) error {
	if !IsRepo(cwd) {
		return fmt.Errorf("workspace is not a git repository")
	}
	_, err := runGit(cwd, diff, "apply", "-R", "--whitespace=nowarn", "--recount", "-")
	return err
}

func headCommit(cwd string) (string, error) {
	out, err := runGit(cwd, "", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func ensureIdentity(cwd string) error {
	for key, fallback := range map[string]string{
		"user.name":  defaultUserName,
		"user.email": defaultUserEmail,
	} {
		out, err := runGit(cwd, "", "config", "--local", "--get", key)
		if err == nil && strings.TrimSpace(string(out)) != "" {
			continue
		}
		if _, err := runGit(cwd, "", "config", "--local", key, fallback); err != nil {
			return err
		}
	}
	return nil
}

func runGit(cwd, stdin string, args ...string) ([]byte, error) {
	baseArgs := []string{"-C", cwd, "-c", "core.autocrlf=false", "-c", "core.safecrlf=false"}
	cmd := exec.Command("git", append(baseArgs, args...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}
