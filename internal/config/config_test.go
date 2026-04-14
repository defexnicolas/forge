package config

import "testing"

func TestDefaultsUseRecommendedYarnProfile(t *testing.T) {
	cfg := Defaults()
	if cfg.Context.Yarn.Profile != "9B" {
		t.Fatalf("profile = %q, want 9B", cfg.Context.Yarn.Profile)
	}
	if cfg.Context.ModelContextTokens != 8192 {
		t.Fatalf("model_context_tokens = %d, want 8192", cfg.Context.ModelContextTokens)
	}
	if cfg.Context.BudgetTokens != 4500 {
		t.Fatalf("budget_tokens = %d, want 4500", cfg.Context.BudgetTokens)
	}
	if cfg.Context.ReserveOutputTokens != 2000 {
		t.Fatalf("reserve_output_tokens = %d, want 2000", cfg.Context.ReserveOutputTokens)
	}
	if cfg.Context.Yarn.MaxNodes != 8 || cfg.Context.Yarn.MaxFileBytes != 12000 || cfg.Context.Yarn.HistoryEvents != 12 {
		t.Fatalf("unexpected yarn defaults: %#v", cfg.Context.Yarn)
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
		{"2B", 2200, 1200, 4, 6000, 6},
		{"9B", 4500, 2000, 8, 12000, 12},
		{"26B", 10000, 3500, 18, 22000, 24},
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
