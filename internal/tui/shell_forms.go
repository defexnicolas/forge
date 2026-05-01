package tui

import tea "github.com/charmbracelet/bubbletea"

type hubFormMode int

const (
	hubFormNone hubFormMode = iota
	hubFormProvider
	hubFormModel
	hubFormModelMulti
	hubFormYarn
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
					result = stripAnsi(m.providerForm.Apply(&cfg, hubSettingsProviders(cfg)))
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
					result = stripAnsi(m.modelForm.Apply(&cfg))
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
			m.statusMessage = stripAnsi(m.modelMultiForm.Result())
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
					result = stripAnsi(m.yarnSettingsForm.Apply(&cfg))
				}
			}
			m.statusMessage = result
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
	default:
		return ""
	}
}
