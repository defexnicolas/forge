package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type searchMode struct {
	input       textinput.Model
	active      bool
	query       string
	positions   []int // rendered-line offsets of matching entries
	currentIdx  int
	jumpPending bool
}

func newSearchMode(theme Theme) searchMode {
	input := textinput.New()
	input.Placeholder = "Search..."
	input.Width = 40
	input.Prompt = "? "
	return searchMode{input: input}
}

func (s searchMode) Update(msg tea.Msg) (searchMode, bool) {
	key, ok := msg.(tea.KeyMsg)
	if ok {
		switch key.Type {
		case tea.KeyEsc:
			s.active = false
			s.query = ""
			s.positions = nil
			s.currentIdx = 0
			s.jumpPending = false
			return s, true
		case tea.KeyEnter:
			if len(s.positions) > 0 {
				s.currentIdx = (s.currentIdx + 1) % len(s.positions)
				s.jumpPending = true
			}
			return s, false
		case tea.KeyShiftTab, tea.KeyCtrlP:
			if len(s.positions) > 0 {
				s.currentIdx = (s.currentIdx - 1 + len(s.positions)) % len(s.positions)
				s.jumpPending = true
			}
			return s, false
		}
		// Shift+Enter arrives as a runes key on many terminals; handle generically.
		if key.Type == tea.KeyRunes && strings.EqualFold(key.String(), "shift+enter") {
			if len(s.positions) > 0 {
				s.currentIdx = (s.currentIdx - 1 + len(s.positions)) % len(s.positions)
				s.jumpPending = true
			}
			return s, false
		}
	}
	prev := s.input.Value()
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	_ = cmd
	s.query = strings.TrimSpace(s.input.Value())
	if s.input.Value() != prev {
		s.currentIdx = 0
		s.jumpPending = true
	}
	return s, false
}

func (s searchMode) View(theme Theme) string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("75")).
		Padding(0, 1).
		Width(50)
	content := s.input.View()
	if len(s.positions) > 0 {
		content += theme.Muted.Render("  " + itoa(s.currentIdx+1) + "/" + itoa(len(s.positions)))
	} else if s.query != "" {
		content += theme.Muted.Render("  no matches")
	}
	return box.Render(content)
}

// FilterHistory highlights matching lines in history and records the rendered
// line offset of each match. The entry at currentIdx is rendered with an extra
// marker so navigation is visible.
func FilterHistory(history []string, query string, currentIdx int) ([]string, []int) {
	if query == "" {
		return history, nil
	}
	lower := strings.ToLower(query)
	result := make([]string, len(history))
	positions := []int{}
	lineOffset := 0
	matchCount := 0
	for i, entry := range history {
		entryLines := strings.Count(entry, "\n") + 1
		if strings.Contains(strings.ToLower(entry), lower) {
			marker := "> "
			if matchCount == currentIdx {
				marker = "▶ "
			}
			result[i] = marker + entry
			positions = append(positions, lineOffset)
			matchCount++
		} else {
			result[i] = entry
		}
		lineOffset += entryLines
	}
	return result, positions
}
