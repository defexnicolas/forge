package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

type yarnMenuItem struct {
	Label      string
	Subcommand string
	NeedsArg   bool
	ArgPrompt  string
}

type yarnMenuForm struct {
	items       []yarnMenuItem
	cursor      int
	awaitingArg bool
	input       textinput.Model
	done        bool
	canceled    bool
	result      string
	theme       Theme
}

func newYarnMenuForm(theme Theme) yarnMenuForm {
	ti := textinput.New()
	ti.CharLimit = 512
	ti.Width = 56

	return yarnMenuForm{
		items: []yarnMenuItem{
			{Label: "Settings (form)", Subcommand: "settings"},
			{Label: "List profiles", Subcommand: "profiles"},
			{Label: "Apply profile", Subcommand: "profile", NeedsArg: true, ArgPrompt: "2B | 4B | 9B | 14B | 26B"},
			{Label: "Dry-run prompt", Subcommand: "dry-run", NeedsArg: true, ArgPrompt: "prompt to preview (e.g. analiza @path/to/file)"},
			{Label: "Show graph", Subcommand: "graph"},
			{Label: "Inspect node", Subcommand: "inspect", NeedsArg: true, ArgPrompt: "node id"},
			{Label: "Probe context window", Subcommand: "probe"},
			{Label: "Status snapshot", Subcommand: "status"},
		},
		theme: theme,
		input: ti,
	}
}

func (f yarnMenuForm) Update(msg tea.Msg) (yarnMenuForm, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyMsg)
	if !isKey {
		if f.awaitingArg {
			var cmd tea.Cmd
			f.input, cmd = f.input.Update(msg)
			return f, cmd
		}
		return f, nil
	}

	if f.awaitingArg {
		switch keyMsg.Type {
		case tea.KeyEsc:
			f.awaitingArg = false
			f.input.Blur()
			f.input.SetValue("")
			return f, nil
		case tea.KeyEnter:
			value := strings.TrimSpace(f.input.Value())
			if value == "" {
				return f, nil
			}
			f.result = f.items[f.cursor].Subcommand + " " + value
			f.done = true
			return f, nil
		}
		var cmd tea.Cmd
		f.input, cmd = f.input.Update(msg)
		return f, cmd
	}

	switch keyMsg.Type {
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
		if f.cursor < len(f.items)-1 {
			f.cursor++
		}
		return f, nil
	case tea.KeyEnter:
		item := f.items[f.cursor]
		if item.NeedsArg {
			f.awaitingArg = true
			f.input.Placeholder = item.ArgPrompt
			f.input.SetValue("")
			f.input.Focus()
			return f, textinput.Blink
		}
		f.result = item.Subcommand
		f.done = true
		return f, nil
	}
	return f, nil
}

func (f yarnMenuForm) View() string {
	t := f.theme
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#af87d7")).
		Padding(1, 2).
		Width(64)

	content := t.TableHeader.Render("YARN Menu") + "\n\n"

	if f.awaitingArg {
		item := f.items[f.cursor]
		content += t.StatusKey.Render("  /yarn "+item.Subcommand) + "\n\n"
		content += "  " + f.input.View() + "\n\n"
		content += t.Muted.Render("  Enter confirm  Esc back")
		return box.Render(content)
	}

	for i, item := range f.items {
		marker := "  "
		if i == f.cursor {
			marker = t.IndicatorAgent.Render("> ")
		}
		label := t.StatusValue.Render(item.Label)
		hint := t.Muted.Render("  /yarn " + item.Subcommand)
		content += marker + label + hint + "\n"
	}
	content += "\n" + t.Muted.Render("  Up/Down navigate  Enter select  Esc cancel")
	return box.Render(content)
}
