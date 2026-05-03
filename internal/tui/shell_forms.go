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
	hubFormWebSearch
	hubFormOutputStyle
	hubFormPlugins
	hubFormWhatsApp
	hubFormClawIdentity
	hubFormClawHeartbeat
	hubFormClawAllowlist
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
	case hubFormWebSearch:
		var cmd tea.Cmd
		m.webSearchForm, cmd = m.webSearchForm.Update(msg)
		if m.webSearchForm.done {
			result := "Web Search settings canceled."
			if !m.webSearchForm.canceled {
				if cfg, ok := m.loadHubSettingsConfig(); ok {
					result = stripAnsi(m.webSearchForm.ApplyInMemory(&cfg))
					if err := saveHubGlobalConfig(cfg); err != nil {
						result = "Global save failed: " + err.Error()
					}
				}
			}
			m.statusMessage = result
			m.activeHubForm = hubFormNone
		}
		return *m, cmd, true
	case hubFormOutputStyle:
		var cmd tea.Cmd
		m.outputStyleForm, cmd = m.outputStyleForm.Update(msg)
		if m.outputStyleForm.done {
			result := "Output style change canceled."
			if !m.outputStyleForm.canceled {
				if cfg, ok := m.loadHubSettingsConfig(); ok {
					result = stripAnsi(m.outputStyleForm.ApplyInMemory(&cfg))
					if err := saveHubGlobalConfig(cfg); err != nil {
						result = "Global save failed: " + err.Error()
					}
				}
			}
			m.statusMessage = result
			m.activeHubForm = hubFormNone
		}
		return *m, cmd, true
	case hubFormPlugins:
		var cmd tea.Cmd
		m.pluginsForm, cmd = m.pluginsForm.Update(msg)
		if m.pluginsForm.done {
			m.statusMessage = "Plugins manager closed."
			m.activeHubForm = hubFormNone
		}
		return *m, cmd, true
	case hubFormClawIdentity:
		var cmd tea.Cmd
		m.clawIdentityForm, cmd = m.clawIdentityForm.Update(msg)
		if m.clawIdentityForm.done {
			m.statusMessage = stripAnsi(m.clawIdentityForm.ApplyInMemory(m.hubClawService()))
			m.activeHubForm = hubFormNone
		}
		return *m, cmd, true
	case hubFormClawHeartbeat:
		var cmd tea.Cmd
		m.clawHeartbeatForm, cmd = m.clawHeartbeatForm.Update(msg)
		if m.clawHeartbeatForm.done {
			result := "Claw cadence canceled."
			if !m.clawHeartbeatForm.canceled {
				if cfg, ok := m.loadHubSettingsConfig(); ok {
					result = stripAnsi(m.clawHeartbeatForm.ApplyInMemory(&cfg))
					if err := saveHubGlobalConfig(cfg); err != nil {
						result = "Global save failed: " + err.Error()
					}
				}
			}
			m.statusMessage = result
			m.activeHubForm = hubFormNone
		}
		return *m, cmd, true
	case hubFormWhatsApp:
		var cmd tea.Cmd
		m.whatsAppForm, cmd = m.whatsAppForm.Update(msg)
		if m.whatsAppForm.done {
			if m.whatsAppForm.canceled {
				m.statusMessage = "WhatsApp pairing canceled."
			} else {
				m.statusMessage = "WhatsApp paired and persisted at ~/.forge/claw/whatsapp.db."
				// Hand the live channel to the Claw service so
				// whatsapp_send + the inbound→memory pump can find
				// it. Done here (not inside the form) to keep the
				// form's state machine free of cross-package wiring.
				if svc := m.hubClawService(); svc != nil {
					if ch := m.whatsAppForm.Channel(); ch != nil {
						svc.RegisterChannel(ch)
					}
				}
			}
			m.activeHubForm = hubFormNone
		}
		return *m, cmd, true
	case hubFormClawAllowlist:
		var cmd tea.Cmd
		m.clawAllowlistForm, cmd = m.clawAllowlistForm.Update(msg)
		if m.clawAllowlistForm.done {
			if m.clawAllowlistForm.canceled {
				m.statusMessage = "Allowlist edit canceled."
			} else {
				jid := m.clawAllowlistForm.JID()
				svc := m.hubClawService()
				switch {
				case svc == nil:
					m.statusMessage = "Claw service unavailable."
				case m.clawAllowlistForm.mode == clawAllowlistAdd:
					if err := svc.AddAllowed(m.clawAllowlistForm.channelName, jid); err != nil {
						m.statusMessage = "Add failed: " + err.Error()
					} else {
						m.statusMessage = "Added to allowlist: " + jid
					}
				case m.clawAllowlistForm.mode == clawAllowlistRemove:
					if err := svc.RemoveAllowed(m.clawAllowlistForm.channelName, jid); err != nil {
						m.statusMessage = "Remove failed: " + err.Error()
					} else {
						m.statusMessage = "Removed from allowlist: " + jid
					}
				}
			}
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
	case hubFormWebSearch:
		return m.webSearchForm.View()
	case hubFormOutputStyle:
		return m.outputStyleForm.View()
	case hubFormPlugins:
		return m.pluginsForm.View()
	case hubFormWhatsApp:
		return m.whatsAppForm.ViewSized(m.hubContentWidth(), m.hubInnerHeight()-1)
	case hubFormClawIdentity:
		return m.clawIdentityForm.View()
	case hubFormClawHeartbeat:
		return m.clawHeartbeatForm.View()
	case hubFormClawAllowlist:
		return m.clawAllowlistForm.View()
	default:
		return ""
	}
}
