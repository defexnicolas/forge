package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"forge/internal/agent"
	"forge/internal/config"
	"forge/internal/globalconfig"
	"forge/internal/llm"
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
		case "reload":
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			result, err := m.agentRuntime.ReloadCurrentModel(ctx)
			// Three outcomes: full reload, metadata-only refresh (backend
			// can't load programmatically), or hard failure.
			if err != nil && !result.Refreshed {
				return t.ErrorStyle.Render("Model reload failed: " + err.Error())
			}
			if err != nil {
				// Backend doesn't support reload but we still re-classified
				// the backend and refreshed DetectedContext / LastProviderUsed.
				// Render as a notice, not an error, so the user sees the
				// actionable instruction without a red banner suggesting
				// nothing happened.
				return t.Warning.Render(fmt.Sprintf("Metadata refreshed for %s (%s)", result.ModelID, result.Backend)) +
					"\n" + t.Muted.Render(err.Error())
			}
			return t.Success.Render(fmt.Sprintf("Model reloaded: %s (%s)", result.ModelID, result.Backend)) +
				t.Muted.Render(fmt.Sprintf(" (parallel_slots=%d)", m.agentRuntime.Config.ModelLoading.ParallelSlots))
		case "set":
			if len(fields) < 3 {
				return "Usage: /model set <model-name>"
			}
			if m.options.Config.Models == nil {
				m.options.Config.Models = map[string]string{}
			}
			m.options.Config.Models["chat"] = fields[2]
			if m.options.Config.Context.Detected != nil && m.options.Config.Context.Detected.ModelID != "" && m.options.Config.Context.Detected.ModelID != fields[2] {
				m.options.Config.Context.Detected = nil
			}
			m.persistConfig()
			m.syncRuntimeConfig()
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

// handleRefreshConfigCommand re-reads the workspace + global config from
// disk and rebuilds the provider registry with fresh OpenAICompatible
// instances. Recovers from the "I edited global.toml externally and the
// in-memory state is stale" case without needing a Forge restart. Mirrors
// what app.NewWorkspace does at startup, just for the config + providers
// (tools, MCPs, claw, git state are left alone — they don't depend on
// provider URLs and rebuilding them mid-session would be disruptive).
func (m *model) handleRefreshConfigCommand() string {
	t := m.theme
	cfg, err := config.LoadWithGlobal(m.options.CWD)
	if err != nil {
		// LoadWithGlobal still hands back the workspace-only config when the
		// global file is malformed; surface the error but apply what we got
		// so the user isn't locked out by a typo.
		m.history = append(m.history, t.Warning.Render("global config: "+err.Error()))
	}
	config.InheritChatModelDefaults(&cfg)
	providers := llm.NewRegistry()
	providers.Register(llm.NewOpenAICompatible("openai_compatible", cfg.Providers.OpenAICompatible))
	providers.Register(llm.NewOpenAICompatible("lmstudio", cfg.Providers.LMStudio))
	m.options.Config = cfg
	m.options.Providers = providers
	m.syncRuntimeConfig()
	m.agentRuntime.Providers = providers
	// Push fresh role models into the runtime so SetRoleModel state matches
	// the reloaded config — without this, ResolveProvider sees the new URL
	// but Models[role] could still hold stale values from the prior session.
	for role, modelID := range cfg.Models {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		m.agentRuntime.SetRoleModel(role, modelID)
	}
	m.refreshProviderState()
	providerName := strings.TrimSpace(cfg.Providers.Default.Name)
	if providerName == "" {
		providerName = "(none)"
	}
	url := strings.TrimSpace(cfg.Providers.LMStudio.BaseURL)
	if cfg.Providers.Default.Name == "openai_compatible" {
		url = strings.TrimSpace(cfg.Providers.OpenAICompatible.BaseURL)
	}
	return t.Success.Render("Config reloaded.") +
		"\n" + t.Muted.Render(fmt.Sprintf("provider=%s url=%s", providerName, url)) +
		"\n" + t.Muted.Render("Backend will re-classify on the next probe (~5s).")
}

// handleReadsCommand exposes the read-only budget guard to the user so they
// can inspect the current state and bump the threshold for this session
// without having to edit .forge/config.toml + /refresh-config. The override
// lives on the Runtime instance and is reset when the user runs /reads
// reset (or restarts Forge).
//
// Subcommands:
//   /reads             — show consumed/threshold for the current mode
//   /reads extend [N]  — bump threshold by +N (default +10) for this session
//   /reads reset       — clear the session override, fall back to config
//   /reads off         — disable the guard entirely for this session
func (m *model) handleReadsCommand(fields []string) string {
	t := m.theme
	if m.agentRuntime == nil {
		return t.ErrorStyle.Render("Runtime not initialized.")
	}
	mode := m.agentRuntime.Mode
	if len(fields) >= 2 {
		switch fields[1] {
		case "extend":
			delta := 10
			if len(fields) >= 3 {
				if n, err := strconv.Atoi(fields[2]); err == nil && n > 0 {
					delta = n
				} else {
					return t.ErrorStyle.Render("Usage: /reads extend [N>0]")
				}
			}
			newBudget := m.agentRuntime.ExtendReadBudget(delta)
			return t.Success.Render(fmt.Sprintf("Read budget extended → %d (mode=%s).", newBudget, mode)) +
				"\n" + t.Muted.Render("This session only; restart or /reads reset to revert.")
		case "reset":
			m.agentRuntime.SetReadBudgetOverride(0)
			return t.Success.Render("Read budget override cleared.") +
				"\n" + t.Muted.Render(fmt.Sprintf("Falls back to config: max_consecutive_read_only=%d, max_builder_read_loops=%d.", m.options.Config.Runtime.MaxConsecutiveReadOnly, m.options.Config.Runtime.MaxBuilderReadLoops))
		case "off":
			m.agentRuntime.SetReadBudgetOverride(-1)
			return t.Warning.Render("Read budget guard DISABLED for this session.") +
				"\n" + t.Muted.Render("max_steps still applies. Use /reads reset to restore the guard.")
		default:
			return t.ErrorStyle.Render("Usage: /reads [extend [N]|reset|off]")
		}
	}
	consumed, threshold := m.agentRuntime.LastReadBudgetSnapshot()
	body := fmt.Sprintf("Read budget — mode=%s consumed=%d", mode, consumed)
	switch {
	case mode == "explore":
		body += " threshold=disabled (explore mode is read-only by design)"
	case threshold == 0:
		body += " threshold=disabled (override=off)"
	default:
		body += fmt.Sprintf(" threshold=%d", threshold)
	}
	return t.Success.Render(body) +
		"\n" + t.Muted.Render("Subcommands: extend [N]  reset  off")
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
	gitState := m.agentRuntime.GitSessionState()
	gitStatus := "unknown"
	switch {
	case !gitState.RepoInitialized:
		gitStatus = "not initialized"
	case gitState.SnapshotRequiredBeforeMutate:
		gitStatus = "dirty worktree"
	case gitState.AutoInitialized || gitState.BaselineCreatedThisSession:
		gitStatus = "baseline created this session"
	default:
		gitStatus = "initialized, clean"
	}
	rows := [][]string{
		{"cwd", m.options.CWD},
		{"mode", m.agentRuntime.Mode},
		{"provider", m.options.Config.Providers.Default.Name},
		{"model", currentModelName(m)},
		{"claw", clawAvailability(m.options.Claw)},
		{"model_loading", fmt.Sprintf("%t/%s", m.options.Config.ModelLoading.Enabled, m.options.Config.ModelLoading.Strategy)},
		{"parallel_slots", fmt.Sprintf("%d", m.options.Config.ModelLoading.ParallelSlots)},
		{"build_subagents", fmt.Sprintf("%t/concurrency=%d", m.options.Config.Build.Subagents.Enabled, m.options.Config.Build.Subagents.Concurrency)},
		{"model_roles", formatModelRoles(m.options.Config.Models)},
		{"session", sessionID},
		{"git", gitStatus},
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
		{"model_loading.parallel_slots", fmt.Sprintf("%d", cfg.ModelLoading.ParallelSlots)},
		{"build.subagents.enabled", fmt.Sprintf("%t", cfg.Build.Subagents.Enabled)},
		{"build.subagents.concurrency", fmt.Sprintf("%d", cfg.Build.Subagents.Concurrency)},
		{"build.subagents.roles", strings.Join(cfg.Build.Subagents.Roles, ", ")},
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
		{"claw.enabled", fmt.Sprintf("%t", cfg.Claw.Enabled)},
		{"claw.autostart", fmt.Sprintf("%t", cfg.Claw.Autostart)},
		{"claw.autonomy_policy", cfg.Claw.AutonomyPolicy},
		{"claw.default_channel", cfg.Claw.DefaultChannel},
		{"claw.persona_name", cfg.Claw.PersonaName},
		{"claw.persona_tone", cfg.Claw.PersonaTone},
		{"plugins.enabled", fmt.Sprintf("%t", cfg.Plugins.Enabled)},
	}
	for role, model := range cfg.Models {
		rows = append(rows, []string{"models." + role, model})
	}
	return t.FormatTable([]string{"Config", "Value"}, rows)
}

func (m model) describeWorkspaceSettings() string {
	t := m.theme
	cfg := m.options.Config
	// Compare against PURE Defaults() — not GlobalDefaultsConfig — so a value
	// inherited from the user's global.toml is not labelled "builtin" just
	// because it differs from no override. The explicit global-keys map
	// further disambiguates the edge case where a global value happens to
	// equal a builtin (e.g. user wrote parallel_slots=2 globally and the
	// builtin is also 2).
	pure := config.Defaults()
	keys := config.WorkspaceKeys(m.options.CWD)
	gKeys := globalconfig.LoadedKeys()
	providerKey := "providers.lmstudio.base_url"
	providerURL := cfg.Providers.LMStudio.BaseURL
	baseProviderURL := pure.Providers.LMStudio.BaseURL
	if cfg.Providers.Default.Name == "openai_compatible" {
		providerKey = "providers.openai_compatible.base_url"
		providerURL = cfg.Providers.OpenAICompatible.BaseURL
		baseProviderURL = pure.Providers.OpenAICompatible.BaseURL
	}
	rows := [][]string{
		{"provider", cfg.Providers.Default.Name, workspaceSettingSource(keys["providers.default.name"], gKeys["providers.default.name"], cfg.Providers.Default.Name, pure.Providers.Default.Name)},
		{"provider_url", providerURL, workspaceSettingSource(keys[providerKey], gKeys[providerKey], providerURL, baseProviderURL)},
		{"chat_model", cfg.Models["chat"], workspaceModelSource(keys, gKeys, cfg, pure, "chat")},
		{"explorer_model", cfg.Models["explorer"], workspaceModelSource(keys, gKeys, cfg, pure, "explorer")},
		{"planner_model", cfg.Models["planner"], workspaceModelSource(keys, gKeys, cfg, pure, "planner")},
		{"editor_model", cfg.Models["editor"], workspaceModelSource(keys, gKeys, cfg, pure, "editor")},
		{"reviewer_model", cfg.Models["reviewer"], workspaceModelSource(keys, gKeys, cfg, pure, "reviewer")},
		{"summarizer_model", cfg.Models["summarizer"], workspaceModelSource(keys, gKeys, cfg, pure, "summarizer")},
		{"model_loading.enabled", fmt.Sprintf("%t", cfg.ModelLoading.Enabled), workspaceSettingSource(keys["model_loading.enabled"], gKeys["model_loading.enabled"], cfg.ModelLoading.Enabled, pure.ModelLoading.Enabled)},
		{"model_loading.strategy", cfg.ModelLoading.Strategy, workspaceSettingSource(keys["model_loading.strategy"], gKeys["model_loading.strategy"], cfg.ModelLoading.Strategy, pure.ModelLoading.Strategy)},
		{"model_loading.parallel_slots", fmt.Sprintf("%d", cfg.ModelLoading.ParallelSlots), workspaceSettingSource(keys["model_loading.parallel_slots"], gKeys["model_loading.parallel_slots"], cfg.ModelLoading.ParallelSlots, pure.ModelLoading.ParallelSlots)},
		{"context.yarn.profile", cfg.Context.Yarn.Profile, workspaceSettingSource(keys["context.yarn.profile"], gKeys["yarn.profile"], cfg.Context.Yarn.Profile, pure.Context.Yarn.Profile)},
		{"context.budget_tokens", fmt.Sprintf("%d", cfg.Context.BudgetTokens), workspaceSettingSource(keys["context.budget_tokens"], gKeys["yarn.budget_tokens"], cfg.Context.BudgetTokens, pure.Context.BudgetTokens)},
		{"context.model_context_tokens", fmt.Sprintf("%d", cfg.Context.ModelContextTokens), workspaceSettingSource(keys["context.model_context_tokens"], gKeys["yarn.model_context_tokens"], cfg.Context.ModelContextTokens, pure.Context.ModelContextTokens)},
		{"context.reserve_output_tokens", fmt.Sprintf("%d", cfg.Context.ReserveOutputTokens), workspaceSettingSource(keys["context.reserve_output_tokens"], gKeys["yarn.reserve_output_tokens"], cfg.Context.ReserveOutputTokens, pure.Context.ReserveOutputTokens)},
	}
	if cfg.Context.Detected != nil && cfg.Context.Detected.LoadedContextLength > 0 {
		rows = append(rows, []string{
			"context.detected.chat",
			fmt.Sprintf("%d", cfg.Context.Detected.LoadedContextLength),
			workspaceSettingSource(keys["context.detected"], false, cfg.Context.Detected.LoadedContextLength, detectedLength(pure.Context.Detected)),
		})
	}
	return t.Muted.Render("Effective workspace settings. Source: workspace = .forge/config.toml override; global = ~/.forge/global.toml; builtin = forge default.") +
		"\n\n" + t.FormatTable([]string{"Setting", "Value", "Source"}, rows)
}

func workspaceSettingSource(isLocal, isGlobal bool, effective, base any) string {
	if isLocal {
		return "workspace"
	}
	if isGlobal {
		return "global"
	}
	if effective != base {
		// Value differs from builtin but neither toml file declares it.
		// Most likely a Normalize-derived field (e.g. concurrency derived
		// from parallel_slots). Surface as "global" because the source is
		// at least non-builtin.
		return "global"
	}
	return "builtin"
}

func workspaceModelSource(keys, gKeys map[string]bool, effective, pure config.Config, role string) string {
	path := "models." + role
	if keys[path] {
		return "workspace"
	}
	if gKeys[path] {
		return "global"
	}
	if role != "chat" && strings.TrimSpace(effective.Models[role]) == strings.TrimSpace(effective.Models["chat"]) {
		switch chatSource := workspaceModelSource(keys, gKeys, effective, pure, "chat"); chatSource {
		case "workspace":
			return "workspace via chat"
		case "global":
			return "global via chat"
		}
	}
	if strings.TrimSpace(effective.Models[role]) != strings.TrimSpace(pure.Models[role]) {
		return "global"
	}
	return "builtin"
}

func detectedLength(detected *config.DetectedContext) int {
	if detected == nil {
		return 0
	}
	return detected.LoadedContextLength
}

func clawAvailability(service any) string {
	if service == nil {
		return "unavailable"
	}
	return "available"
}

func (m *model) enterReviewMode() string {
	return m.runSubagentCommand("reviewer", "Review the current git diff and report findings.")
}

func (m model) describeAgents() string {
	t := m.theme
	list := m.agentRuntime.Subagents.List()
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })

	rows := make([][]string, 0, len(list))
	for _, w := range list {
		rows = append(rows, []string{w.Name, w.Description, w.ModelRole, w.ContextMode})
	}
	table := t.FormatTable([]string{"Agent", "Description", "Model role", "Context"}, rows)

	// Short example using the first agent in the registry so the user has
	// a copy-pasteable starting point — listing alone doesn't tell them
	// the syntax, and "/agent" without args is what dropped them here.
	example := ""
	if len(list) > 0 {
		example = "\n\n" + t.Muted.Render("Run one with:") + "\n  " +
			t.StatusValue.Render("/agent "+list[0].Name+" \"<task description>\"") + "\n  " +
			t.Muted.Render("Tab autocompletes the agent name. Quote the task if it contains spaces.")
	}
	return table + example
}

// agentUsageHint produces the message shown when /agent is invoked
// without enough arguments. Pulls names from the live registry instead
// of a hardcoded list, so plugin-supplied agents surface here too.
func (m model) agentUsageHint() string {
	t := m.theme
	list := m.agentRuntime.Subagents.List()
	names := make([]string, 0, len(list))
	for _, w := range list {
		names = append(names, w.Name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString(t.Warning.Render("Usage: /agent <name> <task>"))
	if len(names) > 0 {
		b.WriteString("\n\n")
		b.WriteString(t.Muted.Render("Available agents (Tab to autocomplete):"))
		b.WriteString("\n  ")
		b.WriteString(t.StatusValue.Render(strings.Join(names, ", ")))
		b.WriteString("\n\n")
		b.WriteString(t.Muted.Render("Examples:"))
		b.WriteString("\n  ")
		b.WriteString(t.StatusValue.Render("/agent explorer \"find every place that calls render()\""))
		b.WriteString("\n  ")
		b.WriteString(t.StatusValue.Render("/agent reviewer \"review the current diff\""))
		b.WriteString("\n  ")
		b.WriteString(t.StatusValue.Render("/agents"))
		b.WriteString(t.Muted.Render("  — show this list with descriptions"))
	}
	return b.String()
}

func (m *model) runSubagentCommand(agentName, prompt string) string {
	t := m.theme
	// Async dispatch: RunSubagentStreaming runs the subagent on a
	// goroutine and emits its tool calls / results / final text to the
	// returned channel. We hand the channel to the existing event
	// consumer (waitForAgentEvent + appendAgentEvent) so the textarea
	// stays responsive and the user sees streaming progress instead of
	// a frozen UI for ~minutes followed by a wall of text. The previous
	// sync call to RunSubagent froze the TUI completely until the
	// subagent finished.
	//
	// Subagent lifetime is governed by config (subagent_timeout_seconds,
	// request_timeout_seconds for local backends, request_idle_timeout
	// 180s watchdog). No wall-clock here — the streaming wrapper just
	// pipes events through.
	m.agentEvents = m.agentRuntime.RunSubagentStreaming(context.Background(), agent.SubagentRequest{
		Agent:  agentName,
		Prompt: prompt,
	})
	m.agentRunning = true
	m.pendingSubagentName = agentName
	m.pendingCommand = waitForAgentEvent(m.agentEvents)
	// NOTE: the prior synchronous flow opened a confirmExplorerPlan form
	// after an "explorer" subagent finished so the user could promote
	// findings to plan mode. That coupling is dropped here — the regular
	// /mode explore → /mode plan handoff (PendingExplorerContext) covers
	// the same ground without needing a special form on slash dispatch.
	// Add it back via a synthesized post-EventDone hook if users miss
	// it.
	return t.AgentPrefix.Render("Running /agent " + agentName + "...")
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

func planRefinementPrompt(goal string) string {
	base := "Refine the existing plan and checklist for the user's latest request. Read the current plan with plan_get and the current checklist with task_list before changing anything. " +
		"Preserve completed work, prefer incremental task_* updates over todo_write, and use todo_write only if the user explicitly asked to replace the checklist from scratch."
	if strings.TrimSpace(goal) == "" {
		return base
	}
	return "PLAN REFINEMENT REQUEST: " + goal + "\n\n" + base
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
			if err := m.agentRuntime.Tasks.Clear(); err != nil {
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
		prompt := planRefinementPrompt("")
		if len(fields) > 2 {
			prompt = planRefinementPrompt(strings.Join(fields[2:], " "))
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
		"\n\n" + t.Muted.Render("These profiles affect run_command only. Commands stay inside the workspace by default and can optionally reuse the managed .forge/venv.") +
		"\n" + t.Muted.Render("Use /profile <name> or /permissions set <name> to change.")
}

func (m *model) setPermissionProfile(name string) string {
	profile, ok := permissions.GetProfile(name)
	if !ok {
		return m.theme.ErrorStyle.Render("Unknown profile: " + name + ". Available: " + strings.Join(permissions.ProfileNames(), ", "))
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

// describeWorkspaceSkills renders the Skills sidebar panel: just the
// installed skills (ScanLocal) with their scope and install path. The
// browser/install flow lives behind /skills — this panel is the read-only
// "what does this workspace already have" view that fits the same shape
// as the Tools and MCPs panels.
func (m model) describeWorkspaceSkills() string {
	if m.options.Skills == nil {
		return "Skills manager not available."
	}
	t := m.theme
	installed := m.options.Skills.ScanLocal()
	dirURL := strings.TrimSpace(m.options.Skills.Options().DirectoryURL)
	if dirURL == "" {
		dirURL = "https://skills.sh/"
	}
	header := t.Muted.Render("source: " + dirURL + " (use /skills to browse and install)")
	if len(installed) == 0 {
		return header + "\n\n" + t.Muted.Render("No skills installed in this workspace.")
	}
	rows := make([][]string, 0, len(installed))
	for _, s := range installed {
		rows = append(rows, []string{s.Name, s.Source, s.InstallPath, s.Description})
	}
	return header + "\n\n" + t.FormatTable([]string{"Skill", "Scope", "Path", "Description"}, rows)
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
		supported := strings.Join(p.SupportedComponents(), ", ")
		if supported == "" {
			supported = "-"
		}
		pending := strings.Join(p.PendingComponents(), ", ")
		if pending == "" {
			pending = "-"
		}
		rows = append(rows, []string{p.Name, p.Source, p.CompatibilityStatus(), supported, pending})
	}
	return t.FormatTable([]string{"Plugin", "Source", "Compat", "Supported", "Pending"}, rows)
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
			return m.theme.Warning.Render("Plan mode entered -- agent still running, interview will not auto-start.")
		}
		m.agentEvents = m.agentRuntime.Run(context.Background(), planInterviewPrompt(goal, false))
		m.agentRunning = true
		m.pendingCommand = waitForAgentEvent(m.agentEvents)
		return m.theme.Success.Render("Entering plan mode -- starting interview...")
	}
	if name != "plan" && name != "build" && m.showPlan {
		m.showPlan = false
		m.recalcLayout()
	}
	if name == "build" {
		m.showPlan = true
		m.recalcLayout()
		return m.theme.Success.Render("Build mode entered -- execution will work through the approved checklist and still ask approval for each edit.")
	}
	// Mode shown in status bar; no inline message.
	return ""
}

// openInVSCode launches the user's VS Code CLI on the workspace cwd.
// Non-blocking — Start() returns immediately so forge keeps responding
// while VS Code spins up. Fails gracefully when `code` is not on PATH
// (the most common cause is the user not having "Add to PATH" enabled
// during VS Code install on Windows).
func (m model) openInVSCode() string {
	bin, err := exec.LookPath("code")
	if err != nil {
		return m.theme.Warning.Render("`code` not found on PATH. In VS Code: View → Command Palette → \"Shell Command: Install 'code' command in PATH\".")
	}
	cmd := exec.Command(bin, m.options.CWD)
	if err := cmd.Start(); err != nil {
		return m.theme.ErrorStyle.Render("Failed to launch VS Code: " + err.Error())
	}
	// Detach: the TUI does not own this child process beyond the launch.
	go func() { _ = cmd.Wait() }()
	return m.theme.Success.Render("Opening " + m.options.CWD + " in VS Code...")
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
