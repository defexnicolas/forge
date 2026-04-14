package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
)

// handleFormUpdate processes input when a form is active. Returns true if handled.
func (m *model) handleFormUpdate(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	var cmds []tea.Cmd

	if m.activeForm == formProvider {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			var cmd tea.Cmd
			m.providerForm, cmd = m.providerForm.Update(msg)
			cmds = append(cmds, cmd)
			if m.providerForm.done {
				m.activeForm = formNone
				result := m.providerForm.Apply(&m.options.Config, m.options.Providers)
				m.agentRuntime.Config = m.options.Config
				m.history = append(m.history, result)
				m.refresh()
			}
			return m, tea.Batch(cmds...), true
		default:
			var cmd tea.Cmd
			m.providerForm, cmd = m.providerForm.Update(msg)
			return m, cmd, true
		}
	}

	if m.activeForm == formSkills {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			var cmd tea.Cmd
			m.skillsForm, cmd = m.skillsForm.Update(msg)
			cmds = append(cmds, cmd)
			if m.skillsForm.done {
				m.activeForm = formNone
				m.history = append(m.history, m.skillsForm.Result())
				m.refresh()
			}
			return m, tea.Batch(cmds...), true
		default:
			var cmd tea.Cmd
			m.skillsForm, cmd = m.skillsForm.Update(msg)
			return m, cmd, true
		}
	}

	if m.activeForm == formTheme {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			var cmd tea.Cmd
			m.themeForm, cmd = m.themeForm.Update(msg)
			cmds = append(cmds, cmd)
			if m.themeForm.done {
				m.activeForm = formNone
				if !m.themeForm.canceled && m.themeForm.chosen != "" {
					m.theme = GetTheme(m.themeForm.chosen)
					m.history = append(m.history, m.theme.Success.Render("Theme: "+m.theme.Name))
				}
				m.refresh()
			}
			return m, tea.Batch(cmds...), true
		default:
			return m, nil, true
		}
	}

	if m.activeForm == formModel {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			var cmd tea.Cmd
			m.modelForm, cmd = m.modelForm.Update(msg)
			cmds = append(cmds, cmd)
			if m.modelForm.done {
				m.activeForm = formNone
				if !m.modelForm.canceled && m.modelForm.chosen != "" {
					result := m.modelForm.Apply(&m.options.Config)
					m.agentRuntime.Config = m.options.Config
					m.history = append(m.history, result)
				}
				m.refresh()
			}
			return m, tea.Batch(cmds...), true
		default:
			return m, nil, true
		}
	}

	if m.activeForm == formConfirmExecute {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			m.confirmExecute = m.confirmExecute.Update(msg)
			if m.confirmExecute.done {
				m.activeForm = formNone
				if m.confirmExecute.confirmed {
					_ = m.agentRuntime.SetMode("build")
					m.history = append(m.history, m.theme.Success.Render("  Switched to build mode."))
					m.history = append(m.history, m.theme.SeparatorLine(m.width-4))
					m.history = append(m.history, m.theme.IndicatorAgent.Render("* ")+m.theme.AgentPrefix.Render("forge [build]"))
					m.history = append(m.history, "")
					m.agentEvents = m.agentRuntime.Run(context.Background(), m.pendingExecuteLine)
					m.agentRunning = true
					m.refresh()
					return m, waitForAgentEvent(m.agentEvents), true
				}
				m.history = append(m.history, m.theme.Muted.Render("  Staying in plan mode."))
				m.refresh()
			}
			return m, nil, true
		default:
			return m, nil, true
		}
	}

	if m.activeForm == formYarnSettings {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			var cmd tea.Cmd
			m.yarnSettingsForm, cmd = m.yarnSettingsForm.Update(msg)
			cmds = append(cmds, cmd)
			if m.yarnSettingsForm.done {
				m.activeForm = formNone
				if !m.yarnSettingsForm.canceled {
					result := m.yarnSettingsForm.Apply(&m.options.Config)
					m.syncRuntimeConfig()
					m.history = append(m.history, result)
				} else {
					m.history = append(m.history, m.theme.Muted.Render("YARN settings canceled."))
				}
				m.refresh()
			}
			return m, tea.Batch(cmds...), true
		default:
			var cmd tea.Cmd
			m.yarnSettingsForm, cmd = m.yarnSettingsForm.Update(msg)
			return m, cmd, true
		}
	}

	if m.searching {
		switch msg.(type) {
		case tea.KeyMsg:
			var done bool
			m.searchMode, done = m.searchMode.Update(msg)
			if done {
				m.searching = false
				m.input.Focus()
			}
			m.refresh()
			return m, nil, true
		}
	}

	return m, nil, false
}
