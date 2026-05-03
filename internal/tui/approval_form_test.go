package tui

import (
	"testing"

	"forge/internal/agent"

	tea "github.com/charmbracelet/bubbletea"
)

// TestApprovalFormAutoModeApprovesAndFlagsAuto verifies the [Auto] button
// approves the current request AND surfaces autoMode=true so the caller
// can persist approval_profile = "auto" globally.
func TestApprovalFormAutoModeApprovesAndFlagsAuto(t *testing.T) {
	req := &agent.ApprovalRequest{ToolName: "edit_file", Summary: "test"}
	f := newApprovalForm(req, DefaultTheme(), 100, 30)

	// Default cursor is 1 (Approve). Move left once → cursor 0 (Auto).
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyLeft})
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if !f.done {
		t.Fatal("Enter should mark the form done")
	}
	if !f.approved {
		t.Fatal("Auto choice must approve the current request")
	}
	if !f.autoMode {
		t.Fatal("Auto choice must flag autoMode for the caller")
	}
}

// TestApprovalFormApproveDoesNotFlagAuto guards against autoMode leaking
// in when the user picks plain Approve — that would silently turn off
// every future prompt without their consent.
func TestApprovalFormApproveDoesNotFlagAuto(t *testing.T) {
	req := &agent.ApprovalRequest{ToolName: "edit_file", Summary: "test"}
	f := newApprovalForm(req, DefaultTheme(), 100, 30)

	// Default cursor is already Approve.
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if !f.approved {
		t.Fatal("Approve should approve")
	}
	if f.autoMode {
		t.Fatal("Approve must NOT flag autoMode")
	}
}

// TestApprovalFormDenyRejects verifies the third button still rejects
// the request and never sets autoMode.
func TestApprovalFormDenyRejects(t *testing.T) {
	req := &agent.ApprovalRequest{ToolName: "edit_file", Summary: "test"}
	f := newApprovalForm(req, DefaultTheme(), 100, 30)

	// Default Approve → Right twice → Deny (cursor 2).
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRight})
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if !f.done {
		t.Fatal("Enter should mark form done")
	}
	if f.approved {
		t.Fatal("Deny must not approve")
	}
	if f.autoMode {
		t.Fatal("Deny must not flag autoMode")
	}
}

// TestApprovalFormUKeyShortcutsAuto verifies the 'u' shortcut hits Auto
// directly without needing arrow navigation.
func TestApprovalFormUKeyShortcutsAuto(t *testing.T) {
	req := &agent.ApprovalRequest{ToolName: "edit_file", Summary: "test"}
	f := newApprovalForm(req, DefaultTheme(), 100, 30)

	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})

	if !f.done || !f.approved || !f.autoMode {
		t.Fatalf("u shortcut should set done+approved+autoMode, got %#v", f)
	}
}
