package agent

import (
	"fmt"
	"strings"

	"forge/internal/gitops"
	"forge/internal/patch"
)

func (r *Runtime) RefreshGitSessionState() {
	if r == nil {
		return
	}
	prior := r.GitSessionState()
	state, err := gitops.InspectSessionState(
		r.CWD,
		r.Config.Git.AutoInit,
		r.Config.Git.RequireCleanOrSnapshot,
		r.Config.Git.BaselineCommitMessage,
	)
	if err != nil {
		return
	}
	if prior.RepoMissingAtStart {
		state.RepoMissingAtStart = true
	}
	if prior.AutoInitialized {
		state.AutoInitialized = true
	}
	if prior.BaselineCreatedThisSession {
		state.BaselineCreatedThisSession = true
		if state.BaselineCommitID == "" {
			state.BaselineCommitID = prior.BaselineCommitID
		}
	}
	r.SetGitSessionState(state)
}

func (r *Runtime) SetGitSessionState(state gitops.SessionState) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.gitState = state
	r.mu.Unlock()
	if r.Builder != nil {
		copyState := state
		r.Builder.GitState = &copyState
	}
}

func (r *Runtime) GitSessionState() gitops.SessionState {
	if r == nil {
		return gitops.SessionState{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gitState
}

func (r *Runtime) prepareGitBackedMutation(summary string, plan patch.Plan) (string, func() error, error) {
	diff := patch.Diff(plan)
	cfg := r.Config.Git
	state := r.GitSessionState()
	if state.RepoInitialized && cfg.RequireCleanOrSnapshot {
		if status, err := gitops.StatusFor(r.CWD); err == nil {
			state.DirtyWorktree = status.Dirty
			state.DirtyEntries = append([]string(nil), status.Entries...)
			state.SnapshotRequiredBeforeMutate = status.Dirty
			r.SetGitSessionState(state)
		}
	}
	var notes []string
	var steps []func() error

	if !state.RepoInitialized {
		if !cfg.AutoInit {
			return "", nil, fmt.Errorf("git repository required before mutating this workspace")
		}
		notes = append(notes, "Forge will initialize a git repository and create a baseline commit before applying this patch.")
		steps = append(steps, func() error {
			if !cfg.CreateBaselineCommit {
				return fmt.Errorf("git auto-init without baseline commit is not supported yet")
			}
			_, err := gitops.EnsureRepo(r.CWD, cfg.BaselineCommitMessage)
			return err
		})
	} else if cfg.RequireCleanOrSnapshot && state.SnapshotRequiredBeforeMutate {
		status := gitops.Status{Dirty: state.DirtyWorktree, Entries: append([]string(nil), state.DirtyEntries...)}
		notes = append(notes, formatDirtyTreeNote(status))
		steps = append(steps, func() error {
			_, err := gitops.SnapshotDirtyWorktree(r.CWD, cfg.SnapshotCommitMessage)
			return err
		})
	}

	if len(notes) == 0 {
		return diff, nil, nil
	}
	beforeApply := func() error {
		for _, step := range steps {
			if err := step(); err != nil {
				return err
			}
		}
		r.RefreshGitSessionState()
		return nil
	}
	var b strings.Builder
	b.WriteString("[forge git]\n")
	for _, note := range notes {
		b.WriteString("- ")
		b.WriteString(note)
		b.WriteByte('\n')
	}
	b.WriteString("- Requested change: ")
	b.WriteString(summary)
	b.WriteString("\n\n")
	b.WriteString(diff)
	return b.String(), beforeApply, nil
}

func formatDirtyTreeNote(status gitops.Status) string {
	if len(status.Entries) == 0 {
		return "Workspace has uncommitted changes. Forge will create a snapshot commit before applying this patch."
	}
	preview := status.Entries
	if len(preview) > 5 {
		preview = preview[:5]
	}
	return fmt.Sprintf(
		"Workspace has uncommitted changes (%d path(s)); Forge will create a snapshot commit before applying this patch. Current status: %s",
		len(status.Entries),
		strings.Join(preview, ", "),
	)
}
