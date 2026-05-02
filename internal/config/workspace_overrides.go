package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"forge/internal/globalconfig"

	"github.com/pelletier/go-toml/v2"
)

type managedSetting struct {
	path  string
	value func(Config) any
}

// WorkspaceKeys returns the set of dotted TOML keys explicitly present in the
// workspace's .forge/config.toml.
func WorkspaceKeys(cwd string) map[string]bool {
	return loadWorkspaceKeys(cwd)
}

// GlobalDefaultsConfig returns the built-in defaults overlaid with the user's
// global Hub defaults. It intentionally does not include any workspace-local
// overrides.
func GlobalDefaultsConfig() (Config, error) {
	cfg := Defaults()
	g, err := globalconfig.Load()
	if err != nil {
		return cfg, err
	}
	ApplyGlobalConfig(&cfg, g)
	InheritChatModelDefaults(&cfg)
	return cfg, nil
}

// PersistWorkspaceConfig writes only workspace-local overrides to
// .forge/config.toml. Values inherited unchanged from the Hub/global defaults
// are stripped so future Hub edits still flow into the workspace.
func PersistWorkspaceConfig(cwd string, effective Config) error {
	path := filepath.Join(cwd, ".forge", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	raw := loadWorkspaceRawMap(path)
	base, err := GlobalDefaultsConfig()
	if err != nil {
		return err
	}
	Normalize(&effective)
	for _, setting := range managedWorkspaceSettings() {
		baseValue := setting.value(base)
		effectiveValue := setting.value(effective)
		if reflect.DeepEqual(baseValue, effectiveValue) || effectiveValue == nil {
			deleteNested(raw, strings.Split(setting.path, "."))
			continue
		}
		setNested(raw, strings.Split(setting.path, "."), effectiveValue)
	}

	data, err := toml.Marshal(raw)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func managedWorkspaceSettings() []managedSetting {
	settings := []managedSetting{
		{path: "providers.default.name", value: func(cfg Config) any { return cfg.Providers.Default.Name }},
		{path: "providers.openai_compatible.base_url", value: func(cfg Config) any { return cfg.Providers.OpenAICompatible.BaseURL }},
		{path: "providers.openai_compatible.api_key", value: func(cfg Config) any { return cfg.Providers.OpenAICompatible.APIKey }},
		{path: "providers.openai_compatible.api_key_env", value: func(cfg Config) any { return cfg.Providers.OpenAICompatible.APIKeyEnv }},
		{path: "providers.openai_compatible.default_model", value: func(cfg Config) any { return cfg.Providers.OpenAICompatible.DefaultModel }},
		{path: "providers.openai_compatible.supports_tools", value: func(cfg Config) any { return cfg.Providers.OpenAICompatible.SupportsTools }},
		{path: "providers.lmstudio.base_url", value: func(cfg Config) any { return cfg.Providers.LMStudio.BaseURL }},
		{path: "providers.lmstudio.api_key", value: func(cfg Config) any { return cfg.Providers.LMStudio.APIKey }},
		{path: "providers.lmstudio.api_key_env", value: func(cfg Config) any { return cfg.Providers.LMStudio.APIKeyEnv }},
		{path: "providers.lmstudio.default_model", value: func(cfg Config) any { return cfg.Providers.LMStudio.DefaultModel }},
		{path: "providers.lmstudio.supports_tools", value: func(cfg Config) any { return cfg.Providers.LMStudio.SupportsTools }},
		{path: "model_loading.enabled", value: func(cfg Config) any { return cfg.ModelLoading.Enabled }},
		{path: "model_loading.strategy", value: func(cfg Config) any { return cfg.ModelLoading.Strategy }},
		{path: "model_loading.parallel_slots", value: func(cfg Config) any { return cfg.ModelLoading.ParallelSlots }},
		{path: "context.budget_tokens", value: func(cfg Config) any { return cfg.Context.BudgetTokens }},
		{path: "context.model_context_tokens", value: func(cfg Config) any { return cfg.Context.ModelContextTokens }},
		{path: "context.reserve_output_tokens", value: func(cfg Config) any { return cfg.Context.ReserveOutputTokens }},
		{path: "context.detected", value: func(cfg Config) any { return detectedValue(cfg.Context.Detected) }},
		{path: "context.yarn.profile", value: func(cfg Config) any { return cfg.Context.Yarn.Profile }},
		{path: "context.yarn.max_nodes", value: func(cfg Config) any { return cfg.Context.Yarn.MaxNodes }},
		{path: "context.yarn.max_file_bytes", value: func(cfg Config) any { return cfg.Context.Yarn.MaxFileBytes }},
		{path: "context.yarn.history_events", value: func(cfg Config) any { return cfg.Context.Yarn.HistoryEvents }},
		{path: "context.yarn.pins", value: func(cfg Config) any { return cfg.Context.Yarn.Pins }},
		{path: "context.yarn.mentions", value: func(cfg Config) any { return cfg.Context.Yarn.Mentions }},
		{path: "context.yarn.compact_events", value: func(cfg Config) any { return cfg.Context.Yarn.CompactEvents }},
		{path: "context.yarn.compact_transcript_chars", value: func(cfg Config) any { return cfg.Context.Yarn.CompactTranscriptChars }},
		{path: "context.yarn.render_mode", value: func(cfg Config) any { return cfg.Context.Yarn.RenderMode }},
		{path: "context.yarn.render_head_lines", value: func(cfg Config) any { return cfg.Context.Yarn.RenderHeadLines }},
		{path: "approval_profile", value: func(cfg Config) any { return cfg.ApprovalProfile }},
		{path: "runtime.request_timeout_seconds", value: func(cfg Config) any { return cfg.Runtime.RequestTimeoutSeconds }},
		{path: "runtime.request_idle_timeout_seconds", value: func(cfg Config) any { return cfg.Runtime.RequestIdleTimeoutSeconds }},
		{path: "runtime.subagent_timeout_seconds", value: func(cfg Config) any { return cfg.Runtime.SubagentTimeoutSeconds }},
		{path: "runtime.task_timeout_seconds", value: func(cfg Config) any { return cfg.Runtime.TaskTimeoutSeconds }},
		{path: "runtime.max_steps", value: func(cfg Config) any { return cfg.Runtime.MaxSteps }},
		{path: "runtime.max_steps_build", value: func(cfg Config) any { return cfg.Runtime.MaxStepsBuild }},
		{path: "runtime.max_no_progress_steps", value: func(cfg Config) any { return cfg.Runtime.MaxNoProgressSteps }},
		{path: "runtime.max_empty_responses", value: func(cfg Config) any { return cfg.Runtime.MaxEmptyResponses }},
		{path: "runtime.max_same_tool_failures", value: func(cfg Config) any { return cfg.Runtime.MaxSameToolFailures }},
		{path: "runtime.max_consecutive_read_only", value: func(cfg Config) any { return cfg.Runtime.MaxConsecutiveReadOnly }},
		{path: "runtime.max_planner_summary_steps", value: func(cfg Config) any { return cfg.Runtime.MaxPlannerSummarySteps }},
		{path: "runtime.max_builder_read_loops", value: func(cfg Config) any { return cfg.Runtime.MaxBuilderReadLoops }},
		{path: "runtime.retry_on_provider_timeout", value: func(cfg Config) any { return cfg.Runtime.RetryOnProviderTimeout }},
		{path: "runtime.inline_builder", value: func(cfg Config) any { return cfg.Runtime.InlineBuilder }},
	}

	for _, role := range []string{"chat", "explorer", "planner", "editor", "reviewer", "summarizer"} {
		role := role
		settings = append(settings,
			managedSetting{
				path: "models." + role,
				value: func(cfg Config) any {
					return strings.TrimSpace(cfg.Models[role])
				},
			},
			managedSetting{
				path: "context.detected_by_role." + role,
				value: func(cfg Config) any {
					if cfg.Context.DetectedByRole == nil {
						return nil
					}
					detected, ok := cfg.Context.DetectedByRole[role]
					if !ok {
						return nil
					}
					return detectedValue(&detected)
				},
			},
		)
	}

	return settings
}

func detectedValue(detected *DetectedContext) any {
	if detected == nil || detected.LoadedContextLength <= 0 {
		return nil
	}
	return map[string]any{
		"model_id":              detected.ModelID,
		"loaded_context_length": detected.LoadedContextLength,
		"max_context_length":    detected.MaxContextLength,
		"probed_at":             detected.ProbedAt,
	}
}

func loadWorkspaceRawMap(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil || raw == nil {
		return map[string]any{}
	}
	return raw
}

func setNested(target map[string]any, parts []string, value any) {
	if len(parts) == 0 {
		return
	}
	current := target
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok || next == nil {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}

func deleteNested(target map[string]any, parts []string) bool {
	if len(parts) == 0 {
		return len(target) == 0
	}
	head := parts[0]
	if len(parts) == 1 {
		delete(target, head)
		return len(target) == 0
	}
	next, ok := target[head].(map[string]any)
	if !ok || next == nil {
		return len(target) == 0
	}
	if deleteNested(next, parts[1:]) {
		delete(target, head)
	} else {
		target[head] = next
	}
	return len(target) == 0
}
