package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"forge/internal/agent"
	"forge/internal/permissions"
	"forge/internal/plans"
	"forge/internal/session"
	"forge/internal/skills"
	"forge/internal/tasks"
	"forge/internal/tools"
)

// /model shows, lists, or sets the chat model.
func (m *model) handleModelCommand(fields []string) string {
	t := m.theme
	if len(fields) >= 2 {
		switch fields[1] {
		case "list":
			return m.listModels()
		case "set":
			if len(fields) < 3 {
				return "Usage: /model set <model-name>"
			}
			m.options.Config.Models["chat"] = fields[2]
			m.agentRuntime.SetChatModel(fields[2])
			msg := t.Success.Render("Model set to: " + fields[2])
			if m.agentRuntime.ActiveParserName != "" {
				msg += t.Muted.Render(fmt.Sprintf(" (family=%s parser=%s)", m.agentRuntime.ActiveModelFamily, m.agentRuntime.ActiveParserName))
			}
			return msg
		}
	}
	rows := [][]string{
		{"default provider", m.options.Config.Providers.Default.Name},
		{"registered", strings.Join(m.options.Providers.Names(), ", ")},
	}
	for role, model := range m.options.Config.Models {
		rows = append(rows, []string{role, model})
	}
	return t.FormatTable([]string{"Role", "Model"}, rows)
}

func (m model) listModels() string {
	t := m.theme
	providerName := m.options.Config.Providers.Default.Name
	if providerName == "" {
		providerName = "lmstudio"
	}
	provider, ok := m.options.Providers.Get(providerName)
	if !ok {
		return "Provider " + providerName + " not registered."
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	models, err := provider.ListModels(ctx)
	if err != nil {
		return t.ErrorStyle.Render("Failed to list models: " + err.Error())
	}
	if len(models) == 0 {
		return "No models available from " + providerName + "."
	}
	rows := make([][]string, 0, len(models))
	currentModel := m.options.Config.Models["chat"]
	for _, model := range models {
		marker := " "
		if model.ID == currentModel {
			marker = "*"
		}
		rows = append(rows, []string{marker, model.ID})
	}
	return t.FormatTable([]string{" ", "Model ID"}, rows) +
		"\n\n" + t.Muted.Render("Use /model set <name> to switch.")
}

func (m model) describeModels() string {
	return m.handleModelCommand([]string{"/model"})
}

func currentModelName(m model) string {
	if m.agentRuntime.LastModelUsed != "" {
		return m.agentRuntime.LastModelUsed
	}
	if model := m.options.Config.Models["chat"]; model != "" {
		return model
	}
	return "default"
}

func keyState(apiKey, apiKeyEnv string) string {
	if apiKeyEnv != "" {
		return "env:" + apiKeyEnv
	}
	if apiKey != "" {
		return "set"
	}
	return "unset"
}

func commandProfileName(policy permissions.CommandPolicy) string {
	for _, name := range permissions.ProfileNames() {
		profile, _ := permissions.GetProfile(name)
		if profile.Policy.Describe() == policy.Describe() {
			return name
		}
	}
	return "custom"
}

func (m model) describeTools() string {
	t := m.theme
	rows := make([][]string, 0)
	for _, desc := range m.options.Tools.Describe() {
		rows = append(rows, []string{desc.Name, desc.Status, desc.Description})
	}
	return t.FormatTable([]string{"Tool", "Status", "Description"}, rows)
}

func (m model) describeStatus() string {
	t := m.theme
	sessionID := "none"
	if m.options.Session != nil {
		sessionID = m.options.Session.ID()
	}
	approval := "none"
	if m.pendingApproval != nil {
		approval = m.pendingApproval.ToolName + ": " + m.pendingApproval.Summary
	}
	agentState := "idle"
	if m.agentRunning {
		agentState = "running"
	}
	if m.streaming {
		agentState = "streaming"
	}
	rows := [][]string{
		{"cwd", m.options.CWD},
		{"mode", m.agentRuntime.Mode},
		{"provider", m.options.Config.Providers.Default.Name},
		{"model", currentModelName(m)},
		{"model_loading", fmt.Sprintf("%t/%s", m.options.Config.ModelLoading.Enabled, m.options.Config.ModelLoading.Strategy)},
		{"model_roles", formatModelRoles(m.options.Config.Models)},
		{"session", sessionID},
		{"command_profile", commandProfileName(m.agentRuntime.Commands)},
		{"context_engine", m.options.Config.Context.Engine},
		{"context_budget", fmt.Sprintf("%d", m.options.Config.Context.BudgetTokens)},
		{"context_profile", m.options.Config.Context.Yarn.Profile},
		{"model_context", fmt.Sprintf("%d", m.options.Config.Context.ModelContextTokens)},
		{"pending_approval", approval},
		{"agent", agentState},
	}
	return t.FormatTable([]string{"Setting", "Value"}, rows)
}

func formatModelRoles(models map[string]string) string {
	if len(models) == 0 {
		return ""
	}
	roles := []string{"explorer", "planner", "editor", "reviewer", "summarizer"}
	var parts []string
	for _, role := range roles {
		if model := strings.TrimSpace(models[role]); model != "" {
			parts = append(parts, role+"="+model)
		}
	}
	return strings.Join(parts, ", ")
}

func (m model) describeConfig() string {
	t := m.theme
	cfg := m.options.Config
	rows := [][]string{
		{"default_agent", cfg.DefaultAgent},
		{"approval_profile", cfg.ApprovalProfile},
		{"providers.default.name", cfg.Providers.Default.Name},
		{"providers.openai_compatible.base_url", cfg.Providers.OpenAICompatible.BaseURL},
		{"providers.openai_compatible.api_key", keyState(cfg.Providers.OpenAICompatible.APIKey, cfg.Providers.OpenAICompatible.APIKeyEnv)},
		{"providers.openai_compatible.default_model", cfg.Providers.OpenAICompatible.DefaultModel},
		{"providers.openai_compatible.supports_tools", fmt.Sprintf("%t", cfg.Providers.OpenAICompatible.SupportsTools)},
		{"providers.lmstudio.base_url", cfg.Providers.LMStudio.BaseURL},
		{"providers.lmstudio.api_key", keyState(cfg.Providers.LMStudio.APIKey, cfg.Providers.LMStudio.APIKeyEnv)},
		{"providers.lmstudio.default_model", cfg.Providers.LMStudio.DefaultModel},
		{"providers.lmstudio.supports_tools", fmt.Sprintf("%t", cfg.Providers.LMStudio.SupportsTools)},
		{"model_loading.enabled", fmt.Sprintf("%t", cfg.ModelLoading.Enabled)},
		{"model_loading.strategy", cfg.ModelLoading.Strategy},
		{"context.engine", cfg.Context.Engine},
		{"context.budget_tokens", fmt.Sprintf("%d", cfg.Context.BudgetTokens)},
		{"context.task.budget_tokens", fmt.Sprintf("%d", cfg.Context.Task.BudgetTokens)},
		{"context.task.max_nodes", fmt.Sprintf("%d", cfg.Context.Task.MaxNodes)},
		{"context.task.max_file_bytes", fmt.Sprintf("%d", cfg.Context.Task.MaxFileBytes)},
		{"context.task.history_events", fmt.Sprintf("%d", cfg.Context.Task.HistoryEvents)},
		{"context.auto_compact", fmt.Sprintf("%t", cfg.Context.AutoCompact)},
		{"context.model_context_tokens", fmt.Sprintf("%d", cfg.Context.ModelContextTokens)},
		{"context.reserve_output_tokens", fmt.Sprintf("%d", cfg.Context.ReserveOutputTokens)},
		{"context.yarn.profile", cfg.Context.Yarn.Profile},
		{"context.yarn.max_nodes", fmt.Sprintf("%d", cfg.Context.Yarn.MaxNodes)},
		{"context.yarn.max_file_bytes", fmt.Sprintf("%d", cfg.Context.Yarn.MaxFileBytes)},
		{"context.yarn.history_events", fmt.Sprintf("%d", cfg.Context.Yarn.HistoryEvents)},
		{"context.yarn.pins", cfg.Context.Yarn.Pins},
		{"context.yarn.mentions", cfg.Context.Yarn.Mentions},
		{"context.yarn.compact_events", fmt.Sprintf("%d", cfg.Context.Yarn.CompactEvents)},
		{"context.yarn.compact_transcript_chars", fmt.Sprintf("%d", cfg.Context.Yarn.CompactTranscriptChars)},
		{"skills.cli", cfg.Skills.CLI},
		{"skills.repositories", strings.Join(cfg.Skills.Repositories, ", ")},
		{"skills.agent", cfg.Skills.Agent},
		{"skills.install_scope", cfg.Skills.InstallScope},
		{"skills.copy", fmt.Sprintf("%t", cfg.Skills.Copy)},
		{"plugins.enabled", fmt.Sprintf("%t", cfg.Plugins.Enabled)},
		{"plugins.claude_compatible", fmt.Sprintf("%t", cfg.Plugins.ClaudeCompatible)},
	}
	for role, model := range cfg.Models {
		rows = append(rows, []string{"models." + role, model})
	}
	return t.FormatTable([]string{"Config", "Value"}, rows)
}

func (m *model) enterReviewMode() string {
	return m.runSubagentCommand("reviewer", "Review the current git diff and report findings.")
}

func (m model) describeAgents() string {
	t := m.theme
	rows := make([][]string, 0)
	for _, w := range m.agentRuntime.Subagents.List() {
		rows = append(rows, []string{w.Name, w.Description, w.ModelRole, w.ContextMode})
	}
	return t.FormatTable([]string{"Agent", "Description", "Model", "Context"}, rows)
}

func (m *model) runSubagentCommand(agentName, prompt string) string {
	t := m.theme
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := m.agentRuntime.RunSubagent(ctx, agent.SubagentRequest{
		Agent:  agentName,
		Prompt: prompt,
	})
	if err != nil {
		return t.ErrorStyle.Render("Subagent failed: " + err.Error())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s", t.AgentPrefix.Render(result.Title), result.Summary)
	for _, block := range result.Content {
		if block.Text != "" {
			fmt.Fprintf(&b, "\n%s", block.Text)
		}
	}
	if strings.TrimSpace(agentName) == "explorer" {
		m.pendingExplorerHandoff = subagentHandoffText(result)
		m.activeForm = formConfirmExplorerPlan
		m.confirmExplorerPlan = newConfirmForm("Pass explorer findings to Plan mode?", m.theme)
		fmt.Fprintf(&b, "\n\n%s", t.Muted.Render("Explorer finished. Confirm to send these findings to Plan mode."))
	}
	return b.String()
}

func subagentHandoffText(result tools.Result) string {
	var b strings.Builder
	if result.Summary != "" {
		fmt.Fprintf(&b, "Summary: %s\n", result.Summary)
	}
	for _, block := range result.Content {
		if strings.TrimSpace(block.Text) != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(strings.TrimSpace(block.Text))
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

func (m model) describePlan() string {
	if m.agentRuntime.Tasks == nil {
		return "Task store unavailable."
	}
	list, err := m.agentRuntime.Tasks.List()
	if err != nil {
		return "Task list failed: " + err.Error()
	}
	return tasks.Format(list)
}

// planInterviewPrompt builds the turn prompt that drives plan-mode's
// interview-first behavior. cleared=true means the user just ran /plan new so
// any prior plan/todos were wiped; cleared=false means we entered plan mode
// via /mode plan and any existing plan should be treated as a starting point.
func planInterviewPrompt(goal string, cleared bool) string {
	base := "Before calling plan_write or todo_write, interview the user with ask_user to clarify scope, constraints, and success criteria. " +
		"Ask the most important open questions first (3-6 max), wait for the answers, and only then draft the plan. " +
		"Each ask_user call MUST include an `options` array with 3 short, mutually-exclusive suggested answers " +
		"(e.g. `{\"question\":\"...\",\"options\":[\"Yes\",\"No\",\"Only for X\"]}`). The TUI adds a 'Write my own' row automatically — do NOT include it yourself. " +
		"After the interview, call plan_write with the full plan document and todo_write with the executable checklist in the same turn."
	if strings.TrimSpace(goal) != "" {
		suffix := "Do not assume."
		if cleared {
			suffix = "Do not assume — the prior plan and todos have been cleared."
		}
		return "NEW PLAN GOAL: " + goal + "\n\n" + base + " " + suffix
	}
	return "PLAN MODE ENTERED. " + base + " If a prior plan exists, first confirm whether the user wants to refine it or start fresh before interviewing."
}

func (m *model) handlePlanCommand(fields []string) string {
	if len(fields) == 1 {
		m.showPlan = !m.showPlan
		m.recalcLayout()
		if m.showPlan {
			return m.theme.Success.Render("Plan panel: ON")
		}
		return m.theme.Muted.Render("Plan panel: OFF")
	}
	switch fields[1] {
	case "panel", "toggle":
		m.showPlan = !m.showPlan
		m.recalcLayout()
		if m.showPlan {
			return m.theme.Success.Render("Plan panel: ON")
		}
		return m.theme.Muted.Render("Plan panel: OFF")
	case "full", "show":
		return m.describeFullPlan()
	case "todos", "tasks":
		return m.describePlan()
	case "new":
		if m.agentRunning {
			return m.theme.Warning.Render("Agent is still running.")
		}
		if len(fields) < 3 {
			return m.theme.Muted.Render("Usage: /plan new <goal>")
		}
		goal := strings.Join(fields[2:], " ")
		if m.agentRuntime.Plans != nil {
			if err := m.agentRuntime.Plans.Clear(); err != nil {
				return m.theme.ErrorStyle.Render("Clear plan failed: " + err.Error())
			}
		}
		if m.agentRuntime.Tasks != nil {
			if _, err := m.agentRuntime.Tasks.ReplacePlan(nil); err != nil {
				return m.theme.ErrorStyle.Render("Clear todos failed: " + err.Error())
			}
		}
		_ = m.agentRuntime.SetMode("plan")
		m.showPlan = true
		m.recalcLayout()
		m.agentEvents = m.agentRuntime.Run(context.Background(), planInterviewPrompt(goal, true))
		m.agentRunning = true
		m.pendingCommand = waitForAgentEvent(m.agentEvents)
		return "Starting plan interview..."
	case "refine":
		if m.agentRunning {
			return m.theme.Warning.Render("Agent is still running.")
		}
		_ = m.agentRuntime.SetMode("plan")
		m.showPlan = true
		m.recalcLayout()
		prompt := "Refine the existing plan document for the user's current goal, then derive the executable checklist. Read existing plan/checklist first if present."
		if len(fields) > 2 {
			prompt = strings.Join(fields[2:], " ")
		}
		m.agentEvents = m.agentRuntime.Run(context.Background(), prompt)
		m.agentRunning = true
		m.pendingCommand = waitForAgentEvent(m.agentEvents)
		return "Refining plan..."
	default:
		return "Usage: /plan [panel|full|todos|new <goal>|refine <goal>]"
	}
}

func (m model) describeFullPlan() string {
	if m.agentRuntime.Plans == nil {
		return "Plan store unavailable."
	}
	doc, ok, err := m.agentRuntime.Plans.Current()
	if err != nil {
		return "Plan read failed: " + err.Error()
	}
	if !ok {
		return "No plan yet."
	}
	return plans.Format(doc)
}

func (m model) describeSession() string {
	if m.options.Session == nil {
		return "Session store unavailable."
	}
	events, err := m.options.Session.Tail(8)
	if err != nil {
		return "Session tail failed: " + err.Error()
	}
	return "session: " + m.options.Session.ID() +
		"\npath: " + m.options.Session.Dir() +
		"\nlive_log: " + m.options.Session.LiveLogPath() +
		"\n\n" + session.Summarize(events) + "\n\n" + session.FormatTail(events)
}

func (m model) describeLog() string {
	if m.options.Session == nil {
		return "Session store unavailable."
	}
	path := m.options.Session.LiveLogPath()
	return "live log: " + path + "\n\n" +
		m.theme.Muted.Render("PowerShell: Get-Content -LiteralPath '"+path+"' -Wait")
}

func (m model) describeSessions() string {
	t := m.theme
	sessions, err := session.List(m.options.CWD, 10)
	if err != nil {
		return "Session list failed: " + err.Error()
	}
	if len(sessions) == 0 {
		return "No previous sessions found."
	}
	rows := make([][]string, 0, len(sessions))
	for _, item := range sessions {
		rows = append(rows, []string{item.ID, fmt.Sprintf("%d", item.EventCount), item.UpdatedAt.Format("2006-01-02 15:04")})
	}
	return t.FormatTable([]string{"Session", "Events", "Updated"}, rows)
}

func (m *model) resumeSession(id string) string {
	var store *session.Store
	var err error
	if id == "latest" {
		store, err = session.OpenLatest(m.options.CWD)
	} else {
		store, err = session.Open(m.options.CWD, id)
	}
	if err != nil {
		return "Resume failed: " + err.Error()
	}
	m.options.Session = store
	m.agentRuntime.Builder.History = store
	return m.theme.Success.Render("Resumed session: " + store.ID())
}

func (m model) handleContextCommand(fields []string) string {
	if len(fields) > 1 {
		switch fields[1] {
		case "pin":
			if len(fields) < 3 {
				return "Usage: /context pin @path/to/file"
			}
			return m.pinContext(fields[2])
		case "drop":
			if len(fields) < 3 {
				return "Usage: /context drop @path/to/file"
			}
			return m.dropContext(fields[2])
		case "yarn":
			return m.describeYarn()
		case "compact":
			return m.compactSession()
		}
	}
	return m.describeContext()
}

func (m model) describeContext() string {
	t := m.theme
	rows := [][]string{
		{"engine", m.options.Config.Context.Engine},
		{"budget_tokens", fmt.Sprintf("%d", m.options.Config.Context.BudgetTokens)},
		{"model_context_tokens", fmt.Sprintf("%d", m.options.Config.Context.ModelContextTokens)},
		{"reserve_output_tokens", fmt.Sprintf("%d", m.options.Config.Context.ReserveOutputTokens)},
		{"auto_compact", fmt.Sprintf("%t", m.options.Config.Context.AutoCompact)},
		{"yarn.profile", m.options.Config.Context.Yarn.Profile},
		{"yarn.max_nodes", fmt.Sprintf("%d", m.options.Config.Context.Yarn.MaxNodes)},
		{"yarn.max_file_bytes", fmt.Sprintf("%d", m.options.Config.Context.Yarn.MaxFileBytes)},
		{"yarn.history_events", fmt.Sprintf("%d", m.options.Config.Context.Yarn.HistoryEvents)},
		{"yarn.pins", m.options.Config.Context.Yarn.Pins},
		{"yarn.mentions", m.options.Config.Context.Yarn.Mentions},
	}
	if m.agentRuntime.Builder.Tray != nil {
		pins, err := m.agentRuntime.Builder.Tray.Pins()
		if err == nil {
			rows = append(rows, []string{"tray", m.agentRuntime.Builder.Tray.Path()})
			rows = append(rows, []string{"pinned", fmt.Sprintf("%d", len(pins))})
			for _, pin := range pins {
				rows = append(rows, []string{"  pin", pin.Path})
			}
		}
	}
	if m.options.Skills != nil {
		local := m.options.Skills.ScanLocal()
		rows = append(rows, []string{"skills.loaded", fmt.Sprintf("%d", len(local))})
		for _, skill := range local {
			label := skill.Name + " (" + skill.Source + ")"
			if skill.InstallPath != "" {
				label += " " + skill.InstallPath
			}
			rows = append(rows, []string{"  skill", label})
			if _, err := m.options.Skills.LoadSkill(skill.Name); err != nil {
				rows = append(rows, []string{"  skill_warning", skill.Name + ": " + err.Error()})
			}
		}
	}
	return t.FormatTable([]string{"Setting", "Value"}, rows)
}

func (m model) pinContext(path string) string {
	if m.agentRuntime.Builder.Tray == nil {
		return "Context tray unavailable."
	}
	pin, err := m.agentRuntime.Builder.Tray.Pin(path)
	if err != nil {
		return "Pin failed: " + err.Error()
	}
	return m.theme.Success.Render("Pinned: " + pin.Path)
}

func (m model) dropContext(path string) string {
	if m.agentRuntime.Builder.Tray == nil {
		return "Context tray unavailable."
	}
	dropped, err := m.agentRuntime.Builder.Tray.Drop(path)
	if err != nil {
		return "Drop failed: " + err.Error()
	}
	if !dropped {
		return "Pin not found."
	}
	return m.theme.Success.Render("Dropped: " + strings.TrimPrefix(path, "@"))
}

func (m model) describePermissions() string {
	t := m.theme
	rows := make([][]string, 0, 4)
	for _, name := range permissions.ProfileNames() {
		profile, _ := permissions.GetProfile(name)
		marker := " "
		if profile.Policy.Describe() == m.agentRuntime.Commands.Describe() {
			marker = "*"
		}
		rows = append(rows, []string{marker, name, profile.Description})
	}
	return t.FormatTable([]string{" ", "Profile", "Description"}, rows) +
		"\n\n" + m.agentRuntime.Commands.Describe() +
		"\n\n" + t.Muted.Render("These profiles affect run_command only. File edits and patches still require approval in build mode.") +
		"\n" + t.Muted.Render("Use /permissions set <profile> to change.")
}

func (m *model) setPermissionProfile(name string) string {
	profile, ok := permissions.GetProfile(name)
	if !ok {
		return m.theme.ErrorStyle.Render("Unknown profile: " + name + ". Available: safe, normal, fast, yolo")
	}
	m.agentRuntime.Commands = profile.Policy
	return m.theme.Success.Render("Permission profile set to: " + name + " - " + profile.Description)
}

func (m model) runTestCommand(command string) string {
	decision, reason := m.agentRuntime.Commands.Decide(command)
	if decision == permissions.Deny {
		return reason
	}
	if decision == permissions.Ask {
		return reason + ". Use the chat agent for approval-gated commands."
	}
	tool, ok := m.options.Tools.Get("run_command")
	if !ok {
		return "run_command tool is not registered."
	}
	input, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		return err.Error()
	}
	result, err := tool.Run(tools.Context{Context: context.Background(), CWD: m.options.CWD, Agent: m.options.Config.DefaultAgent}, input)
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)\n", result.Summary, reason)
	for _, block := range result.Content {
		if block.Text != "" {
			b.WriteString(block.Text)
		}
	}
	if err != nil {
		fmt.Fprintf(&b, "\nerror: %s", err)
	}
	return strings.TrimSpace(b.String())
}

func (m model) describeSkills() string {
	if m.options.Skills == nil {
		return "Skills manager not available."
	}
	source := "cache"
	found, cached := m.options.Skills.ListDirectoryCached()
	if !cached {
		source = "offline built-in fallback"
		found = fallbackSkills(m.options.Skills)
	}
	if len(found) == 0 {
		return "No skills found. Use /skills refresh to fetch from Skills CLI."
	}
	t := m.theme
	rows := make([][]string, 0, len(found))
	for _, skill := range found {
		rows = append(rows, []string{skill.Name, skill.Repo, skill.Source, installedLabel(skill), skill.Description})
	}
	return t.Muted.Render("source: "+source) + "\n\n" + t.FormatTable([]string{"Skill", "Repo", "Source", "Installed", "Description"}, rows)
}

func (m model) describeSkillsCache(repos []string) string {
	if m.options.Skills == nil {
		return "Skills manager not available."
	}
	var infos []skills.CacheInfo
	if len(repos) == 0 {
		infos = append(infos, m.options.Skills.DirectoryCacheInfo())
	} else {
		infos = m.options.Skills.CacheInfo(repos)
	}
	if len(infos) == 0 {
		return "No skills repositories configured."
	}
	rows := make([][]string, 0, len(infos))
	for _, info := range infos {
		state := "missing"
		updated := "-"
		count := "-"
		if info.Exists {
			state = "cached"
			updated = info.UpdatedAt.Format("2006-01-02 15:04")
			count = fmt.Sprintf("%d", info.Count)
		}
		if info.Error != "" {
			state = "error"
			updated = info.Error
		}
		rows = append(rows, []string{info.Repo, state, updated, count, info.Path})
	}
	return m.theme.FormatTable([]string{"Repo", "State", "Updated", "Skills", "Path"}, rows) +
		"\n\n" + m.theme.Muted.Render("Use /skills refresh [repo] to update cache.")
}

func installedLabel(skill skills.Skill) string {
	if skill.Installed {
		return "yes"
	}
	return "no"
}

func (m model) describePlugins() string {
	found, err := m.options.Plugins.Discover()
	if err != nil {
		return "plugins unavailable: " + err.Error()
	}
	if len(found) == 0 {
		return "No plugins found."
	}
	t := m.theme
	rows := make([][]string, 0, len(found))
	for _, p := range found {
		rows = append(rows, []string{p.Name, p.Source, p.Path})
	}
	return t.FormatTable([]string{"Plugin", "Source", "Path"}, rows)
}

func (m model) describeMode() string {
	t := m.theme
	rows := make([][]string, 0, 4)
	for _, name := range agent.ModeNames() {
		mode, _ := agent.GetMode(name)
		marker := " "
		if name == m.agentRuntime.Mode {
			marker = "*"
		}
		rows = append(rows, []string{marker, mode.Name, mode.Description})
	}
	return t.FormatTable([]string{" ", "Mode", "Description"}, rows)
}

func (m *model) setMode(name, goal string) string {
	if err := m.agentRuntime.SetMode(name); err != nil {
		return err.Error()
	}
	// Entering plan mode with an explicit goal kicks off the interview
	// immediately — same behavior as /plan new. Without a goal we stay silent
	// and let the next user message drive the turn; the plan-mode handoff
	// carries the "interview first" steering so the model still asks before
	// writing a plan.
	if name == "plan" {
		m.showPlan = true
		m.recalcLayout()
		if strings.TrimSpace(goal) == "" {
			return m.theme.Muted.Render("Plan mode entered. Send a message describing what you want planned; forge will interview you before drafting.")
		}
		if m.agentRunning {
			return m.theme.Warning.Render("Plan mode entered — agent still running, interview will not auto-start.")
		}
		m.agentEvents = m.agentRuntime.Run(context.Background(), planInterviewPrompt(goal, false))
		m.agentRunning = true
		m.pendingCommand = waitForAgentEvent(m.agentEvents)
		return m.theme.Success.Render("Entering plan mode — starting interview...")
	}
	// Mode shown in status bar; no inline message.
	return ""
}

func (m model) describeDiff() string {
	t := m.theme
	if m.pendingApproval != nil {
		return t.FormatDiffColored(m.pendingApproval.Diff)
	}
	cmd := exec.Command("git", "-C", m.options.CWD, "diff")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "git diff unavailable: " + err.Error() + "\n" + string(out)
	}
	if len(out) == 0 {
		return "No workspace diff."
	}
	return t.FormatDiffColored(string(out))
}

func (m *model) approvePending() string {
	if m.pendingApproval == nil {
		return "No pending approval."
	}
	m.pendingApproval.Response <- agent.ApprovalResponse{Approved: true}
	summary := m.theme.Success.Render("Approved: " + m.pendingApproval.Summary)
	m.pendingApproval = nil
	return summary
}

func (m *model) rejectPending() string {
	if m.pendingApproval == nil {
		return "No pending approval."
	}
	m.pendingApproval.Response <- agent.ApprovalResponse{Approved: false}
	summary := m.theme.Warning.Render("Rejected: " + m.pendingApproval.Summary)
	m.pendingApproval = nil
	return summary
}

func (m *model) undoLast() string {
	summary, err := m.agentRuntime.UndoLast()
	if err != nil {
		return "Undo failed: " + err.Error()
	}
	return m.theme.Success.Render("Undid: " + summary)
}
