package tui

import (
	"strings"

	"forge/internal/claw"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// clawIdentityForm lets the user edit Claw's persona without re-running
// the interview. Mirrors providerForm: a fixed-size []textinput.Model,
// Tab/Enter navigation, ApplyInMemory writes back via Service.UpdateIdentity.
//
// Fields kept blank survive untouched — the user can change just one
// thing without overwriting the rest.

const (
	clawIdField int = iota
	clawIdName
	clawIdTone
	clawIdStyle
	clawIdSeed
	clawIdFieldCount
)

type clawIdentityForm struct {
	fields   [clawIdFieldCount]textinput.Model
	focused  int
	done     bool
	canceled bool
	theme    Theme
}

func newClawIdentityForm(current claw.Identity, theme Theme) clawIdentityForm {
	fields := [clawIdFieldCount]textinput.Model{}

	name := textinput.New()
	name.Placeholder = "Claw"
	name.SetValue(current.Name)
	name.Width = 50
	name.Prompt = "  Name  "
	name.Focus()
	fields[clawIdName] = name

	tone := textinput.New()
	tone.Placeholder = "warm | direct | playful | formal"
	tone.SetValue(current.Tone)
	tone.Width = 50
	tone.Prompt = "  Tone  "
	fields[clawIdTone] = tone

	style := textinput.New()
	style.Placeholder = "concise | thorough | terse"
	style.SetValue(current.Style)
	style.Width = 50
	style.Prompt = "  Style "
	fields[clawIdStyle] = style

	seed := textinput.New()
	seed.Placeholder = "single-sentence persona seed"
	seed.SetValue(current.Seed)
	seed.Width = 50
	seed.Prompt = "  Seed  "
	fields[clawIdSeed] = seed

	return clawIdentityForm{
		fields:  fields,
		focused: clawIdName,
		theme:   theme,
	}
}

func (f clawIdentityForm) Update(msg tea.Msg) (clawIdentityForm, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			f.canceled = true
			f.done = true
			return f, nil
		case tea.KeyEnter:
			if f.focused == clawIdSeed {
				f.done = true
				return f, nil
			}
			return f.next(), nil
		case tea.KeyTab, tea.KeyDown:
			return f.next(), nil
		case tea.KeyShiftTab, tea.KeyUp:
			return f.prev(), nil
		}
	}
	var cmd tea.Cmd
	f.fields[f.focused], cmd = f.fields[f.focused].Update(msg)
	return f, cmd
}

func (f clawIdentityForm) next() clawIdentityForm {
	f.fields[f.focused].Blur()
	f.focused = ((f.focused - clawIdName + 1) % (clawIdFieldCount - clawIdName)) + clawIdName
	f.fields[f.focused].Focus()
	return f
}

func (f clawIdentityForm) prev() clawIdentityForm {
	f.fields[f.focused].Blur()
	count := clawIdFieldCount - clawIdName
	f.focused = ((f.focused-clawIdName-1+count)%count) + clawIdName
	f.fields[f.focused].Focus()
	return f
}

func (f clawIdentityForm) View() string {
	t := f.theme
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#5f87d7")).
		Padding(1, 2).
		Width(64)

	var b strings.Builder
	b.WriteString(t.TableHeader.Render("Claw Identity") + "\n\n")
	labels := []string{"Name", "Tone", "Style", "Seed"}
	for i, label := range labels {
		idx := clawIdName + i
		indicator := "  "
		if f.focused == idx {
			indicator = t.IndicatorAgent.Render("* ")
		}
		b.WriteString(indicator + t.StatusKey.Render(label+": ") + f.fields[idx].View() + "\n")
	}
	b.WriteString("\n" + t.Muted.Render("Tab: next  Enter: save (on Seed)  Esc: cancel"))
	b.WriteString("\n" + t.Muted.Render("Empty fields keep the existing value."))
	return box.Render(b.String())
}

// ApplyInMemory writes the form values back via Service.UpdateIdentity.
// Returns a status message for the TUI.
func (f clawIdentityForm) ApplyInMemory(svc *claw.Service) string {
	if f.canceled {
		return "Claw identity edit canceled."
	}
	if svc == nil {
		return f.theme.ErrorStyle.Render("Claw service unavailable.")
	}
	if err := svc.UpdateIdentity(
		f.fields[clawIdName].Value(),
		f.fields[clawIdTone].Value(),
		f.fields[clawIdStyle].Value(),
		f.fields[clawIdSeed].Value(),
	); err != nil {
		return f.theme.ErrorStyle.Render("UpdateIdentity failed: " + err.Error())
	}
	return f.theme.Success.Render("Claw identity updated.")
}
