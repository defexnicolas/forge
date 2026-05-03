package tui

import (
	"strconv"
	"strings"

	"forge/internal/config"
	"forge/internal/plugins"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// outputStyleForm lists every output-style file the discovered plugins
// expose (via Plugin.ListOutputStyles) and lets the user pick one — or
// pick "(none)" to clear the override. Selection is persisted on Enter
// into Config.OutputStyle, which the runtime then appends to the system
// prompt of every subsequent turn.
type outputStyleForm struct {
	styles      []plugins.OutputStyle
	cursor      int
	current     string
	done        bool
	canceled    bool
	theme       Theme
}

func newOutputStyleForm(cfg config.Config, available []plugins.OutputStyle, theme Theme) outputStyleForm {
	cursor := 0
	for i, s := range available {
		if s.Path == cfg.OutputStyle {
			cursor = i + 1 // +1 because index 0 is the synthetic "(none)" row
			break
		}
	}
	return outputStyleForm{
		styles:  available,
		cursor:  cursor,
		current: cfg.OutputStyle,
		theme:   theme,
	}
}

func (f outputStyleForm) rowCount() int { return len(f.styles) + 1 }

func (f outputStyleForm) Update(msg tea.Msg) (outputStyleForm, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			f.canceled = true
			f.done = true
			return f, nil
		case tea.KeyUp:
			if f.cursor > 0 {
				f.cursor--
			}
			return f, nil
		case tea.KeyDown:
			if f.cursor < f.rowCount()-1 {
				f.cursor++
			}
			return f, nil
		case tea.KeyEnter:
			f.done = true
			return f, nil
		}
	}
	return f, nil
}

func (f outputStyleForm) View() string {
	t := f.theme
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#5f87d7")).
		Padding(1, 2).
		Width(70)

	var b strings.Builder
	b.WriteString(t.TableHeader.Render("Output Style") + "\n\n")
	if len(f.styles) == 0 {
		b.WriteString(t.Muted.Render("No output styles discovered. Add one to a plugin's output-styles/ dir."))
		b.WriteString("\n\n" + t.Muted.Render("Esc: close"))
		return box.Render(b.String())
	}
	rows := make([]string, 0, f.rowCount())
	rows = append(rows, "(none)")
	for _, s := range f.styles {
		rows = append(rows, s.Plugin+" / "+s.Name)
	}
	for i, label := range rows {
		marker := "  "
		if i == f.cursor {
			marker = t.IndicatorAgent.Render("> ")
		}
		b.WriteString(marker + label + "\n")
	}
	b.WriteString("\n" + t.Muted.Render("Up/Down: select  Enter: apply  Esc: cancel  ("+strconv.Itoa(len(f.styles))+" available)"))
	return box.Render(b.String())
}

// ApplyInMemory writes the chosen path (or "" for none) into cfg.
func (f outputStyleForm) ApplyInMemory(cfg *config.Config) string {
	if f.canceled {
		return "Output style change canceled."
	}
	if f.cursor == 0 {
		cfg.OutputStyle = ""
		return f.theme.Success.Render("Output style cleared.")
	}
	idx := f.cursor - 1
	if idx >= len(f.styles) {
		return f.theme.ErrorStyle.Render("Selection out of range.")
	}
	cfg.OutputStyle = f.styles[idx].Path
	return f.theme.Success.Render("Output style: " + f.styles[idx].Plugin + "/" + f.styles[idx].Name)
}
