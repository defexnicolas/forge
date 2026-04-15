package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type confirmForm struct {
	message   string
	selected  int // 0 = Yes, 1 = No
	done      bool
	confirmed bool
	theme     Theme
}

func newConfirmForm(message string, theme Theme) confirmForm {
	return newConfirmFormWithDefault(message, theme, true)
}

func newConfirmFormWithDefault(message string, theme Theme, defaultYes bool) confirmForm {
	selected := 0
	if !defaultYes {
		selected = 1
	}
	return confirmForm{
		message:  message,
		selected: selected,
		theme:    theme,
	}
}

func (f confirmForm) Update(msg tea.Msg) confirmForm {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			f.done = true
			f.confirmed = false
			return f
		case tea.KeyLeft, tea.KeyRight, tea.KeyTab:
			f.selected = 1 - f.selected
		case tea.KeyEnter:
			f.done = true
			f.confirmed = f.selected == 0
		default:
			switch msg.String() {
			case "y", "Y":
				f.done = true
				f.confirmed = true
			case "n", "N":
				f.done = true
				f.confirmed = false
			}
		}
	}
	return f
}

func (f confirmForm) View() string {
	t := f.theme
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#d7af5f")).
		Padding(1, 2).
		Width(50)

	content := t.Warning.Render("  "+f.message) + "\n\n"

	yesStyle := t.Muted
	noStyle := t.Muted
	if f.selected == 0 {
		yesStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("78")).
			Foreground(lipgloss.Color("0")).
			Bold(true).
			Padding(0, 2)
	} else {
		noStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("203")).
			Foreground(lipgloss.Color("0")).
			Bold(true).
			Padding(0, 2)
	}

	yes := yesStyle.Render("Yes")
	no := noStyle.Render("No")

	if f.selected != 0 {
		yes = t.Muted.Render("  Yes  ")
	}
	if f.selected != 1 {
		no = t.Muted.Render("  No  ")
	}

	content += "       " + yes + "     " + no + "\n\n"
	content += t.Muted.Render("  Left/Right select  Enter confirm  Esc cancel")

	return box.Render(content)
}
