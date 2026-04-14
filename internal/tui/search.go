package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type searchMode struct {
	input   textinput.Model
	active  bool
	query   string
	matches int
}

func newSearchMode(theme Theme) searchMode {
	input := textinput.New()
	input.Placeholder = "Search..."
	input.Width = 40
	input.Prompt = "? "
	return searchMode{input: input}
}

func (s searchMode) Update(msg tea.Msg) (searchMode, bool) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			s.active = false
			s.query = ""
			return s, true // done
		case tea.KeyEnter:
			s.query = strings.TrimSpace(s.input.Value())
			s.active = false
			return s, true // done, keep query for highlight
		}
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	_ = cmd
	s.query = strings.TrimSpace(s.input.Value())
	return s, false
}

func (s searchMode) View(theme Theme) string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("75")).
		Padding(0, 1).
		Width(50)
	content := s.input.View()
	if s.matches > 0 {
		content += theme.Muted.Render("  " + string(rune('0'+s.matches%10)) + " matches")
	}
	return box.Render(content)
}

// FilterHistory highlights matching lines in history.
func FilterHistory(history []string, query string) ([]string, int) {
	if query == "" {
		return history, 0
	}
	lower := strings.ToLower(query)
	matches := 0
	result := make([]string, len(history))
	for i, line := range history {
		if strings.Contains(strings.ToLower(line), lower) {
			// Bold the matching lines.
			result[i] = "> " + line
			matches++
		} else {
			result[i] = line
		}
	}
	return result, matches
}
