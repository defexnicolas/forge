package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type themeForm struct {
	themes   []string
	selected int
	done     bool
	canceled bool
	chosen   string
	theme    Theme
}

func newThemeForm(current Theme) themeForm {
	themes := ThemeNames()
	selected := 0
	for i, name := range themes {
		if name == current.Name {
			selected = i
			break
		}
	}
	return themeForm{
		themes:   themes,
		selected: selected,
		theme:    current,
	}
}

func (f themeForm) Update(msg tea.Msg) (themeForm, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			f.canceled = true
			f.done = true
			return f, nil
		case tea.KeyUp:
			if f.selected > 0 {
				f.selected--
			}
			return f, nil
		case tea.KeyDown:
			if f.selected < len(f.themes)-1 {
				f.selected++
			}
			return f, nil
		case tea.KeyEnter:
			if len(f.themes) > 0 && f.selected < len(f.themes) {
				f.chosen = f.themes[f.selected]
				f.done = true
			}
			return f, nil
		}
	}
	return f, nil
}

func (f themeForm) View() string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#af87d7")).
		Padding(1, 2).
		Width(52)

	t := f.theme
	content := t.TableHeader.Render("Theme Selector") + "\n\n"

	for i, name := range f.themes {
		marker := "  "
		if i == f.selected {
			marker = t.IndicatorAgent.Render("> ")
		}

		// Show a color preview using the candidate theme.
		preview := GetTheme(name)
		sample := preview.Accent.Render("sample") + " " +
			preview.Success.Render("text") + " " +
			preview.Warning.Render("here")

		label := t.StatusValue.Render(name)
		if name == t.Name {
			label += t.Success.Render(" [current]")
		}
		content += marker + label + "  " + sample + "\n"
	}

	content += "\n" + t.Muted.Render("  Up/Down navigate  Enter select  Esc cancel")
	return box.Render(content)
}
