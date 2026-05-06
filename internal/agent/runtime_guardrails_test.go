package agent

import (
	"strings"
	"testing"

	"forge/internal/config"
)

func TestIsToolFailureSummary(t *testing.T) {
	cases := []struct {
		name    string
		summary string
		want    bool
	}{
		// Genuine failures the runtime wrappers produce.
		{"runtime appended 'failed: ...'", "edit_file failed: file not found", true},
		{"json parse error pattern", "error: unexpected character 'x'", true},
		{"policy denial", "denied by command policy: rm *", true},
		{"path not found suffix", "config.toml: not found", true},
		{"starts with 'error'", "error opening file", true},
		{"starts with 'failed'", "failed to spawn shell", true},

		// Legitimate output that previously tripped the guard.
		{"run_command clean", "npm test", false},
		{"run_command echoing 'error' string", "echo 'error in tests'", false},
		{"command containing 'failed' word", "grep failed_login auth.log", false},
		{"git diff result mentioning errors file", "git diff src/error_handler.go", false},
		{"search hits with 'not found' substring", "search 'phrase not found in body'", false},
		{"empty summary", "", false},
		{"benign muted output", "0 results", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isToolFailureSummary(tc.summary); got != tc.want {
				t.Errorf("isToolFailureSummary(%q) = %v, want %v", tc.summary, got, tc.want)
			}
		})
	}
}

func TestReadBudgetNudgeForMode(t *testing.T) {
	// The hard-stop replacement (soft-nudge) message must be mode-specific.
	// Explore mode is exempt from the guard at the call site, but the helper
	// is still tested for completeness — the default branch handles it.
	cases := []struct {
		mode             string
		mustContain      []string
		mustNotContain   []string
	}{
		{
			mode:        "build",
			mustContain: []string{"execute_task", "edit_file"},
		},
		{
			mode:           "plan",
			mustContain:    []string{"plan_write", "todo_write"},
			mustNotContain: []string{"execute_task"},
		},
		{
			mode:           "chat",
			mustContain:    []string{"Answer the user"},
			mustNotContain: []string{"execute_task", "plan_write", "todo_write"},
		},
		{
			mode:           "explore",
			mustNotContain: []string{"execute_task", "plan_write", "todo_write"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			got := readBudgetNudgeForMode(tc.mode, 12, 12)
			for _, s := range tc.mustContain {
				if !strings.Contains(got, s) {
					t.Errorf("nudge(%s) missing %q; got: %s", tc.mode, s, got)
				}
			}
			for _, s := range tc.mustNotContain {
				if strings.Contains(got, s) {
					t.Errorf("nudge(%s) must not contain %q; got: %s", tc.mode, s, got)
				}
			}
		})
	}
}

func TestActiveReadBudget(t *testing.T) {
	// Build mode uses the higher max_builder_read_loops (default 12); other
	// modes fall back to max_consecutive_read_only (default 6). The
	// per-session override (set by /reads extend) wins over both.
	cases := []struct {
		name     string
		mode     string
		override int
		cfg      config.RuntimeConfig
		want     int
	}{
		{name: "build default", mode: "build", want: 12},
		{name: "chat default", mode: "chat", want: 6},
		{name: "plan default", mode: "plan", want: 6},
		{name: "debug default (higher than build)", mode: "debug", want: 25},
		{name: "build config override", mode: "build", cfg: config.RuntimeConfig{MaxBuilderReadLoops: 20}, want: 20},
		{name: "debug config override", mode: "debug", cfg: config.RuntimeConfig{MaxDebugReadLoops: 50}, want: 50},
		{name: "debug override below floor 12 raises to 12", mode: "debug", cfg: config.RuntimeConfig{MaxDebugReadLoops: 4}, want: 12},
		{name: "session override beats config", mode: "build", override: 30, cfg: config.RuntimeConfig{MaxBuilderReadLoops: 20}, want: 30},
		{name: "session override negative disables", mode: "build", override: -1, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runtime{
				Mode:               tc.mode,
				readBudgetOverride: tc.override,
				Config:             config.Config{Runtime: tc.cfg},
			}
			if got := r.activeReadBudget(); got != tc.want {
				t.Errorf("activeReadBudget(mode=%s, override=%d, cfg=%+v) = %d, want %d",
					tc.mode, tc.override, tc.cfg, got, tc.want)
			}
		})
	}
}

func TestMaxReasoningTokens(t *testing.T) {
	// The thinking-budget guard caps reasoning_content tokens before any
	// text or tool_call is emitted. Default 6000 (≈4500 words). Negative
	// = disabled. Positive overrides.
	cases := []struct {
		name string
		cfg  config.RuntimeConfig
		want int
	}{
		{name: "default", want: 6000},
		{name: "positive override", cfg: config.RuntimeConfig{MaxReasoningTokens: 10000}, want: 10000},
		{name: "negative disables (returns 0)", cfg: config.RuntimeConfig{MaxReasoningTokens: -1}, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runtime{Config: config.Config{Runtime: tc.cfg}}
			if got := r.maxReasoningTokens(); got != tc.want {
				t.Errorf("maxReasoningTokens(cfg=%+v) = %d, want %d", tc.cfg, got, tc.want)
			}
		})
	}
}

func TestIsLikelyCodeLine(t *testing.T) {
	// The narration-loop guard skips code-shaped lines so a real file with
	// repeated code (3 useEffect blocks, identical struct field tags, etc.)
	// does not trigger a false-positive cancel. These cases pin the
	// heuristic to the exact failure the user reported and the obvious
	// adjacent shapes.
	cases := []struct {
		name string
		line string
		want bool
	}{
		// User's reported false-positive — a React component with 3 useEffects.
		{"useeffect open brace arrow", "useeffect(() => {", true},
		{"useeffect close brace", "}, []);", true},
		{"react jsx-ish closing", "});", true},

		// Adjacent code shapes that should also be exempt.
		{"function decl", "function handleClick() {", true},
		{"const arrow", "const handler = () => {", true},
		{"import statement", "import react from 'react';", true},
		{"export default", "export default function home() {", true},
		{"go func decl", "func (r *runtime) run() {", true},
		{"return stmt", "return null;", true},
		{"closing brace longer", "}     // close component", true},
		{"go struct tag", "name string //json field", false /* no code shape */},
		{"semicolon end", "dosomething();", true},

		// Legit narration — must NOT be exempt, otherwise the guard is useless.
		{"narration plain", "let me think about this for a second", false},
		{"narration plain 2", "ok i will read the file now and then edit", false},
		{"narration with mention of code", "i need to update the useeffect call", false},
		{"narration ending punct", "alright moving on to the next task.", false},
		{"narration colon end", "my plan now is the following:", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isLikelyCodeLine(tc.line)
			if got != tc.want {
				t.Errorf("isLikelyCodeLine(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

func TestExtendReadBudget(t *testing.T) {
	// /reads extend N starts from the active config value when there's no
	// override yet, then accumulates across calls.
	r := &Runtime{
		Mode: "build",
		Config: config.Config{Runtime: config.RuntimeConfig{
			MaxBuilderReadLoops: 12,
		}},
	}
	if got := r.ExtendReadBudget(10); got != 22 {
		t.Errorf("first extend +10 from default 12 = %d, want 22", got)
	}
	if got := r.ExtendReadBudget(5); got != 27 {
		t.Errorf("second extend +5 = %d, want 27", got)
	}
	r.SetReadBudgetOverride(0)
	if got := r.activeReadBudget(); got != 12 {
		t.Errorf("after reset, activeReadBudget = %d, want 12 (config default)", got)
	}
}
