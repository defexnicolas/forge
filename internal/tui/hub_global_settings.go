package tui

import (
	"strings"

	"forge/internal/config"
	"forge/internal/globalconfig"
)

func loadHubGlobalConfig() (config.Config, error) {
	cfg := config.Defaults()
	g, err := globalconfig.Load()
	if err != nil {
		return cfg, err
	}
	config.ApplyGlobalConfig(&cfg, g)
	config.InheritChatModelDefaults(&cfg)
	return cfg, nil
}

func saveHubGlobalConfig(cfg config.Config) error {
	current, err := globalconfig.Load()
	if err != nil {
		return err
	}
	current.Models = cloneStringMap(cfg.Models)
	current.ModelLoading = &globalconfig.ModelLoadingDefaults{
		Enabled:       boolPtr(cfg.ModelLoading.Enabled),
		Strategy:      stringPtr(cfg.ModelLoading.Strategy),
		ParallelSlots: intPtr(cfg.ModelLoading.ParallelSlots),
	}
	current.DetectedByRole = map[string]globalconfig.DetectedModel{}
	if cfg.Context.Detected != nil && cfg.Context.Detected.LoadedContextLength > 0 {
		current.DetectedByRole["chat"] = toGlobalDetected(*cfg.Context.Detected)
	}
	for role, detected := range cfg.Context.DetectedByRole {
		if detected.LoadedContextLength <= 0 {
			continue
		}
		current.DetectedByRole[role] = toGlobalDetected(detected)
	}
	current.Providers = map[string]globalconfig.ProviderEntry{
		"openai_compatible": toGlobalProvider(cfg.Providers.OpenAICompatible),
		"lmstudio":          toGlobalProvider(cfg.Providers.LMStudio),
	}
	current.Yarn = &globalconfig.YarnDefaults{
		Profile:                stringPtr(cfg.Context.Yarn.Profile),
		BudgetTokens:           intPtr(cfg.Context.BudgetTokens),
		ModelContextTokens:     intPtr(cfg.Context.ModelContextTokens),
		ReserveOutputTokens:    intPtr(cfg.Context.ReserveOutputTokens),
		MaxNodes:               intPtr(cfg.Context.Yarn.MaxNodes),
		MaxFileBytes:           intPtr(cfg.Context.Yarn.MaxFileBytes),
		HistoryEvents:          intPtr(cfg.Context.Yarn.HistoryEvents),
		Pins:                   stringPtr(cfg.Context.Yarn.Pins),
		Mentions:               stringPtr(cfg.Context.Yarn.Mentions),
		CompactEvents:          intPtr(cfg.Context.Yarn.CompactEvents),
		CompactTranscriptChars: intPtr(cfg.Context.Yarn.CompactTranscriptChars),
		RenderMode:             stringPtr(cfg.Context.Yarn.RenderMode),
		RenderHeadLine:         intPtr(cfg.Context.Yarn.RenderHeadLines),
	}
	// WebSearch — only persist when at least one field is set, so the
	// global file stays clean for users who never opened the form.
	ws := cfg.WebSearch
	if ws.Provider != "" || ws.APIKey != "" || ws.APIKeyEnv != "" || ws.BaseURL != "" {
		current.WebSearch = &globalconfig.WebSearchDefaults{
			Provider:  stringPtr(ws.Provider),
			APIKey:    stringPtr(ws.APIKey),
			APIKeyEnv: stringPtr(ws.APIKeyEnv),
			BaseURL:   stringPtr(ws.BaseURL),
		}
	}
	if strings.TrimSpace(cfg.OutputStyle) != "" {
		current.OutputStyle = stringPtr(cfg.OutputStyle)
	}
	if strings.TrimSpace(cfg.ApprovalProfile) != "" {
		current.ApprovalProfile = stringPtr(cfg.ApprovalProfile)
	}
	// Claw — persist any non-default cadence / persona / tools override.
	defaults := config.Defaults().Claw
	if cfg.Claw.HeartbeatIntervalSeconds != defaults.HeartbeatIntervalSeconds ||
		cfg.Claw.DreamIntervalMinutes != defaults.DreamIntervalMinutes ||
		cfg.Claw.PersonaName != defaults.PersonaName ||
		cfg.Claw.PersonaTone != defaults.PersonaTone ||
		cfg.Claw.AutonomyPolicy != defaults.AutonomyPolicy ||
		cfg.Claw.ToolsEnabled != defaults.ToolsEnabled {
		current.Claw = &globalconfig.ClawDefaults{
			HeartbeatIntervalSeconds: intPtr(cfg.Claw.HeartbeatIntervalSeconds),
			DreamIntervalMinutes:     intPtr(cfg.Claw.DreamIntervalMinutes),
			PersonaName:              stringPtr(cfg.Claw.PersonaName),
			PersonaTone:              stringPtr(cfg.Claw.PersonaTone),
			AutonomyPolicy:           stringPtr(cfg.Claw.AutonomyPolicy),
			ToolsEnabled:             boolPtr(cfg.Claw.ToolsEnabled),
		}
	}
	return globalconfig.Save(current)
}

func toGlobalProvider(cfg config.ProviderConfig) globalconfig.ProviderEntry {
	return globalconfig.ProviderEntry{
		BaseURL:       stringPtr(cfg.BaseURL),
		APIKey:        stringPtr(cfg.APIKey),
		APIKeyEnv:     stringPtr(cfg.APIKeyEnv),
		DefaultModel:  stringPtr(cfg.DefaultModel),
		SupportsTools: boolPtr(cfg.SupportsTools),
	}
}

func toGlobalDetected(d config.DetectedContext) globalconfig.DetectedModel {
	return globalconfig.DetectedModel{
		ModelID:             d.ModelID,
		LoadedContextLength: d.LoadedContextLength,
		MaxContextLength:    d.MaxContextLength,
		ProbedAt:            d.ProbedAt,
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func stringPtr(v string) *string { return &v }
func intPtr(v int) *int          { return &v }
func boolPtr(v bool) *bool       { return &v }
