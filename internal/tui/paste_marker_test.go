package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestInterceptPasteKeySinglelinePassesThrough(t *testing.T) {
	m := &model{}
	in := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello world"), Paste: true}
	out := m.interceptPasteKey(in)
	got, ok := out.(tea.KeyMsg)
	if !ok {
		t.Fatalf("expected KeyMsg, got %T", out)
	}
	if string(got.Runes) != "hello world" {
		t.Errorf("single-line paste should not be rewritten, got %q", string(got.Runes))
	}
	if m.pasteCounter != 0 {
		t.Errorf("counter should not advance on single-line paste, got %d", m.pasteCounter)
	}
}

func TestInterceptPasteKeyMultilineCollapses(t *testing.T) {
	m := &model{}
	body := "line1\nline2\nline3\nline4"
	in := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(body), Paste: true}
	out := m.interceptPasteKey(in)
	got, ok := out.(tea.KeyMsg)
	if !ok {
		t.Fatalf("expected KeyMsg, got %T", out)
	}
	rewritten := string(got.Runes)
	if !strings.HasPrefix(rewritten, "[Pasted text #1 +4 lines]") {
		t.Errorf("multi-line paste should produce a marker, got %q", rewritten)
	}
	if got.Paste {
		t.Error("rewritten message should clear the Paste flag so paste-guard timing isn't double-tripped")
	}
	if m.pasteCounter != 1 {
		t.Fatalf("counter = %d, want 1", m.pasteCounter)
	}
	stored, ok := m.pastes[1]
	if !ok || stored.Text != body {
		t.Errorf("paste #1 not stored verbatim; got %#v", stored)
	}
}

func TestInterceptPasteKeyTwoLinePastesPassThrough(t *testing.T) {
	// pasteMinLines = 3; two-line snippets stay direct so the user can
	// see and edit them without unwrap.
	m := &model{}
	body := "line1\nline2"
	in := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(body), Paste: true}
	out := m.interceptPasteKey(in)
	got, _ := out.(tea.KeyMsg)
	if string(got.Runes) != body {
		t.Errorf("two-line paste should pass through, got %q", string(got.Runes))
	}
	if m.pasteCounter != 0 {
		t.Errorf("counter should not advance below pasteMinLines, got %d", m.pasteCounter)
	}
}

func TestInterceptPasteKeyDetectsBracketlessPasteByNewlines(t *testing.T) {
	// Some terminals don't send the bracketed-paste marker; a
	// KeyRunes payload that contains an actual '\n' is still a
	// paste in practice (Enter is its own KeyEnter event).
	m := &model{}
	body := "a\nb\nc"
	in := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(body), Paste: false}
	out := m.interceptPasteKey(in)
	got, _ := out.(tea.KeyMsg)
	if !strings.Contains(string(got.Runes), "Pasted text") {
		t.Errorf("newline-bearing rune burst should still be collapsed, got %q", string(got.Runes))
	}
}

func TestExpandPastesRoundtripsContent(t *testing.T) {
	m := &model{
		pastes: map[int]pastedBlock{
			1: {ID: 1, Lines: 4, Text: "line1\nline2\nline3\nline4"},
			2: {ID: 2, Lines: 3, Text: "alpha\nbeta\ngamma"},
		},
	}
	in := "Here is the snippet [Pasted text #1 +4 lines] and another [Pasted text #2 +3 lines] please review."
	out := m.expandPastes(in)
	if !strings.Contains(out, "line1\nline2\nline3\nline4") {
		t.Errorf("paste #1 not expanded: %q", out)
	}
	if !strings.Contains(out, "alpha\nbeta\ngamma") {
		t.Errorf("paste #2 not expanded: %q", out)
	}
	if strings.Contains(out, "[Pasted text #") {
		t.Errorf("markers should be gone after expand, got %q", out)
	}
}

func TestExpandPastesPreservesUnknownIDs(t *testing.T) {
	m := &model{pastes: map[int]pastedBlock{}}
	in := "see [Pasted text #99 +5 lines] for context"
	out := m.expandPastes(in)
	if out != in {
		t.Errorf("unknown id should be preserved, got %q", out)
	}
}

func TestExpandPastesNoMarkers(t *testing.T) {
	m := &model{}
	in := "just a regular message"
	if got := m.expandPastes(in); got != in {
		t.Errorf("plain text should pass through unchanged, got %q", got)
	}
}
