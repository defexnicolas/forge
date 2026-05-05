package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestProfileFormSelectsCurrent(t *testing.T) {
	form := newProfileForm("fast", DefaultTheme())
	if form.profiles[form.selected].Name != "fast" {
		t.Fatalf("selected = %q, want %q", form.profiles[form.selected].Name, "fast")
	}
}

func TestProfileFormDownEnter(t *testing.T) {
	form := newProfileForm("safe", DefaultTheme())
	if form.profiles[form.selected].Name != "safe" {
		t.Fatalf("initial selected = %q, want safe", form.profiles[form.selected].Name)
	}
	form, _ = form.Update(tea.KeyMsg{Type: tea.KeyDown})
	form, _ = form.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !form.done {
		t.Fatalf("expected done after Enter")
	}
	if form.canceled {
		t.Fatalf("expected not canceled")
	}
	if form.chosen != "normal" {
		t.Fatalf("chosen = %q, want %q", form.chosen, "normal")
	}
}

func TestProfileFormEscCancels(t *testing.T) {
	form := newProfileForm("normal", DefaultTheme())
	form, _ = form.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !form.done {
		t.Fatalf("expected done after Esc")
	}
	if !form.canceled {
		t.Fatalf("expected canceled after Esc")
	}
	if form.chosen != "" {
		t.Fatalf("chosen = %q, want empty", form.chosen)
	}
}

func TestProfileFormViewMarksCurrent(t *testing.T) {
	form := newProfileForm("trusted", DefaultTheme())
	view := form.View()
	if !strings.Contains(view, "trusted") {
		t.Fatalf("view should contain 'trusted', got %q", view)
	}
	if !strings.Contains(view, "[current]") {
		t.Fatalf("view should mark the current profile, got %q", view)
	}
}
