package tui

import (
	"forge/internal/config"
	"forge/internal/globalconfig"

	tea "github.com/charmbracelet/bubbletea"
)

type hubFormMode int

const (
	hubFormNone hubFormMode = iota
	hubFormProvider
	hubFormModel
	hubFormModelMulti
	hubFormYarn
	hubFormTheme
	hubFormSkills
)

func (m *shellModel) handleHubFormUpdate(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch m.activeHubForm {
	case hubFormProvider:
		var cmd tea.Cmd
		m.providerForm, cmd = m.providerForm.Update(msg)
		if m.providerForm.done {
			result := "Provider config canceled."
			if !m.providerForm.canceled {
				if cfg, ok := m.loadHubSettingsConfig(); ok {
					result = stripAnsi(m.providerForm.ApplyInMemory(&cfg, hubSettingsProviders(cfg)))
					config.InheritChatModelDefaults(&cfg)
					if err := saveHubGlobalConfig(cfg); err != nil {
						result = "Global save failed: " + err.Error()
					} else {
						m.applyHubChatConfig(cfg)
					}
				}
			}
			m.statusMessage = result
			m.activeHubForm = hubFormNone
		}
		return *m, cmd, true
	case hubFormModel:
		var cmd tea.Cmd
		m.modelForm, cmd = m.modelForm.Update(msg)
		if m.modelForm.done {
			result := "Model selection canceled."
			if !m.modelForm.canceled {
				if cfg, ok := m.loadHubSettingsConfig(); ok {
					result = stripAnsi(m.modelForm.ApplyRoleInMemory(&cfg, "chat"))
					config.InheritChatModelDefaults(&cfg)
					if err := saveHubGlobalConfig(cfg); err != nil {
						result = "Global save failed: " + err.Error()
					} else {
						m.applyHubChatConfig(cfg)
					}
				}
			}
			m.statusMessage = result
			m.activeHubForm = hubFormNone
		}
		return *m, cmd, true
	case hubFormModelMulti:
		var cmd tea.Cmd
		m.modelMultiForm, cmd = m.modelMultiForm.Update(msg)
		if m.modelMultiForm.done {
			result := stripAnsi(m.modelMultiForm.Result())
			if !m.modelMultiForm.canceled && m.modelMultiForm.errMsg == "" {
				if err := saveHubGlobalConfig(m.modelMultiForm.cfg); err != nil {
					result = "Global save failed: " + err.Error()
				} else {
					m.applyHubChatConfig(m.modelMultiForm.cfg)
				}
			}
			m.statusMessage = result
			m.activeHubForm = hubFormNone
		}
		return *m, cmd, true
	case hubFormYarn:
		var cmd tea.Cmd
		m.yarnSettingsForm, cmd = m.yarnSettingsForm.Update(msg)
		if m.yarnSettingsForm.done {
			result := "YARN settings canceled."
			if !m.yarnSettingsForm.canceled {
				if cfg, ok := m.loadHubSettingsConfig(); ok {
					result = stripAnsi(m.yarnSettingsForm.ApplyInMemory(&cfg))
					if err := saveHubGlobalConfig(cfg); err != nil {
						result = "Global save failed: " + err.Error()
					} else {
						m.applyHubChatConfig(cfg)
					}
				}
			}
			m.statusMessage = result
			m.activeHubForm = hubFormNone
		}
		return *m, cmd, true
	case hubFormTheme:
		var cmd tea.Cmd
		m.themeForm, cmd = m.themeForm.Update(msg)
		if m.themeForm.done {
			result := "Theme change canceled."
			if !m.themeForm.canceled && m.themeForm.chosen != "" {
				m.theme = GetTheme(m.themeForm.chosen)
				if err := globalconfig.SetTheme(m.themeForm.chosen); err != nil {
					result = "Theme persisted in session, but global save failed: " + err.Error()
				} else {
					result = "Theme set globally to " + m.themeForm.chosen
				}
				// Propagate to the workspace's renderer if one is open so
				// the change is visible immediately, not after restart.
				if m.workspace != nil {
					m.workspace.theme = m.theme
					m.workspace.refresh()
				}
				if m.hubChat != nil {
					m.hubChat.theme = m.theme
					m.hubChat.refresh()
				}
			}
			m.statusMessage = result
			m.activeHubForm = hubFormNone
		}
		return *m, cmd, true
	case hubFormSkills:
		var cmd tea.Cmd
		m.skillsForm, cmd = m.skillsForm.Update(msg)
		if m.skillsForm.done {
			m.statusMessage = "Skills browser closed."
			m.activeHubForm = hubFormNone
		}
		return *m, cmd, true
	default:
		return *m, nil, false
	}
}

func (m shellModel) activeHubFormView() string {
	switch m.activeHubForm {
	case hubFormProvider:
		return m.providerForm.View()
	case hubFormModel:
		return m.modelForm.View()
	case hubFormModelMulti:
		return m.modelMultiForm.View()
	case hubFormYarn:
		return m.yarnSettingsForm.View()
	case hubFormTheme:
		return m.themeForm.View()
	case hubFormSkills:
		return m.skillsForm.View()
	default:
		return ""
	}
}
