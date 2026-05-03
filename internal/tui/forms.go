package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"forge/internal/agent"
	"forge/internal/config"
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
			autoMode := m.approvalForm.autoMode
			m.activeForm = formNone
			// "Auto" approves the current request AND flips the approval
			// profile to "auto" so future mutating tools no longer prompt.
			// Persist globally (rather than per-workspace) because the
			// user's intent is "stop asking me, anywhere".
			if autoMode {
				m.options.Config.ApprovalProfile = "auto"
				m.syncRuntimeConfig()
				if err := persistGlobalApprovalProfile("auto"); err != nil {
					m.history = append(m.history, m.theme.Warning.Render("Auto-approve set for this session, but global save failed: "+err.Error()))
				} else {
					m.history = append(m.history, m.theme.Success.Render("Auto-approve enabled. approval_profile = \"auto\" persisted to ~/.forge/global.toml."))
				}
			}
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
					m.history = append(m.history, "    "+m.theme.Muted.Render("-> (skipped)"))
				} else {
					for _, line := range strings.Split(answer, "\n") {
						m.history = append(m.history, "    "+m.theme.Muted.Render("-> ")+line)
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
				m.syncRuntimeConfig()
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
				m.syncRuntimeConfig()
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
				m.syncRuntimeConfig()
				activeRole := m.activeModelRole()
				strategy := strings.ToLower(strings.TrimSpace(m.options.Config.ModelLoading.Strategy))
				for _, selection := range m.modelMultiForm.selections {
					m.agentRuntime.SetRoleModel(selection.role, selection.modelID)
					if strategy == "parallel" || selection.role == activeRole {
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
					cmd := m.runPlanExecution(m.pendingExecuteLine)
					m.refresh()
					return m, cmd, true
				}
				m.pendingExecuteLine = ""
				m.history = append(m.history, m.theme.Muted.Render("Plan left pending. Type an execute request when ready."))
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
						_ = m.agentRuntime.Tasks.Clear()
					}
					m.history = append(m.history, m.theme.Success.Render("Prior plan and todos cleared."))
					cleared = true
				}
				prompt := planInterviewPrompt(line, cleared)
				if !cleared {
					prompt = planRefinementPrompt(line)
				}
				m.agentEvents = m.agentRuntime.Run(context.Background(), prompt)
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

func (m *model) runPlanExecution(line string) tea.Cmd {
	line = strings.TrimSpace(line)
	if line == "" {
		line = "Execute the approved plan."
	}
	if m.agentRuntime != nil && m.agentRuntime.Mode == "plan" {
		_ = m.agentRuntime.SetMode("build")
		m.showPlan = true
	}
	m.pendingExecuteLine = ""
	m.history = append(m.history, m.theme.SeparatorLine(m.width-4))
	m.history = append(m.history, m.theme.IndicatorAgent.Render("* ")+m.theme.AgentPrefix.Render("forge"))
	m.history = append(m.history, "")
	if strings.EqualFold(strings.TrimSpace(line), "Execute the approved plan.") {
		line = "Execute the approved plan. Use the plan/checklist digest already in context first; only call plan_get or task_list if that digest is insufficient."
	}
	m.agentEvents = m.agentRuntime.Run(context.Background(), line)
	m.agentRunning = true
	return waitForAgentEvent(m.agentEvents)
}

// runModePreflight dispatches the configured preflight subagents for the
// current mode. Returns "" when preflight is disabled for the mode, when the
// message is trivial chit-chat, or when no roles are configured. The caller
// injects the result into the runtime's per-mode pending-handoff field.
func (m *model) runModePreflight(mode, line string) string {
	if m == nil || m.agentRuntime == nil {
		return ""
	}
	cfg := modePreflightConfig(m.agentRuntime.Config, mode)
	if !cfg.Enabled {
		return ""
	}
	if looksLikeTrivia(line) {
		return ""
	}
	if cached, ok := m.agentRuntime.PreflightCacheGet(mode, line); ok {
		return cached
	}
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = m.agentRuntime.Config.ModelLoading.ParallelSlots
		if concurrency <= 0 {
			concurrency = 2
		}
		if concurrency > 2 {
			concurrency = 2
		}
	}
	if slots := m.agentRuntime.Config.ModelLoading.ParallelSlots; slots > 0 && concurrency > slots {
		concurrency = slots
	}
	roles := cfg.Roles
	if len(roles) == 0 {
		return ""
	}
	prompts := preflightPrompts(mode, line)
	sharedContext, _ := json.Marshal(map[string]string{
		"text": m.agentRuntime.SharedTaskContext(line),
	})
	tasks := make([]agent.SubagentRequest, 0, len(roles))
	for _, role := range roles {
		prompt, ok := prompts[role]
		if !ok {
			prompt = "Contribute a concise read-only analysis for: " + line
		}
		tasks = append(tasks, agent.SubagentRequest{Agent: role, Prompt: prompt, Context: sharedContext})
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
	formatted := formatPreflightResult(result)
	m.agentRuntime.PreflightCacheSet(mode, line, formatted)
	return formatted
}

func modePreflightConfig(cfg config.Config, mode string) config.BuildSubagentsConfig {
	switch mode {
	case "build":
		return cfg.Build.Subagents
	case "explore":
		return cfg.Explore.Subagents
	case "plan":
		return cfg.Plan.Subagents
	default:
		return config.BuildSubagentsConfig{}
	}
}

// preflightPrompts returns compact, structured prompts per role. Structured
// output keeps each subagent response in the ~400–600 token range instead of
// the freeform ~1–2k they used to emit, which directly shrinks the tier-C
// handoff block in the main turn.
func preflightPrompts(mode, line string) map[string]string {
	switch mode {
	case "build":
		return map[string]string{
			"explorer": "List the 3–8 files most likely to be read or touched for this request. Format each line as `path — one-line why`. No prose. Request: " + line,
			"reviewer": "Return risks for the request as rows `[risk] | [file:line] | [mitigation]`. Max 5 rows, no prose. Request: " + line,
			"debug":    "Return likely failure modes as rows `[mode] | [trigger] | [detect cmd]`. Max 5 rows, no prose. Request: " + line,
		}
	case "explore", "plan":
		return map[string]string{
			"explorer": "List the 3–8 files most likely relevant to this question. Format each line as `path — one-line why`. No prose. Question: " + line,
			"reviewer": "Return risks if the user acts on this analysis as rows `[risk] | [file:line] | [mitigation]`. Max 5 rows. Question: " + line,
			"debug":    "Return likely failure modes to watch as rows `[mode] | [trigger] | [detect cmd]`. Max 5 rows. Question: " + line,
		}
	default:
		return map[string]string{}
	}
}

// looksLikeTrivia bails out of preflight for short messages or common
// conversational replies. Saves 2–4s of subagent latency on "thanks"/"ok"
// follow-ups without sacrificing the fan-out for real questions.
func looksLikeTrivia(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || len(trimmed) < 20 {
		return true
	}
	lowered := strings.ToLower(trimmed)
	for _, prefix := range []string{"thanks", "thank you", "ok", "okay", "si", "sí", "no", "ya", "listo", "dale", "gracias", "perfect", "nice", "cool", "great"} {
		if lowered == prefix || strings.HasPrefix(lowered, prefix+" ") || strings.HasPrefix(lowered, prefix+",") || strings.HasPrefix(lowered, prefix+".") || strings.HasPrefix(lowered, prefix+"!") {
			return true
		}
	}
	return false
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
	out := strings.TrimSpace(b.String())
	if len(out) > 4000 {
		out = out[:4000] + "\n[preflight truncated]"
	}
	return out
}
