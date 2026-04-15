package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPasteBurstEnterDoesNotSubmit(t *testing.T) {
	m := newModelMultiTestModel(t, &tuiFakeProvider{})

	m = updatePasteTest(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Please refactor")})
	m = updatePasteTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = updatePasteTest(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("the snake module")})

	if m.agentRunning {
		t.Fatal("paste newline should not submit the input")
	}
	value := m.input.Value()
	if !strings.Contains(value, "Please refactor") || !strings.Contains(value, "the snake module") {
		t.Fatalf("pasted text missing from input: %q", value)
	}
	if !strings.Contains(value, "\n") {
		t.Fatalf("paste newline should remain in textarea, got %q", value)
	}
}

func TestNormalEnterSubmitsWithoutAddingTextareaNewline(t *testing.T) {
	m := newModelMultiTestModel(t, &tuiFakeProvider{})
	m.input.SetValue("/help")

	m = updatePasteTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if value := m.input.Value(); value != "" {
		t.Fatalf("input should be cleared after submit, got %q", value)
	}
	if len(m.history) == 0 || !strings.Contains(stripAnsi(strings.Join(m.history, "\n")), "/model-multi") {
		t.Fatalf("expected /help output in history")
	}
}

func updatePasteTest(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	updated, cmd := m.Update(msg)
	if cmd != nil {
		// Textarea cursor blink commands can sleep long enough to expire the
		// paste guard; the program schedules them asynchronously in real use.
		_ = cmd
	}
	next, ok := updated.(model)
	if !ok {
		ptr, ptrOK := updated.(*model)
		if !ptrOK {
			t.Fatalf("Update returned %T", updated)
		}
		next = *ptr
	}
	return next
}
