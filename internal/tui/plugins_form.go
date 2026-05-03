package tui

import (
	"strconv"
	"strings"

	"forge/internal/plugins"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// pluginsForm shows every plugin discovered for the current scope and
// lets the user toggle enable/disable. Toggling persists immediately to
// .forge/plugins.json (workspace scope) so a restart reflects the choice.
//
// The Hub-scope variant operates on the workspace whose CWD the form was
// constructed with — there is intentionally no global enable/disable file
// today, because plugins are still discovered per-workspace and the same
// directory may live under more than one project's .forge/plugins/.
type pluginsForm struct {
	cwd       string
	discovered []plugins.Plugin
	disabled  map[string]bool
	cursor    int
	done      bool
	canceled  bool
	statusMsg string
	theme     Theme
}

func newPluginsForm(cwd string, theme Theme) (pluginsForm, error) {
	mgr := plugins.NewManager(cwd)
	discovered, err := mgr.Discover()
	if err != nil {
		return pluginsForm{}, err
	}
	state := plugins.LoadEnabledState(cwd)
	dis := map[string]bool{}
	for k, v := range state.Disabled {
		dis[k] = v
	}
	return pluginsForm{
		cwd:        cwd,
		discovered: discovered,
		disabled:   dis,
		theme:      theme,
	}, nil
}

func (f pluginsForm) Update(msg tea.Msg) (pluginsForm, tea.Cmd) {
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
			if f.cursor < len(f.discovered)-1 {
				f.cursor++
			}
			return f, nil
		case tea.KeyEnter, tea.KeySpace:
			if f.cursor < len(f.discovered) {
				name := f.discovered[f.cursor].Name
				if f.disabled[name] {
					delete(f.disabled, name)
					f.statusMsg = "enabled " + name + " (restart workspace to load)"
				} else {
					f.disabled[name] = true
					f.statusMsg = "disabled " + name + " (restart workspace to drop)"
				}
				if err := plugins.SaveEnabledState(f.cwd, plugins.EnabledState{Disabled: f.disabled}); err != nil {
					f.statusMsg = "save failed: " + err.Error()
				}
			}
			return f, nil
		case tea.KeyRunes:
			if string(msg.Runes) == "q" {
				f.done = true
				return f, nil
			}
		}
	}
	return f, nil
}

func (f pluginsForm) View() string {
	t := f.theme
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#5f87d7")).
		Padding(1, 2).
		Width(78)

	var b strings.Builder
	b.WriteString(t.TableHeader.Render("Plugins") + "\n\n")
	if len(f.discovered) == 0 {
		b.WriteString(t.Muted.Render("No plugins discovered. Drop one under .forge/plugins/, .claude/plugins/, ~/.forge/plugins/ or ~/.claude/plugins/."))
		b.WriteString("\n\n" + t.Muted.Render("Esc: close"))
		return box.Render(b.String())
	}
	for i, p := range f.discovered {
		marker := "  "
		if i == f.cursor {
			marker = t.IndicatorAgent.Render("> ")
		}
		state := "[x]"
		if f.disabled[p.Name] {
			state = "[ ]"
		}
		line := marker + state + " " + p.Name
		if p.Source != "" {
			line += t.Muted.Render("  ("+p.Source+")")
		}
		b.WriteString(line + "\n")
		if comps := p.SupportedComponents(); len(comps) > 0 {
			b.WriteString(t.Muted.Render("       "+strings.Join(comps, ", ")) + "\n")
		}
	}
	b.WriteString("\n")
	if f.statusMsg != "" {
		b.WriteString(t.Success.Render(f.statusMsg) + "\n")
	}
	b.WriteString(t.Muted.Render("Up/Down: select  Enter/Space: toggle  q: close  ("+strconv.Itoa(len(f.discovered))+" discovered)"))
	return box.Render(b.String())
}
