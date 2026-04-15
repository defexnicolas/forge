package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"forge/internal/agent"
	"forge/internal/tools"

	tea "github.com/charmbracelet/bubbletea"
)

// handleFormUpdate processes input when a form is active. Returns true if handled.
func (m *model) handleFormUpdate(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	defer m.recalcLayout()
	var cmds []tea.Cmd

	if m.activeForm == formApproval {
		var cmd tea.Cmd
		m.approvalForm, cmd = m.approvalForm.Update(msg)
		cmds = append(cmds, cmd)
		if m.approvalForm.done {
			req := m.approvalForm.request
			approved := m.approvalForm.approved
			m.activeForm = formNone
			if req != nil {
				req.Response <- agent.ApprovalResponse{Approved: approved}
				var result string
				if approved {
					result = m.theme.Success.Render("Approved: " + req.Summary)
				} else {
					result = m.theme.Warning.Render("Rejected: " + req.Summary)
				}
				m.history = append(m.history, result)
			}
			m.pendingApproval = nil
			m.refresh()
		}
		return m, tea.Batch(cmds...), true
	}

	if m.activeForm == formAskUser {
		var cmd tea.Cmd
		m.askUserForm, cmd = m.askUserForm.Update(msg)
		cmds = append(cmds, cmd)
		if m.askUserForm.done {
			req := m.askUserForm.request
			answer := strings.TrimSpace(m.askUserForm.answer)
			m.activeForm = formNone
			if req != nil {
				req.Response <- answer
				if answer == "" {
					m.history = append(m.history, "    "+m.theme.Muted.Render("→ (skipped)"))
				} else {
					for _, line := range strings.Split(answer, "\n") {
						m.history = append(m.history, "    "+m.theme.Muted.Render("→ ")+line)
					}
				}
			}
			m.pendingAskUser = nil
			m.refresh()
		}
		return m, tea.Batch(cmds...), true
	}

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
		var cmd tea.Cmd
		m.modelForm, cmd = m.modelForm.Update(msg)
		cmds = append(cmds, cmd)
		if m.modelForm.done {
			m.activeForm = formNone
			if !m.modelForm.canceled {
				result := m.modelForm.Apply(&m.options.Config)
				m.agentRuntime.Config = m.options.Config
				m.agentRuntime.Builder.Config = m.options.Config
				m.agentRuntime.SetChatModel(m.options.Config.Models["chat"])
				if result != "" && m.agentRuntime.ActiveParserName != "" {
					result += "\n" + m.theme.Muted.Render(fmt.Sprintf("family=%s parser=%s", m.agentRuntime.ActiveModelFamily, m.agentRuntime.ActiveParserName))
				}
				if result != "" {
					m.history = append(m.history, result)
				}
			}
			m.refresh()
		}
		return m, tea.Batch(cmds...), true
	}

	if m.activeForm == formModelMulti {
		var cmd tea.Cmd
		m.modelMultiForm, cmd = m.modelMultiForm.Update(msg)
		cmds = append(cmds, cmd)
		if m.modelMultiForm.done {
			m.activeForm = formNone
			if !m.modelMultiForm.canceled && m.modelMultiForm.errMsg == "" {
				m.options.Config = m.modelMultiForm.cfg
				m.agentRuntime.Config = m.options.Config
				m.agentRuntime.Builder.Config = m.options.Config
				for _, selection := range m.modelMultiForm.selections {
					m.agentRuntime.SetRoleModel(selection.role, selection.modelID)
					if selection.detected != nil && selection.detected.LoadedContextLength > 0 {
						m.agentRuntime.MarkModelLoaded(selection.modelID)
					}
				}
				result := m.modelMultiForm.Result()
				if result != "" {
					for _, selection := range m.modelMultiForm.selections {
						parser := ""
						if m.agentRuntime.Parsers != nil {
							parser = m.agentRuntime.Parsers.ForModel(selection.modelID).Name()
						}
						result += "\n" + m.theme.Muted.Render(fmt.Sprintf("%s family=%s parser=%s", selection.role, agent.DetectModelFamily(selection.modelID), parser))
					}
				}
				if result != "" {
					m.history = append(m.history, result)
				}
			} else if result := m.modelMultiForm.Result(); result != "" {
				m.history = append(m.history, result)
			}
			m.refresh()
		}
		return m, tea.Batch(cmds...), true
	}

	if m.activeForm == formConfirmExecute {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			m.confirmExecute = m.confirmExecute.Update(msg)
			if m.confirmExecute.done {
				m.activeForm = formNone
				if m.confirmExecute.confirmed {
					cmd := m.runBuildWithPreflight(m.pendingExecuteLine)
					m.refresh()
					return m, cmd, true
				}
				m.pendingExecuteLine = ""
				m.history = append(m.history, m.theme.Muted.Render("Plan left pending. Use /mode build or type an execute request when ready."))
				m.refresh()
			}
			return m, nil, true
		default:
			return m, nil, true
		}
	}

	if m.activeForm == formConfirmPlanReset {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			m.confirmPlanReset = m.confirmPlanReset.Update(msg)
			if m.confirmPlanReset.done {
				m.activeForm = formNone
				line := m.pendingPlanLine
				m.pendingPlanLine = ""
				cleared := false
				if m.confirmPlanReset.confirmed {
					if m.agentRuntime.Plans != nil {
						_ = m.agentRuntime.Plans.Clear()
					}
					if m.agentRuntime.Tasks != nil {
						_, _ = m.agentRuntime.Tasks.ReplacePlan(nil)
					}
					m.history = append(m.history, m.theme.Success.Render("Prior plan and todos cleared."))
					cleared = true
				}
				m.agentEvents = m.agentRuntime.Run(context.Background(), planInterviewPrompt(line, cleared))
				m.agentRunning = true
				m.refresh()
				return m, waitForAgentEvent(m.agentEvents), true
			}
			return m, nil, true
		default:
			return m, nil, true
		}
	}

	if m.activeForm == formConfirmExplorerPlan {
		switch msg.(type) {
		case tea.KeyMsg:
			m.confirmExplorerPlan = m.confirmExplorerPlan.Update(msg)
			if m.confirmExplorerPlan.done {
				m.activeForm = formNone
				if m.confirmExplorerPlan.confirmed {
					m.agentRuntime.PendingExplorerContext = m.pendingExplorerHandoff
					m.pendingExplorerHandoff = ""
					_ = m.agentRuntime.SetMode("plan")
					m.history = append(m.history, m.theme.SeparatorLine(m.width-4))
					m.history = append(m.history, m.theme.IndicatorAgent.Render("* ")+m.theme.AgentPrefix.Render("forge"))
					m.history = append(m.history, "")
					m.agentEvents = m.agentRuntime.Run(context.Background(), "Create or refine the plan from the explorer findings. Confirm what Explorer found and turn it into actionable tasks.")
					m.agentRunning = true
					m.showPlan = true
					m.refresh()
					return m, waitForAgentEvent(m.agentEvents), true
				}
				m.pendingExplorerHandoff = ""
				m.history = append(m.history, m.theme.Muted.Render("Explorer findings kept in the conversation; Plan handoff canceled."))
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

	if m.activeForm == formYarnMenu {
		var cmd tea.Cmd
		m.yarnMenuForm, cmd = m.yarnMenuForm.Update(msg)
		cmds = append(cmds, cmd)
		if m.yarnMenuForm.done {
			result := m.yarnMenuForm.result
			canceled := m.yarnMenuForm.canceled
			m.activeForm = formNone
			if canceled || result == "" {
				m.refresh()
				return m, tea.Batch(cmds...), true
			}
			if result == "settings" {
				m.activeForm = formYarnSettings
				m.yarnSettingsForm = newYarnSettingsForm(m.options.CWD, m.options.Config, m.theme)
				m.refresh()
				return m, tea.Batch(cmds...), true
			}
			fields := append([]string{"/yarn"}, strings.Fields(result)...)
			out := m.handleYarnCommand(fields)
			if out != "" {
				m.history = append(m.history, out)
			}
			m.refresh()
		}
		return m, tea.Batch(cmds...), true
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
			if m.searchMode.jumpPending && len(m.searchMode.positions) > 0 {
				target := m.searchMode.positions[m.searchMode.currentIdx]
				offset := target - m.viewport.Height/2
				if offset < 0 {
					offset = 0
				}
				m.viewport.SetYOffset(offset)
				m.stickyBottom = false
				m.searchMode.jumpPending = false
			}
			return m, nil, true
		}
	}

	return m, nil, false
}

func (m *model) runBuildWithPreflight(line string) tea.Cmd {
	line = strings.TrimSpace(line)
	if line == "" {
		line = "Execute the approved plan."
	}
	_ = m.agentRuntime.SetMode("build")
	m.pendingExecuteLine = ""
	m.history = append(m.history, m.theme.SeparatorLine(m.width-4))
	m.history = append(m.history, m.theme.IndicatorAgent.Render("* ")+m.theme.AgentPrefix.Render("forge"))
	m.history = append(m.history, "")
	if preflight := m.runBuildPreflight(line); strings.TrimSpace(preflight) != "" {
		m.agentRuntime.PendingBuildPreflight = preflight
		m.lastBuildPreflight = preflight
		m.history = append(m.history, "    "+m.theme.Muted.Render("Build preflight complete."))
	}
	m.agentEvents = m.agentRuntime.Run(context.Background(), line)
	m.agentRunning = true
	return waitForAgentEvent(m.agentEvents)
}

func (m *model) runBuildPreflight(line string) string {
	if m == nil || m.agentRuntime == nil {
		return ""
	}
	cfg := m.agentRuntime.Config.Build.Subagents
	if !cfg.Enabled {
		return ""
	}
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 3
	}
	roles := cfg.Roles
	if len(roles) == 0 {
		roles = []string{"explorer", "reviewer", "debug"}
	}
	prompts := map[string]string{
		"explorer": "Before build execution, inspect the repository facts, likely files, and contracts needed for this request. Keep the answer concise and read-only. Request: " + line,
		"reviewer": "Before build execution, inspect the current plan/checklist and repository state for risks, validation needs, and likely failure modes. Keep the answer concise and read-only. Request: " + line,
		"debug":    "Before build execution, anticipate likely failure modes, edge cases, and regressions for this request. Point at the specific code/tests to watch. Read-only. Request: " + line,
	}
	tasks := make([]agent.SubagentRequest, 0, len(roles))
	for _, role := range roles {
		prompt, ok := prompts[role]
		if !ok {
			prompt = "Before build execution, contribute a concise read-only analysis for: " + line
		}
		tasks = append(tasks, agent.SubagentRequest{Agent: role, Prompt: prompt})
	}
	req := agent.SubagentBatchRequest{
		MaxConcurrency: concurrency,
		Tasks:          tasks,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result, err := m.agentRuntime.RunSubagents(ctx, req)
	if err != nil {
		return "Preflight failed: " + err.Error()
	}
	return formatPreflightResult(result)
}

func formatPreflightResult(result tools.Result) string {
	var b strings.Builder
	if result.Summary != "" {
		b.WriteString(result.Summary)
		b.WriteByte('\n')
	}
	for _, block := range result.Content {
		if block.Type == "json" {
			continue
		}
		text := strings.TrimSpace(block.Text)
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	return strings.TrimSpace(b.String())
}
