package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"forge/internal/config"
	contextbuilder "forge/internal/context"
	"forge/internal/llm"
	"forge/internal/session"
	"forge/internal/yarn"

	"github.com/pelletier/go-toml/v2"
)

// /yarn shows graph and node details.
func (m *model) handleYarnCommand(fields []string) string {
	if len(fields) >= 2 {
		switch fields[1] {
		case "graph":
			return m.yarnGraph()
		case "settings":
			if len(fields) >= 5 && fields[2] == "set" {
				return m.yarnSettingsSet(fields[3], strings.Join(fields[4:], " "))
			}
			m.activeForm = formYarnSettings
			m.yarnSettingsForm = newYarnSettingsForm(m.options.CWD, m.options.Config, m.theme)
			return "Opening YARN settings..."
		case "profiles":
			return m.describeYarnProfiles()
		case "profile":
			if len(fields) < 3 {
				return "Usage: /yarn profile <2B|4B|9B|14B|26B>"
			}
			return m.applyYarnProfile(fields[2])
		case "dry-run":
			if len(fields) < 3 {
				return "Usage: /yarn dry-run <prompt>"
			}
			return m.yarnDryRun(strings.Join(fields[2:], " "))
		case "inspect":
			if len(fields) < 3 {
				return "Usage: /yarn inspect <node-id>"
			}
			return m.yarnInspect(fields[2])
		case "probe":
			return m.yarnProbe()
		}
	}
	m.activeForm = formYarnMenu
	m.yarnMenuForm = newYarnMenuForm(m.theme)
	return "Opening YARN menu..."
}

func (m model) describeYarn() string {
	t := m.theme
	store := yarn.New(m.options.CWD)
	nodes, err := store.Load()
	if err != nil {
		return t.ErrorStyle.Render("YARN unavailable: " + err.Error())
	}
	rows := make([][]string, 0, len(nodes))
	for _, node := range nodes {
		label := node.Kind
		if node.Path != "" {
			label += ":" + node.Path
		}
		links := ""
		if len(node.Links) > 0 {
			links = strings.Join(node.Links, ", ")
		}
		rows = append(rows, []string{node.ID, label, links, node.UpdatedAt.Format("15:04:05")})
	}
	window, budget, reserve := config.EffectiveBudgets(m.options.Config)
	detected := "none"
	if d := m.options.Config.Context.Detected; d != nil && d.LoadedContextLength > 0 {
		age := time.Since(d.ProbedAt).Round(time.Second)
		detected = fmt.Sprintf("%d (model=%s, probed %s ago)", d.LoadedContextLength, d.ModelID, age)
	}
	header := fmt.Sprintf("engine=%s  profile=%s  budget=%d  reserve=%d  window=%d  max_nodes=%d  path=%s  nodes=%d\ndetected: %s",
		m.options.Config.Context.Engine,
		m.options.Config.Context.Yarn.Profile,
		budget,
		reserve,
		window,
		m.options.Config.Context.Yarn.MaxNodes,
		store.Path(),
		len(nodes),
		detected,
	)
	if len(rows) == 0 {
		return header + "\n" + t.Muted.Render("No YARN nodes yet.")
	}
	return header + "\n\n" + t.FormatTable([]string{"ID", "Label", "Links", "Updated"}, rows)
}

func (m model) describeYarnProfiles() string {
	rows := make([][]string, 0)
	current := strings.ToUpper(m.options.Config.Context.Yarn.Profile)
	for _, profile := range config.YarnProfiles() {
		marker := " "
		if profile.Name == current {
			marker = "*"
		}
		rows = append(rows, []string{
			marker,
			profile.Name,
			fmt.Sprintf("%dk-%dk", profile.LMContextMin/1000, profile.LMContextMax/1000),
			strconv.Itoa(profile.BudgetTokens),
			strconv.Itoa(profile.ReserveOutputTokens),
			strconv.Itoa(profile.MaxNodes),
			strconv.Itoa(profile.MaxFileBytes),
			strconv.Itoa(profile.HistoryEvents),
		})
	}
	return m.theme.FormatTable([]string{" ", "Profile", "LM ctx", "YARN", "Reserve", "Nodes", "File bytes", "History"}, rows) +
		"\n\n" + m.theme.Muted.Render("Use /yarn profile <name> to apply. LM ctx is a recommendation for LM Studio.")
}

func (m *model) applyYarnProfile(name string) string {
	profile, ok := config.ApplyYarnProfile(&m.options.Config, name)
	if !ok {
		return m.theme.ErrorStyle.Render("Unknown YARN profile: " + name + ". Available: 2B, 4B, 9B, 14B, 26B")
	}
	m.persistConfig()
	m.syncRuntimeConfig()
	return m.theme.Success.Render("Applied YARN profile: "+profile.Name) +
		fmt.Sprintf("\nRecommended LM Studio context: %d-%d", profile.LMContextMin, profile.LMContextMax) +
		fmt.Sprintf("\nYARN budget=%d reserve=%d max_nodes=%d history=%d", profile.BudgetTokens, profile.ReserveOutputTokens, profile.MaxNodes, profile.HistoryEvents) +
		yarnOverflowWarning(m.options.Config, m.theme)
}

func (m *model) yarnSettingsSet(key, value string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	value = strings.TrimSpace(value)
	switch key {
	case "profile":
		return m.applyYarnProfile(value)
	case "engine":
		if value != "yarn" && value != "simple" {
			return "engine must be yarn or simple"
		}
		m.options.Config.Context.Engine = value
	case "budget_tokens", "budget":
		m.options.Config.Context.BudgetTokens = parsePositive(value, m.options.Config.Context.BudgetTokens)
	case "model_context_tokens", "model_context", "lm_context":
		m.options.Config.Context.ModelContextTokens = parsePositive(value, m.options.Config.Context.ModelContextTokens)
	case "reserve_output_tokens", "reserve":
		m.options.Config.Context.ReserveOutputTokens = parsePositive(value, m.options.Config.Context.ReserveOutputTokens)
	case "max_nodes", "nodes":
		m.options.Config.Context.Yarn.MaxNodes = parsePositive(value, m.options.Config.Context.Yarn.MaxNodes)
	case "max_file_bytes", "file_bytes":
		m.options.Config.Context.Yarn.MaxFileBytes = parsePositive(value, m.options.Config.Context.Yarn.MaxFileBytes)
	case "history_events", "history":
		m.options.Config.Context.Yarn.HistoryEvents = parsePositive(value, m.options.Config.Context.Yarn.HistoryEvents)
	case "pins":
		m.options.Config.Context.Yarn.Pins = normalizeYarnMode(value, m.options.Config.Context.Yarn.Pins, true)
	case "mentions":
		m.options.Config.Context.Yarn.Mentions = normalizeYarnMode(value, m.options.Config.Context.Yarn.Mentions, false)
	case "compact_events":
		m.options.Config.Context.Yarn.CompactEvents = parsePositive(value, m.options.Config.Context.Yarn.CompactEvents)
	case "compact_transcript_chars", "compact_chars":
		m.options.Config.Context.Yarn.CompactTranscriptChars = parsePositive(value, m.options.Config.Context.Yarn.CompactTranscriptChars)
	default:
		return "Unknown YARN setting: " + key
	}
	config.Normalize(&m.options.Config)
	m.persistConfig()
	m.syncRuntimeConfig()
	return m.theme.Success.Render("YARN setting updated: "+key+"="+value) + yarnOverflowWarning(m.options.Config, m.theme)
}

func (m model) yarnDryRun(prompt string) string {
	snapshot := m.agentRuntime.Builder.BuildWithOptions(prompt, contextbuilder.BuildOptions{RecordYarn: false})
	preview, err := m.agentRuntime.PreviewPrompt(prompt)
	if err != nil {
		return m.theme.ErrorStyle.Render("Prompt preview unavailable: " + err.Error())
	}
	rows := make([][]string, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		source := item.Source
		if source == "" {
			source = "-"
		}
		mode := item.Mode
		if mode == "" {
			mode = "-"
		}
		rows = append(rows, []string{
			item.Kind,
			item.Path,
			strconv.Itoa(len(item.Content) / 4),
			source,
			mode,
		})
	}
	warning := "none"
	if snapshot.TokensBudget > 0 && snapshot.TokensUsed > snapshot.TokensBudget {
		warning = "estimated context exceeds YARN budget"
	}
	if m.options.Config.Context.ModelContextTokens > 0 &&
		m.options.Config.Context.BudgetTokens+m.options.Config.Context.ReserveOutputTokens > m.options.Config.Context.ModelContextTokens {
		warning = "YARN budget + reserved output exceeds model context"
	}
	summary := fmt.Sprintf("estimated prompt context: %d / %d YARN tokens\nmodel window: %d\nreserved output: %d\nwarning: %s",
		snapshot.TokensUsed,
		snapshot.TokensBudget,
		m.options.Config.Context.ModelContextTokens,
		m.options.Config.Context.ReserveOutputTokens,
		warning,
	)
	strategy := preview.ArtifactStrategy
	if strategy == "" {
		strategy = "none"
	}
	summary += fmt.Sprintf("\nmode: %s\nnative tools: %t\nartifact strategy: %s", strings.ToUpper(m.agentRuntime.Mode), preview.SupportsTools, strategy)
	if len(rows) == 0 {
		return summary +
			"\n\n" + m.theme.Muted.Render("No context items selected.") +
			"\n\nSYSTEM PROMPT PREVIEW:\n" + truncatePreviewBlock(preview.System, 1400) +
			"\n\nUSER PROMPT PREVIEW:\n" + truncatePreviewBlock(preview.User, 2200)
	}
	return summary +
		"\n\n" + m.theme.FormatTable([]string{"Kind", "Path", "Tokens", "Source", "Mode"}, rows) +
		"\n\nSYSTEM PROMPT PREVIEW:\n" + truncatePreviewBlock(preview.System, 1400) +
		"\n\nUSER PROMPT PREVIEW:\n" + truncatePreviewBlock(preview.User, 2200)
}

func truncatePreviewBlock(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 16 {
		return text[:limit]
	}
	return text[:limit-16] + "\n[truncated...]"
}

func (m model) yarnGraph() string {
	t := m.theme
	store := yarn.New(m.options.CWD)
	nodes, err := store.Load()
	if err != nil {
		return t.ErrorStyle.Render("YARN unavailable: " + err.Error())
	}
	if len(nodes) == 0 {
		return t.Muted.Render("No YARN nodes.")
	}
	path, err := writeYarnGraphHTML(m.options.CWD, nodes)
	if err != nil {
		return t.ErrorStyle.Render("YARN graph export failed: " + err.Error())
	}
	if err := openYarnGraphPath(path); err != nil {
		return t.Warning.Render("YARN graph generated, but browser open failed: "+err.Error()) + "\n" + path
	}
	return t.Success.Render("Opened YARN graph in browser.") + "\n" + path
}

func (m model) yarnInspect(id string) string {
	t := m.theme
	store := yarn.New(m.options.CWD)
	nodes, err := store.Load()
	if err != nil {
		return t.ErrorStyle.Render("YARN unavailable: " + err.Error())
	}
	for _, node := range nodes {
		if node.ID == id || strings.Contains(node.ID, id) {
			var b strings.Builder
			b.WriteString(t.TableHeader.Render("Node: "+node.ID) + "\n")
			b.WriteString(t.StatusKey.Render("Kind:    ") + node.Kind + "\n")
			b.WriteString(t.StatusKey.Render("Path:    ") + node.Path + "\n")
			b.WriteString(t.StatusKey.Render("Summary: ") + node.Summary + "\n")
			b.WriteString(t.StatusKey.Render("Updated: ") + node.UpdatedAt.Format("2006-01-02 15:04:05") + "\n")
			if len(node.Links) > 0 {
				b.WriteString(t.StatusKey.Render("Links:   ") + strings.Join(node.Links, ", ") + "\n")
			}
			b.WriteString(t.StatusKey.Render("Content: ") + "\n")
			content := node.Content
			if len(content) > 2000 {
				content = content[:2000] + "\n[truncated]"
			}
			b.WriteString(content)
			return b.String()
		}
	}
	return t.ErrorStyle.Render("Node not found: " + id)
}

func (m model) compactSession() string {
	if m.options.Session == nil {
		return "Session store unavailable."
	}
	events, err := m.options.Session.Tail(m.options.Config.Context.Yarn.CompactEvents)
	if err != nil {
		return "Session compact failed: " + err.Error()
	}
	if len(events) == 0 {
		return "No session events to compact."
	}
	providerName := m.options.Config.Providers.Default.Name
	if providerName == "" {
		providerName = "lmstudio"
	}
	provider, ok := m.options.Providers.Get(providerName)
	if !ok {
		return "Provider " + providerName + " is not registered."
	}
	mdl := m.options.Config.Models["summarizer"]
	if mdl == "" {
		mdl = m.options.Config.Models["chat"]
	}
	transcript := session.FormatTail(events)
	compactChars := m.options.Config.Context.Yarn.CompactTranscriptChars
	if compactChars <= 0 {
		compactChars = 50000
	}
	if len(transcript) > compactChars {
		transcript = transcript[len(transcript)-compactChars:]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := provider.Chat(ctx, llm.ChatRequest{
		Model: mdl,
		Messages: []llm.Message{
			{Role: "system", Content: "Summarize this Forge coding-agent session for future context. Keep durable decisions, files discussed, commands, errors, and next steps. Be concise and factual."},
			{Role: "user", Content: transcript},
		},
	})
	summary := ""
	fallback := ""
	if err != nil {
		summary = session.Summarize(events)
		fallback = "\nmodel compact failed: " + err.Error()
	} else {
		summary = strings.TrimSpace(resp.Content)
	}
	if summary == "" {
		summary = session.Summarize(events)
	}
	store := yarn.New(m.options.CWD)
	err = store.Upsert(yarn.Node{
		Kind:    "compact",
		Path:    "session:" + m.options.Session.ID(),
		Summary: "compacted session " + m.options.Session.ID(),
		Content: summary,
	})
	if err != nil {
		return "YARN compact write failed: " + err.Error()
	}
	return m.theme.Success.Render("Compacted session into YARN") + fallback
}

func (m *model) yarnProbe() string {
	t := m.theme
	providerName := m.options.Config.Providers.Default.Name
	if providerName == "" {
		providerName = "lmstudio"
	}
	provider, ok := m.options.Providers.Get(providerName)
	if !ok {
		return t.ErrorStyle.Render("Provider " + providerName + " not registered.")
	}
	role := m.activeModelRole()
	modelID := m.activeRoleModelID()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := provider.ProbeModel(ctx, modelID)
	if err != nil {
		return t.ErrorStyle.Render("Probe failed: " + err.Error())
	}
	if info == nil || info.LoadedContextLength <= 0 {
		return t.Warning.Render("Provider did not report loaded_context_length -- profile caps in effect.")
	}
	detected := &config.DetectedContext{
		ModelID:             info.ID,
		LoadedContextLength: info.LoadedContextLength,
		MaxContextLength:    info.MaxContextLength,
		ProbedAt:            time.Now().UTC(),
	}
	if role == "chat" {
		m.options.Config.Context.Detected = detected
	}
	config.SetDetectedForRole(&m.options.Config, role, detected)
	m.persistConfig()
	m.syncRuntimeConfig()
	_, budget, reserve := config.EffectiveBudgets(m.activeRoleConfig())
	return t.Success.Render(fmt.Sprintf("Probed %s (%s): loaded=%d max=%d", info.ID, role, info.LoadedContextLength, info.MaxContextLength)) +
		fmt.Sprintf("\nEffective YARN budget=%d reserve=%d", budget, reserve)
}

func (m *model) syncRuntimeConfig() {
	m.agentRuntime.Config = m.options.Config
	m.agentRuntime.Builder.Config = m.options.Config
}

func (m model) persistConfig() {
	dir := filepath.Join(m.options.CWD, ".forge")
	_ = os.MkdirAll(dir, 0o755)
	data, err := toml.Marshal(m.options.Config)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "config.toml"), data, 0o644)
}
