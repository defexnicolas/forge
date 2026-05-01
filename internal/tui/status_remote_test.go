package tui

import (
	"strings"
	"testing"

	"forge/internal/config"
	"forge/internal/gitops"
)

func TestStatusLineUsesActiveRoleDetectedContext(t *testing.T) {
	m := newSizedLayoutModel(t, 96, 24)
	m.options.Config = config.Defaults()
	m.agentRuntime.Config = m.options.Config
	m.options.Config.ModelLoading.Enabled = true
	m.options.Config.Models["planner"] = "plan-model"
	config.SetDetectedForRole(&m.options.Config, "planner", &config.DetectedContext{
		ModelID:             "plan-model",
		LoadedContextLength: 131072,
	})
	m.agentRuntime.Config = m.options.Config
	if err := m.agentRuntime.SetMode("plan"); err != nil {
		t.Fatal(err)
	}
	m.agentRuntime.LastTokensUsed = 2048
	m.agentRuntime.LastTokensBudget = 20000

	got := stripAnsi(m.statusLineView())
	if !strings.Contains(got, "ctx:2.0k/131k") {
		t.Fatalf("status line missing active role ctx window:\n%s", got)
	}
	if !strings.Contains(got, "yarn:20k") {
		t.Fatalf("status line missing yarn budget:\n%s", got)
	}
}

func TestStatusLineUsesActiveRoleModelWhenIdle(t *testing.T) {
	m := newSizedLayoutModel(t, 96, 24)
	m.options.Config = config.Defaults()
	m.agentRuntime.Config = m.options.Config
	m.options.Config.ModelLoading.Enabled = true
	m.options.Config.Models["chat"] = "chat-model"
	m.options.Config.Models["planner"] = "hub-plan-model"
	m.agentRuntime.Config = m.options.Config
	if err := m.agentRuntime.SetMode("plan"); err != nil {
		t.Fatal(err)
	}

	got := stripAnsi(m.statusLineView())
	if !strings.Contains(got, "hub-plan-model") {
		t.Fatalf("status line missing planner model:\n%s", got)
	}
	if strings.Contains(got, "chat-model") {
		t.Fatalf("status line should prefer planner model over chat model:\n%s", got)
	}
}

func TestRemoteSessionSnapshotIncludesVisibleStreamingState(t *testing.T) {
	m := newSizedLayoutModel(t, 96, 24)
	m.history = append(m.history, "    previous line")
	m.streaming = true
	m.streamingStartIdx = len(m.history)
	m.history = append(m.history, "")
	m.streamingRaw.WriteString("partial remote answer")

	session := m.remoteSessionSnapshot()
	history, ok := session["history"].([]string)
	if !ok {
		t.Fatalf("history payload type = %T", session["history"])
	}
	if len(history) == 0 || !strings.Contains(history[len(history)-1], "partial remote answer") {
		t.Fatalf("remote history missing streaming line: %#v", history)
	}
	if status, _ := session["status"].(string); status == "" {
		t.Fatal("expected session status to be populated")
	}
}

func TestGitBannerAndStatusReflectSessionState(t *testing.T) {
	m := newSizedLayoutModel(t, 96, 24)
	m.agentRuntime.SetGitSessionState(gitops.SessionState{
		RepoInitialized:              true,
		DirtyWorktree:                true,
		BaselinePresent:              true,
		SnapshotRequiredBeforeMutate: true,
		DirtyEntries:                 []string{" M file.txt"},
	})

	view := stripAnsi(m.View())
	if !strings.Contains(view, "Dirty worktree detected. Forge will snapshot current changes before the next mutation.") {
		t.Fatalf("missing git banner in view:\n%s", view)
	}
	status := stripAnsi(m.statusLineView())
	if !strings.Contains(status, "git:dirty") {
		t.Fatalf("missing git state in status line:\n%s", status)
	}
}
