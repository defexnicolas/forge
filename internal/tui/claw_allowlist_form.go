package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// clawAllowlistForm collects a single JID for the allowlist and
// applies it as either an add or a remove. The mode is fixed when the
// form is created — pressing A in the Channels view opens add-mode,
// R opens remove-mode pre-populated with the first existing entry as
// a hint.
type clawAllowlistFormMode int

const (
	clawAllowlistAdd clawAllowlistFormMode = iota
	clawAllowlistRemove
)

type clawAllowlistForm struct {
	input       textinput.Model
	mode        clawAllowlistFormMode
	channelName string
	existing    []string
	done        bool
	canceled    bool
	errMsg      string
	theme       Theme
}

func newClawAllowlistForm(theme Theme, mode clawAllowlistFormMode, channelName string, existing []string) clawAllowlistForm {
	in := textinput.New()
	in.Width = 50
	in.CharLimit = 120
	in.Focus()
	switch mode {
	case clawAllowlistAdd:
		in.Prompt = "  Add JID  "
		in.Placeholder = "5215555555555@s.whatsapp.net"
	case clawAllowlistRemove:
		in.Prompt = "  Remove JID  "
		if len(existing) > 0 {
			in.SetValue(existing[0])
		}
	}
	return clawAllowlistForm{
		input:       in,
		mode:        mode,
		channelName: channelName,
		existing:    existing,
		theme:       theme,
	}
}

func (f clawAllowlistForm) Update(msg tea.Msg) (clawAllowlistForm, tea.Cmd) {
	switch m := msg.(type) {
	case tea.KeyMsg:
		switch m.Type {
		case tea.KeyEsc:
			f.canceled = true
			f.done = true
			return f, nil
		case tea.KeyEnter:
			if strings.TrimSpace(f.input.Value()) == "" {
				f.errMsg = "JID is required"
				return f, nil
			}
			f.done = true
			return f, nil
		}
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return f, cmd
}

func (f clawAllowlistForm) View() string {
	t := f.theme
	title := "Allowlist: add JID"
	if f.mode == clawAllowlistRemove {
		title = "Allowlist: remove JID"
	}
	var b strings.Builder
	b.WriteString(t.TableHeader.Render(title) + "\n\n")
	if f.mode == clawAllowlistRemove && len(f.existing) > 0 {
		b.WriteString(t.Muted.Render("Current entries:") + "\n")
		for i, jid := range f.existing {
			if i >= 8 {
				b.WriteString(t.Muted.Render("  …") + "\n")
				break
			}
			b.WriteString(t.Muted.Render("  • " + jid) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(f.input.View() + "\n")
	if f.errMsg != "" {
		b.WriteString("\n" + t.ErrorStyle.Render(f.errMsg) + "\n")
	}
	b.WriteString("\n" + t.Muted.Render("Enter: confirm   Esc: cancel"))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#5f87d7")).
		Padding(0, 1).
		Width(64)
	return box.Render(b.String())
}

// JID returns the trimmed value the user submitted. Caller checks
// f.canceled before applying.
func (f clawAllowlistForm) JID() string {
	return strings.TrimSpace(f.input.Value())
}
