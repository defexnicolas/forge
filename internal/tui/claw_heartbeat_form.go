package tui

import (
	"strconv"
	"strings"

	"forge/internal/config"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// clawHeartbeatForm bundles the two cadence settings the user actually
// touches (heartbeat seconds + dream minutes) into one form. Splitting
// them across two tabs would clutter the submenu without any UX win —
// they are read together and tuned together.

const (
	clawHbHeartbeatSeconds int = iota
	clawHbDreamMinutes
	clawHbToolsEnabled
	clawHbFieldCount
)

type clawHeartbeatForm struct {
	fields   [clawHbFieldCount]textinput.Model
	focused  int
	done     bool
	canceled bool
	errMsg   string
	theme    Theme
}

func newClawHeartbeatForm(current config.ClawConfig, theme Theme) clawHeartbeatForm {
	fields := [clawHbFieldCount]textinput.Model{}

	hb := textinput.New()
	hb.Placeholder = "30"
	hb.SetValue(strconv.Itoa(current.HeartbeatIntervalSeconds))
	hb.Width = 20
	hb.Prompt = "  Heartbeat (s)  "
	hb.CharLimit = 5
	hb.Focus()
	fields[clawHbHeartbeatSeconds] = hb

	dr := textinput.New()
	dr.Placeholder = "180"
	dr.SetValue(strconv.Itoa(current.DreamIntervalMinutes))
	dr.Width = 20
	dr.Prompt = "  Dream (min)    "
	dr.CharLimit = 6
	fields[clawHbDreamMinutes] = dr

	te := textinput.New()
	te.Placeholder = "false"
	teVal := "false"
	if current.ToolsEnabled {
		teVal = "true"
	}
	te.SetValue(teVal)
	te.Width = 20
	te.Prompt = "  Tools (true/false)"
	te.CharLimit = 5
	fields[clawHbToolsEnabled] = te

	return clawHeartbeatForm{
		fields:  fields,
		focused: clawHbHeartbeatSeconds,
		theme:   theme,
	}
}

func (f clawHeartbeatForm) Update(msg tea.Msg) (clawHeartbeatForm, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			f.canceled = true
			f.done = true
			return f, nil
		case tea.KeyEnter:
			if f.focused == clawHbToolsEnabled {
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

func (f clawHeartbeatForm) next() clawHeartbeatForm {
	f.fields[f.focused].Blur()
	f.focused = (f.focused + 1) % clawHbFieldCount
	f.fields[f.focused].Focus()
	return f
}

func (f clawHeartbeatForm) prev() clawHeartbeatForm {
	f.fields[f.focused].Blur()
	f.focused = (f.focused - 1 + clawHbFieldCount) % clawHbFieldCount
	f.fields[f.focused].Focus()
	return f
}

func (f clawHeartbeatForm) View() string {
	t := f.theme
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#5f87d7")).
		Padding(1, 2).
		Width(56)

	var b strings.Builder
	b.WriteString(t.TableHeader.Render("Claw Cadences + Tools") + "\n\n")
	labels := []string{"Heartbeat (s)", "Dream (min)", "Tools enabled"}
	for i, label := range labels {
		indicator := "  "
		if f.focused == i {
			indicator = t.IndicatorAgent.Render("* ")
		}
		b.WriteString(indicator + t.StatusKey.Render(label+": ") + f.fields[i].View() + "\n")
	}
	b.WriteString("\n" + t.Muted.Render("Heartbeat: how often Claw checks crons + heartbeats."))
	b.WriteString("\n" + t.Muted.Render("Dream:     how often Claw consolidates memory."))
	b.WriteString("\n" + t.Muted.Render("Tools:     true = Claw can call web_search/web_fetch/whatsapp_send."))
	b.WriteString("\n" + t.Muted.Render("           Default false avoids accidental Ollama API spend."))
	if f.errMsg != "" {
		b.WriteString("\n\n" + t.ErrorStyle.Render(f.errMsg))
	}
	b.WriteString("\n\n" + t.Muted.Render("Tab: next  Enter: save (on Tools)  Esc: cancel"))
	return box.Render(b.String())
}

// ApplyInMemory writes the form values into cfg.Claw and returns a
// status message. The caller persists with globalconfig.Save (via
// saveHubGlobalConfig).
func (f clawHeartbeatForm) ApplyInMemory(cfg *config.Config) string {
	if f.canceled {
		return "Claw cadence edit canceled."
	}
	hbStr := strings.TrimSpace(f.fields[clawHbHeartbeatSeconds].Value())
	drStr := strings.TrimSpace(f.fields[clawHbDreamMinutes].Value())
	teStr := strings.ToLower(strings.TrimSpace(f.fields[clawHbToolsEnabled].Value()))
	hb, err := strconv.Atoi(hbStr)
	if err != nil || hb < 1 {
		return f.theme.ErrorStyle.Render("Heartbeat must be a positive integer (seconds).")
	}
	dr, err := strconv.Atoi(drStr)
	if err != nil || dr < 0 {
		return f.theme.ErrorStyle.Render("Dream must be a non-negative integer (minutes; 0 disables dreams).")
	}
	if teStr != "true" && teStr != "false" && teStr != "yes" && teStr != "no" && teStr != "1" && teStr != "0" {
		return f.theme.ErrorStyle.Render("Tools enabled must be true / false (or yes/no, 1/0).")
	}
	te := teStr == "true" || teStr == "yes" || teStr == "1"
	cfg.Claw.HeartbeatIntervalSeconds = hb
	cfg.Claw.DreamIntervalMinutes = dr
	cfg.Claw.ToolsEnabled = te
	return f.theme.Success.Render(
		"Claw cadences updated: heartbeat=" + strconv.Itoa(hb) + "s dream=" + strconv.Itoa(dr) + "m tools=" + strconv.FormatBool(te),
	)
}
