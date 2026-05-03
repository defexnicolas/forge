package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestExitConfirmFormDefaultsToBackground locks the safe-default
// behavior: when an agent is mid-task, the modal's initial selection
// should be Background. Cancel and Kill require a deliberate move.
func TestExitConfirmFormDefaultsToBackground(t *testing.T) {
	f := newExitWorkspaceConfirmForm(DefaultTheme(), "/tmp/repo")
	if f.cursor != 0 {
		t.Fatalf("default cursor should be 0 (Background), got %d", f.cursor)
	}
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !f.done {
		t.Fatal("Enter should mark form done")
	}
	if f.choice != exitChoiceBackground {
		t.Fatalf("default Enter should pick Background, got %d", f.choice)
	}
}

// TestExitConfirmFormKillChoice verifies arrow nav reaches Kill and
// Enter commits to it.
func TestExitConfirmFormKillChoice(t *testing.T) {
	f := newExitWorkspaceConfirmForm(DefaultTheme(), "/tmp/repo")
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRight})
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if f.choice != exitChoiceKill {
		t.Fatalf("Right then Enter should pick Kill, got %d", f.choice)
	}
}

// TestExitConfirmFormEscapeIsCancel locks the contract that Esc inside
// the modal means "I changed my mind" — never an accidental kill.
func TestExitConfirmFormEscapeIsCancel(t *testing.T) {
	f := newExitWorkspaceConfirmForm(DefaultTheme(), "/tmp/repo")
	// Move to Kill first to make the test more meaningful: Esc should
	// still cancel even when the highlighted option is destructive.
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRight})
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if f.choice != exitChoiceCancel {
		t.Fatalf("Esc must always resolve as Cancel, got %d", f.choice)
	}
}

// TestExitConfirmFormShortcutKeys verifies the b/k/c hotkeys map to
// the right outcomes without arrow navigation.
func TestExitConfirmFormShortcutKeys(t *testing.T) {
	cases := []struct {
		key  rune
		want exitChoice
	}{
		{'b', exitChoiceBackground},
		{'k', exitChoiceKill},
		{'c', exitChoiceCancel},
	}
	for _, tc := range cases {
		f := newExitWorkspaceConfirmForm(DefaultTheme(), "/tmp/repo")
		f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{tc.key}})
		if !f.done {
			t.Fatalf("key %q should mark form done", tc.key)
		}
		if f.choice != tc.want {
			t.Fatalf("key %q should pick choice %d, got %d", tc.key, tc.want, f.choice)
		}
	}
}
