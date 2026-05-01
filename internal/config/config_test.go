package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge/internal/globalconfig"
)

func TestDefaultsUseRecommendedYarnProfile(t *testing.T) {
	cfg := Defaults()
	if !cfg.Providers.LMStudio.SupportsTools {
		t.Fatal("providers.lmstudio.supports_tools should default to true")
	}
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
	if cfg.Runtime.RequestTimeoutSeconds != 45 ||
		cfg.Runtime.SubagentTimeoutSeconds != 90 ||
		cfg.Runtime.TaskTimeoutSeconds != 180 ||
		cfg.Runtime.MaxNoProgressSteps != 3 ||
		cfg.Runtime.MaxEmptyResponses != 2 ||
		cfg.Runtime.MaxSameToolFailures != 2 ||
		cfg.Runtime.MaxConsecutiveReadOnly != 6 ||
		cfg.Runtime.MaxPlannerSummarySteps != 2 ||
		cfg.Runtime.MaxBuilderReadLoops != 4 ||
		cfg.Runtime.RetryOnProviderTimeout {
		t.Fatalf("unexpected runtime defaults: %#v", cfg.Runtime)
	}
	if !cfg.Git.AutoInit ||
		!cfg.Git.CreateBaselineCommit ||
		!cfg.Git.RequireCleanOrSnapshot ||
		!cfg.Git.AutoStageMutations ||
		cfg.Git.AutoCommit ||
		cfg.Git.BaselineCommitMessage == "" ||
		cfg.Git.SnapshotCommitMessage == "" {
		t.Fatalf("unexpected git defaults: %#v", cfg.Git)
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
	if cfg.Runtime.RequestTimeoutSeconds != 45 || cfg.Runtime.MaxBuilderReadLoops != 4 {
		t.Fatalf("expected runtime defaults, got %#v", cfg.Runtime)
	}
	if cfg.Git.BaselineCommitMessage == "" || cfg.Git.SnapshotCommitMessage == "" {
		t.Fatalf("expected git messages, got %#v", cfg.Git)
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

func writeWorkspaceConfig(t *testing.T, cwd string, raw string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(cwd, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".forge", "config.toml"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeGlobal(t *testing.T, g globalconfig.GlobalConfig) {
	t.Helper()
	if err := globalconfig.Save(g); err != nil {
		t.Fatalf("write global: %v", err)
	}
}

func TestLoadWithGlobalUsesGlobalForUnsetWorkspaceFields(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	cwd := t.TempDir()
	// Workspace omits explorer + planner models -- those fall to built-in
	// "local-model", which counts as "unset" so global wins.
	writeWorkspaceConfig(t, cwd, `
[models]
chat = "workspace-chat"
`)
	scope := "user"
	cli := "pnpx"
	writeGlobal(t, globalconfig.GlobalConfig{
		Models: map[string]string{
			"explorer": "global-explorer",
			"planner":  "global-planner",
			"chat":     "global-chat", // should LOSE: workspace set chat already
		},
		Skills: &globalconfig.SkillsDefaults{
			InstallScope: &scope,
			CLI:          &cli,
		},
	})

	cfg, err := LoadWithGlobal(cwd)
	if err != nil {
		t.Fatalf("LoadWithGlobal: %v", err)
	}
	if cfg.Models["chat"] != "workspace-chat" {
		t.Errorf("workspace 'chat' should win, got %q", cfg.Models["chat"])
	}
	if cfg.Models["explorer"] != "global-explorer" {
		t.Errorf("global should fill explorer, got %q", cfg.Models["explorer"])
	}
	if cfg.Models["planner"] != "global-planner" {
		t.Errorf("global should fill planner, got %q", cfg.Models["planner"])
	}
	if cfg.Skills.InstallScope != "user" {
		t.Errorf("global skills.install_scope should win, got %q", cfg.Skills.InstallScope)
	}
	if cfg.Skills.CLI != "pnpx" {
		t.Errorf("global skills.cli should win when workspace at default 'npx', got %q", cfg.Skills.CLI)
	}
}

func TestLoadWithGlobalRespectsWorkspaceOverride(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	cwd := t.TempDir()
	writeWorkspaceConfig(t, cwd, `
[skills]
install_scope = "project"
cli           = "yarn-dlx"
`)
	scope := "user"
	cli := "pnpx"
	writeGlobal(t, globalconfig.GlobalConfig{
		Skills: &globalconfig.SkillsDefaults{
			InstallScope: &scope,
			CLI:          &cli,
		},
	})

	cfg, err := LoadWithGlobal(cwd)
	if err != nil {
		t.Fatalf("LoadWithGlobal: %v", err)
	}
	if cfg.Skills.InstallScope != "project" {
		t.Errorf("workspace install_scope should win, got %q", cfg.Skills.InstallScope)
	}
	if cfg.Skills.CLI != "yarn-dlx" {
		t.Errorf("workspace cli should win, got %q", cfg.Skills.CLI)
	}
}

func TestLoadWithGlobalMissingGlobalIsFine(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir()) // no global written
	cwd := t.TempDir()
	writeWorkspaceConfig(t, cwd, `
[skills]
install_scope = "project"
`)
	cfg, err := LoadWithGlobal(cwd)
	if err != nil {
		t.Fatalf("missing global should not error, got %v", err)
	}
	if cfg.Skills.InstallScope != "project" {
		t.Errorf("workspace value lost, got %q", cfg.Skills.InstallScope)
	}
}

func TestLoadWithGlobalProvidersFillEmptyOnly(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	cwd := t.TempDir()
	// Workspace overrides only base_url for openai_compatible.
	writeWorkspaceConfig(t, cwd, `
[providers.openai_compatible]
base_url = "https://workspace.example/v1"
`)
	baseURL := "https://global.example/v1"
	apiKey := "global-key"
	writeGlobal(t, globalconfig.GlobalConfig{
		Providers: map[string]globalconfig.ProviderEntry{
			"openai_compatible": {
				BaseURL: &baseURL,
				APIKey:  &apiKey,
			},
		},
	})

	cfg, err := LoadWithGlobal(cwd)
	if err != nil {
		t.Fatalf("LoadWithGlobal: %v", err)
	}
	if cfg.Providers.OpenAICompatible.BaseURL != "https://workspace.example/v1" {
		t.Errorf("workspace base_url should win, got %q", cfg.Providers.OpenAICompatible.BaseURL)
	}
	if cfg.Providers.OpenAICompatible.APIKey != "global-key" {
		t.Errorf("global api_key should fill, got %q", cfg.Providers.OpenAICompatible.APIKey)
	}
}

func TestLoadWithGlobalAppliesModelLoadingAndDetectedDefaults(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	cwd := t.TempDir()
	writeGlobal(t, globalconfig.GlobalConfig{
		Models: map[string]string{
			"chat":    "hub-chat",
			"planner": "hub-planner",
		},
		ModelLoading: &globalconfig.ModelLoadingDefaults{
			Enabled:       boolPtr(true),
			Strategy:      stringPtr("single"),
			ParallelSlots: intPtr(4),
		},
		DetectedByRole: map[string]globalconfig.DetectedModel{
			"planner": {
				ModelID:             "hub-planner",
				LoadedContextLength: 64000,
			},
		},
		Yarn: &globalconfig.YarnDefaults{
			ModelContextTokens:  intPtr(64000),
			ReserveOutputTokens: intPtr(4000),
		},
	})

	cfg, err := LoadWithGlobal(cwd)
	if err != nil {
		t.Fatalf("LoadWithGlobal: %v", err)
	}
	if cfg.Models["planner"] != "hub-planner" {
		t.Fatalf("planner model = %q, want hub-planner", cfg.Models["planner"])
	}
	if !cfg.ModelLoading.Enabled || cfg.ModelLoading.Strategy != "single" || cfg.ModelLoading.ParallelSlots != 4 {
		t.Fatalf("unexpected model_loading: %#v", cfg.ModelLoading)
	}
	if detected := DetectedForRole(cfg, "planner", "hub-planner"); detected == nil || detected.LoadedContextLength != 64000 {
		t.Fatalf("planner detected = %#v, want loaded context 64000", detected)
	}
	if cfg.Context.ModelContextTokens != 64000 || cfg.Context.ReserveOutputTokens != 4000 {
		t.Fatalf("unexpected context defaults: model=%d reserve=%d", cfg.Context.ModelContextTokens, cfg.Context.ReserveOutputTokens)
	}
}

func TestLoadWithGlobalOverridesScaffoldModelLoadingDefaults(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	cwd := t.TempDir()
	writeWorkspaceConfig(t, cwd, `
[model_loading]
enabled = false
strategy = "single"
parallel_slots = 2
`)
	writeGlobal(t, globalconfig.GlobalConfig{
		ModelLoading: &globalconfig.ModelLoadingDefaults{
			Enabled:       boolPtr(true),
			Strategy:      stringPtr("parallel"),
			ParallelSlots: intPtr(4),
		},
	})

	cfg, err := LoadWithGlobal(cwd)
	if err != nil {
		t.Fatalf("LoadWithGlobal: %v", err)
	}
	if !cfg.ModelLoading.Enabled || cfg.ModelLoading.Strategy != "parallel" || cfg.ModelLoading.ParallelSlots != 4 {
		t.Fatalf("expected global model_loading to override scaffold defaults, got %#v", cfg.ModelLoading)
	}
}

func TestPersistWorkspaceConfigDoesNotMaterializeHubDefaults(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	cwd := t.TempDir()
	writeGlobal(t, globalconfig.GlobalConfig{
		Models: map[string]string{
			"chat": "hub-chat",
		},
		ModelLoading: &globalconfig.ModelLoadingDefaults{
			Enabled:       boolPtr(true),
			Strategy:      stringPtr("single"),
			ParallelSlots: intPtr(4),
		},
		Yarn: &globalconfig.YarnDefaults{
			Profile:            stringPtr("26B"),
			ModelContextTokens: intPtr(131072),
		},
	})

	cfg, err := LoadWithGlobal(cwd)
	if err != nil {
		t.Fatalf("LoadWithGlobal: %v", err)
	}
	InheritChatModelDefaults(&cfg)
	cfg.Context.Detected = &DetectedContext{
		ModelID:             "hub-chat",
		LoadedContextLength: 131072,
	}
	SetDetectedForRole(&cfg, "chat", cfg.Context.Detected)
	if err := PersistWorkspaceConfig(cwd, cfg); err != nil {
		t.Fatalf("PersistWorkspaceConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(cwd, ".forge", "config.toml"))
	if err != nil {
		t.Fatalf("read persisted workspace config: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "models.chat") || strings.Contains(text, "parallel_slots") || strings.Contains(text, "profile = \"26B\"") {
		t.Fatalf("workspace config materialized hub defaults:\n%s", text)
	}
	if !strings.Contains(text, "loaded_context_length = 131072") {
		t.Fatalf("expected detected context to persist, got:\n%s", text)
	}
}

func TestPersistWorkspaceConfigWritesOnlyLocalOverrides(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	cwd := t.TempDir()
	writeGlobal(t, globalconfig.GlobalConfig{
		Models: map[string]string{
			"chat": "hub-chat",
		},
	})

	cfg, err := LoadWithGlobal(cwd)
	if err != nil {
		t.Fatalf("LoadWithGlobal: %v", err)
	}
	InheritChatModelDefaults(&cfg)
	cfg.Models["chat"] = "workspace-chat"
	if err := PersistWorkspaceConfig(cwd, cfg); err != nil {
		t.Fatalf("PersistWorkspaceConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(cwd, ".forge", "config.toml"))
	if err != nil {
		t.Fatalf("read persisted workspace config: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "chat = 'workspace-chat'") {
		t.Fatalf("expected local chat override, got:\n%s", text)
	}
	if strings.Contains(text, "planner =") || strings.Contains(text, "parallel_slots") {
		t.Fatalf("unexpected extra overrides persisted:\n%s", text)
	}
}

func boolPtr(v bool) *bool       { return &v }
func stringPtr(v string) *string { return &v }
func intPtr(v int) *int          { return &v }
