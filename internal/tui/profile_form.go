package tui

import (
	"forge/internal/permissions"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type profileForm struct {
	profiles []permissions.Profile
	current  string
	selected int
	done     bool
	canceled bool
	chosen   string
	theme    Theme
}

func newProfileForm(current string, t Theme) profileForm {
	names := permissions.ProfileNames()
	profiles := make([]permissions.Profile, 0, len(names))
	selected := 0
	for i, name := range names {
		p, _ := permissions.GetProfile(name)
		profiles = append(profiles, p)
		if name == current {
			selected = i
		}
	}
	return profileForm{
		profiles: profiles,
		current:  current,
		selected: selected,
		theme:    t,
	}
}

func (f profileForm) Update(msg tea.Msg) (profileForm, tea.Cmd) {
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
			if f.selected < len(f.profiles)-1 {
				f.selected++
			}
			return f, nil
		case tea.KeyEnter:
			if f.selected < len(f.profiles) {
				f.chosen = f.profiles[f.selected].Name
				f.done = true
			}
			return f, nil
		}
	}
	return f, nil
}

func (f profileForm) View() string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#af87d7")).
		Padding(1, 2).
		Width(72)

	t := f.theme
	content := t.TableHeader.Render("Permission Profile") + "\n\n"

	for i, p := range f.profiles {
		marker := "  "
		if i == f.selected {
			marker = t.IndicatorAgent.Render("> ")
		}
		label := t.StatusValue.Render(p.Name)
		if p.Name == f.current {
			label += t.Success.Render(" [current]")
		}
		content += marker + label + "\n"
		content += "    " + t.Muted.Render(p.Description) + "\n"
	}

	content += "\n" + t.Muted.Render("  Up/Down navigate  Enter select  Esc cancel")
	return box.Render(content)
}
