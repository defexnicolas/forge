package config

import "testing"

func TestDefaultsUseRecommendedYarnProfile(t *testing.T) {
	cfg := Defaults()
	if cfg.Models["explorer"] != "local-model" {
		t.Fatalf("models.explorer = %q, want local-model", cfg.Models["explorer"])
	}
	if cfg.ModelLoading.Strategy != "single" {
		t.Fatalf("model_loading.strategy = %q, want single", cfg.ModelLoading.Strategy)
	}
	if cfg.ModelLoading.Enabled {
		t.Fatal("model_loading.enabled should default to false")
	}
	if cfg.ModelLoading.ParallelSlots != 2 {
		t.Fatalf("parallel_slots = %d, want 2", cfg.ModelLoading.ParallelSlots)
	}
	if !cfg.Build.Subagents.Enabled || cfg.Build.Subagents.Concurrency != 2 {
		t.Fatalf("unexpected build subagent defaults: %#v", cfg.Build.Subagents)
	}
	if cfg.Context.Yarn.Profile != "9B" {
		t.Fatalf("profile = %q, want 9B", cfg.Context.Yarn.Profile)
	}
	if cfg.Context.ModelContextTokens != 16384 {
		t.Fatalf("model_context_tokens = %d, want 16384", cfg.Context.ModelContextTokens)
	}
	if cfg.Context.BudgetTokens != 8000 {
		t.Fatalf("budget_tokens = %d, want 8000", cfg.Context.BudgetTokens)
	}
	if cfg.Context.ReserveOutputTokens != 2000 {
		t.Fatalf("reserve_output_tokens = %d, want 2000", cfg.Context.ReserveOutputTokens)
	}
	if cfg.Context.Yarn.MaxNodes != 12 || cfg.Context.Yarn.MaxFileBytes != 14000 || cfg.Context.Yarn.HistoryEvents != 14 {
		t.Fatalf("unexpected yarn defaults: %#v", cfg.Context.Yarn)
	}
	if cfg.Context.Task.BudgetTokens != 4000 ||
		cfg.Context.Task.MaxNodes != 6 ||
		cfg.Context.Task.MaxFileBytes != 8000 ||
		cfg.Context.Task.HistoryEvents != 4 {
		t.Fatalf("unexpected task context defaults: %#v", cfg.Context.Task)
	}
}

func TestNormalizeBackfillsMultiModelDefaults(t *testing.T) {
	cfg := Config{Models: map[string]string{"chat": "qwen"}}
	Normalize(&cfg)
	if cfg.Models["chat"] != "qwen" {
		t.Fatalf("models.chat = %q, want qwen", cfg.Models["chat"])
	}
	if cfg.Models["explorer"] == "" || cfg.Models["planner"] == "" || cfg.Models["editor"] == "" {
		t.Fatalf("expected multi model roles to be backfilled, got %#v", cfg.Models)
	}
	if cfg.ModelLoading.Strategy != "single" {
		t.Fatalf("strategy = %q, want single", cfg.ModelLoading.Strategy)
	}
	if cfg.Context.Task.BudgetTokens != 4000 || cfg.Context.Task.MaxNodes != 6 {
		t.Fatalf("expected task context defaults, got %#v", cfg.Context.Task)
	}
	if cfg.Build.Subagents.Concurrency != 2 || len(cfg.Build.Subagents.Roles) == 0 {
		t.Fatalf("expected build concurrency defaults, got %#v", cfg.Build.Subagents)
	}
}

func TestConfigForTaskRoleUsesSmallContext(t *testing.T) {
	cfg := Defaults()
	cfg.Context.BudgetTokens = 12000
	cfg.Context.Yarn.MaxNodes = 20
	cfg.Context.Yarn.MaxFileBytes = 50000
	cfg.Context.Yarn.HistoryEvents = 30
	cfg.Context.Task = TaskContextConfig{
		BudgetTokens:  1234,
		MaxNodes:      3,
		MaxFileBytes:  4567,
		HistoryEvents: 2,
	}
	SetDetectedForRole(&cfg, "explorer", &DetectedContext{ModelID: "explore-model", LoadedContextLength: 32000})
	cfg.Models["explorer"] = "explore-model"

	taskCfg := ConfigForTaskRole(cfg, "explorer", "explore-model")
	if taskCfg.Models["chat"] != "explore-model" {
		t.Fatalf("task chat model = %q", taskCfg.Models["chat"])
	}
	if taskCfg.Context.BudgetTokens != 1234 ||
		taskCfg.Context.Yarn.MaxNodes != 3 ||
		taskCfg.Context.Yarn.MaxFileBytes != 4567 ||
		taskCfg.Context.Yarn.HistoryEvents != 2 {
		t.Fatalf("unexpected task context config: %#v", taskCfg.Context)
	}
	if taskCfg.Context.Detected != nil {
		t.Fatalf("task context should not inherit detected large window: %#v", taskCfg.Context.Detected)
	}
}

func TestNormalizeDerivesBuildConcurrencyFromParallelSlots(t *testing.T) {
	cfg := Config{
		Models: map[string]string{"chat": "qwen"},
		ModelLoading: ModelLoadingConfig{
			ParallelSlots: 4,
		},
	}
	Normalize(&cfg)
	if cfg.Build.Subagents.Concurrency != 2 {
		t.Fatalf("concurrency = %d, want capped default 2", cfg.Build.Subagents.Concurrency)
	}
	if len(cfg.Build.Subagents.Roles) == 0 {
		t.Fatalf("expected default build subagent roles")
	}
}

func TestApplyYarnProfiles(t *testing.T) {
	cases := []struct {
		name    string
		budget  int
		reserve int
		nodes   int
		file    int
		history int
	}{
		{"2B", 5000, 1500, 8, 10000, 10},
		{"9B", 8000, 2000, 12, 14000, 14},
		{"26B", 20000, 3500, 22, 24000, 26},
	}
	for _, tc := range cases {
		cfg := Defaults()
		profile, ok := ApplyYarnProfile(&cfg, tc.name)
		if !ok {
			t.Fatalf("profile %s not found", tc.name)
		}
		if profile.Name != tc.name {
			t.Fatalf("profile name = %q, want %q", profile.Name, tc.name)
		}
		if cfg.Context.BudgetTokens != tc.budget ||
			cfg.Context.ReserveOutputTokens != tc.reserve ||
			cfg.Context.Yarn.MaxNodes != tc.nodes ||
			cfg.Context.Yarn.MaxFileBytes != tc.file ||
			cfg.Context.Yarn.HistoryEvents != tc.history {
			t.Fatalf("%s applied unexpected config: %#v", tc.name, cfg.Context)
		}
	}
}
