package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"forge/internal/llm"
	"forge/internal/tools"
)

type subagentRunError struct {
	Agent     string
	Kind      string
	Phase     string
	StepsUsed int
	TimedOut  bool
	Provider  string
	BaseURL   string
	Model     string
	ModelRole string
	Cause     error
}

func (e *subagentRunError) Error() string {
	if e == nil {
		return ""
	}
	agent := strings.TrimSpace(e.Agent)
	if agent == "" {
		agent = "subagent"
	}
	msg := fmt.Sprintf("%s failed", agent)
	if e.Kind != "" {
		msg += ": " + e.Kind
	}
	if e.Phase != "" {
		msg += " during " + e.Phase
	}
	if e.StepsUsed > 0 {
		msg += fmt.Sprintf(" after %d step(s)", e.StepsUsed)
	}
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

func (e *subagentRunError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type executeTaskFailureMeta struct {
	TaskID      string `json:"task_id"`
	FailureKind string `json:"failure_kind"`
	Phase       string `json:"last_known_phase,omitempty"`
	StepsUsed   int    `json:"steps_used,omitempty"`
	TimedOut    bool   `json:"timed_out,omitempty"`
	Provider    string `json:"provider,omitempty"`
	BaseURL     string `json:"base_url,omitempty"`
	Model       string `json:"model,omitempty"`
	ModelRole   string `json:"model_role,omitempty"`
	Cause       string `json:"cause,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

func (r *Runtime) requestTimeout() time.Duration {
	// Local backends (LM Studio, llama-server) routinely take 60+ seconds
	// just to PROCESS a long prompt before emitting the first token —
	// 96k tokens of context on a 35B model on commodity hardware is
	// minutes of pre-fill, not seconds. The default 45s wall-clock
	// timeout was tuned for cloud APIs and turns long-context local
	// turns into "context deadline exceeded" failures. Disable the
	// wall-clock for local backends and rely on the idle-timeout
	// watchdog (which only arms after the first SSE chunk) to catch
	// genuinely-hung requests.
	if r.isLocalBackend() {
		return 0
	}
	secs := r.Config.Runtime.RequestTimeoutSeconds
	if secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// applySamplingDefaults populates a ChatRequest's sampling fields from the
// active config. Pinning these explicitly per-request is the only way to
// get reproducible output across LM Studio sessions: the UI preset
// (Default / Deterministic / Creative / Balanced) silently rewires the
// server-side defaults between mounts, so a model that was deterministic
// before a UI preset change suddenly drifts. Forge sends temperature /
// top_p / top_k / min_p / presence_penalty / repeat_penalty on every
// chat completion, overriding whatever preset is active.
//
// If the caller already pinned Temperature (e.g. Claw subroutines that
// want a deliberately-different value), the existing override is kept.
// The other five fields are overridden unconditionally — no current
// caller sets them, so there's no collision risk, and adding a "let
// caller win" branch per field would balloon the call sites.
func (r *Runtime) applySamplingDefaults(req *llm.ChatRequest) {
	if r == nil || req == nil {
		return
	}
	s := r.Config.Sampling
	temp := s.Temperature
	topP := s.TopP
	topK := s.TopK
	minP := s.MinP
	presence := s.PresencePenalty
	repeat := s.RepeatPenalty
	if req.Temperature == nil {
		req.Temperature = &temp
	}
	req.TopP = &topP
	req.TopK = &topK
	req.MinP = &minP
	req.PresencePenalty = &presence
	req.RepeatPenalty = &repeat
}

// isLocalBackend reports whether the active provider talks to a local model
// server (LM Studio or llama-server). The check uses BackendNamer so it
// stays accurate even when the registry slot is named "lmstudio" but the
// resolved backend is actually llama-server, or vice versa.
func (r *Runtime) isLocalBackend() bool {
	if r == nil || r.Providers == nil {
		return false
	}
	name := strings.TrimSpace(r.Config.Providers.Default.Name)
	if name == "" {
		return false
	}
	provider, ok := r.Providers.Get(name)
	if !ok {
		return false
	}
	bn, ok := provider.(llm.BackendNamer)
	if !ok {
		return false
	}
	switch bn.BackendName() {
	case "lmstudio", "llama-server":
		return true
	default:
		return false
	}
}

func (r *Runtime) requestIdleTimeout() time.Duration {
	secs := r.Config.Runtime.RequestIdleTimeoutSeconds
	if secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// streamProvider invokes provider.Stream, opting into the idle-timeout
// watchdog when the provider implements StreamWithIdle. Centralising this
// keeps the type assertion off every LLM call site.
func (r *Runtime) streamProvider(ctx context.Context, provider llm.Provider, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if idle := r.requestIdleTimeout(); idle > 0 {
		if p, ok := provider.(interface {
			StreamWithIdle(context.Context, llm.ChatRequest, time.Duration) (<-chan llm.ChatEvent, error)
		}); ok {
			return p.StreamWithIdle(ctx, req, idle)
		}
	}
	return provider.Stream(ctx, req)
}

func (r *Runtime) subagentTimeout() time.Duration {
	secs := r.Config.Runtime.SubagentTimeoutSeconds
	if secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

func (r *Runtime) taskTimeout() time.Duration {
	secs := r.Config.Runtime.TaskTimeoutSeconds
	if secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

func (r *Runtime) maxNoProgressSteps() int {
	if v := r.Config.Runtime.MaxNoProgressSteps; v > 0 {
		return v
	}
	return 3
}

func (r *Runtime) maxEmptyResponses() int {
	if v := r.Config.Runtime.MaxEmptyResponses; v > 0 {
		return v
	}
	return 2
}

func (r *Runtime) maxSameToolFailures() int {
	if v := r.Config.Runtime.MaxSameToolFailures; v > 0 {
		return v
	}
	return 2
}

func (r *Runtime) maxConsecutiveReadOnly() int {
	v := r.Config.Runtime.MaxConsecutiveReadOnly
	if v < 0 {
		return math.MaxInt32
	}
	if v > 0 {
		return v
	}
	return 6
}

func (r *Runtime) maxPlannerSummarySteps() int {
	if v := r.Config.Runtime.MaxPlannerSummarySteps; v > 0 {
		return v
	}
	return 2
}

func (r *Runtime) maxBuilderReadLoops() int {
	v := r.Config.Runtime.MaxBuilderReadLoops
	// Negative value = explicitly opt out of the guard. Useful when the
	// user knows their model needs to read deeply (large codebase
	// exploration) and would rather the runtime never preempt with a
	// "too many reads" error. Caller still hits MaxSteps eventually.
	if v < 0 {
		return math.MaxInt32
	}
	if v > 0 {
		// Floor at 8 — anything lower fires before the agent finishes
		// reading the relevant files for even a small task.
		if v < 8 {
			return 8
		}
		return v
	}
	return 12
}

// activeReadBudget returns the threshold of consecutive read-only tool calls
// that fires the soft-nudge / hard-stop guard for the CURRENT mode.
//   - build:  maxBuilderReadLoops (default 12) — multi-task workflow
//   - debug:  maxDebugReadLoops (default 25) — hypothesis-test cycles are
//             read-heavy by design (read → instrument → read → run);
//             the regular 10 cap was tripping mid-investigation
//   - plan / chat / others: maxConsecutiveReadOnly (default 6, config 10)
//   - explore: exempt — caller short-circuits before reaching here
//
// Returns 0 to mean "guard disabled" — used when the per-session
// override is negative.
func (r *Runtime) activeReadBudget() int {
	if r == nil {
		return 0
	}
	if r.readBudgetOverride < 0 {
		return 0
	}
	if r.readBudgetOverride > 0 {
		return r.readBudgetOverride
	}
	switch r.Mode {
	case "build":
		return r.maxBuilderReadLoops()
	case "debug":
		return r.maxDebugReadLoops()
	}
	return r.maxConsecutiveReadOnly()
}

// maxReasoningTokens caps the reasoning_content tokens a model can
// emit BEFORE producing text or a tool call. When exceeded, the
// streaming guard cancels and reprompts. Targets the "100k tokens of
// flip-flop reasoning, no tool call" failure mode common with
// reasoning-heavy local models on debug-style tasks.
//
// Returns 0 to mean "guard disabled" — used when the config value is
// negative. Default 6000 ≈ 4500 words ≈ 6 dense paragraphs of thought,
// enough for one focused chain but not for endless speculation.
//
// Debug mode is hard-walled to MaxReasoningTokensDebug (default 3500)
// regardless of the global MaxReasoningTokens. Reasoning: the
// hypothesis-test loop should produce a one-sentence theory and an
// instrument edit, not multi-paragraph speculation, and the
// carry-forward synthesizer (see internal/session.contextEvents) keeps
// the next turn from re-deriving what the aborted turn already
// discovered. The global cap (commonly tuned for build/plan where the
// model legitimately needs to plan a refactor) does NOT override the
// debug cap — set MaxReasoningTokensDebug explicitly to opt out.
func (r *Runtime) maxReasoningTokens() int {
	if r.Mode == "debug" {
		v := r.Config.Runtime.MaxReasoningTokensDebug
		if v < 0 {
			return 0
		}
		if v > 0 {
			return v
		}
		return 3500
	}
	v := r.Config.Runtime.MaxReasoningTokens
	if v < 0 {
		return 0
	}
	if v > 0 {
		return v
	}
	return 6000
}

// maxDebugReadLoops is the cap on consecutive read-only tool calls in
// debug mode before the soft-nudge fires. Higher than build/plan
// because hypothesis-test loops legitimately read (file → run → file
// → log → file) without "making progress" by the original guard's
// definition. 25 covers ~3 hypothesis cycles before the model is
// nudged to instrument or run instead of reading more.
//
// Config knob: runtime.max_debug_read_loops. 0 = use default 25.
// Negative = disable the guard for debug mode entirely (rely on
// max_steps as the only cap).
func (r *Runtime) maxDebugReadLoops() int {
	v := r.Config.Runtime.MaxDebugReadLoops
	if v < 0 {
		return math.MaxInt32
	}
	if v > 0 {
		// Floor at 12 — anything below the build cap defeats the
		// purpose of having a separate knob.
		if v < 12 {
			return 12
		}
		return v
	}
	return 25
}

// SetReadBudgetOverride installs a per-session override for the read-only
// budget guard. Used by the /reads slash command so the user can extend the
// budget without editing .forge/config.toml. Pass 0 to clear (fall back to
// config), a positive value to set the threshold, or a negative value to
// disable the guard for the session.
func (r *Runtime) SetReadBudgetOverride(v int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readBudgetOverride = v
}

// ExtendReadBudget bumps the read-only budget by delta for this session and
// returns the new effective threshold. If there's no override yet, it starts
// from the current configured value for the active mode (build vs other).
func (r *Runtime) ExtendReadBudget(delta int) int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	base := r.readBudgetOverride
	if base <= 0 {
		// No override yet — start from the active config value so the bump
		// is relative to what the user is actually living with.
		if r.Mode == "build" {
			base = r.maxBuilderReadLoops()
		} else {
			base = r.maxConsecutiveReadOnly()
		}
	}
	r.readBudgetOverride = base + delta
	return r.readBudgetOverride
}

// recordReadBudgetSnapshot caches the most recent (consumed, threshold) pair
// so the /reads slash command can display it between turns. Called from the
// main turn loop right before each EventReadBudget emission.
func (r *Runtime) recordReadBudgetSnapshot(consumed, threshold int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.lastReadBudgetConsumed = consumed
	r.lastReadBudgetThreshold = threshold
	r.mu.Unlock()
}

// LastReadBudgetSnapshot returns the most recent (consumed, threshold) pair
// observed by the runtime. Used by the /reads status command to show the
// current state without having to peek at the active turn.
func (r *Runtime) LastReadBudgetSnapshot() (int, int) {
	if r == nil {
		return 0, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastReadBudgetConsumed, r.lastReadBudgetThreshold
}

// readBudgetGracePastNudge is the number of additional read-only steps the
// model is allowed AFTER the soft nudge before the hard stop fires. Gives
// the model a fair window to self-correct (write the edit, dispatch
// execute_task, answer in prose) without being killed mid-thought.
func (r *Runtime) readBudgetGracePastNudge() int {
	if v := r.Config.Runtime.ReadBudgetGracePastNudge; v > 0 {
		return v
	}
	return 3
}

// readBudgetEarlyNudgeForDebug fires at ~60% of the debug read budget.
// Goal: give the model a clear signal to switch from reading to
// instrumenting BEFORE it spends its remaining reads, instead of getting
// the only nudge once it's already at the threshold. Debug-only — other
// modes don't have a hypothesis-test cycle and a 60% pre-warning would
// just add noise to plan/build/chat.
func readBudgetEarlyNudgeForDebug(consumed, threshold int) string {
	return fmt.Sprintf("You are at %d/%d reads — about %.0f%% of the debug budget. Stop reading and instrument now: add a print/log at the suspected site and call run_command. If you genuinely need more reading first, delegate the breadth-first part to spawn_subagent('explorer', ...) instead of burning your inline budget.", consumed, threshold, debugEarlyNudgeFraction*100)
}

// readBudgetNudgeForMode returns the per-mode soft-nudge text injected into
// the next observation when the model crosses the read threshold for the
// first time in a turn. The message is mode-specific so the model gets a
// concrete next action — telling a chat-mode response to "dispatch
// execute_task" only confuses it.
func readBudgetNudgeForMode(mode string, consumed, threshold int) string {
	switch mode {
	case "build":
		return fmt.Sprintf("You have made %d consecutive read-only tool calls (budget=%d) without editing. Decide now: dispatch execute_task on the next checklist task, or call edit_file / write_file / apply_patch with the change you already have in mind. If you genuinely need more reading, the next tool call must be a CONCRETE read with a clear hypothesis (not an open-ended scan) — otherwise the next read will hard-stop the turn.", consumed, threshold)
	case "plan":
		return fmt.Sprintf("You have made %d consecutive read-only tool calls (budget=%d). It is time to commit the design — call plan_write to save the plan, then todo_write to write the executable checklist. If you need more information from the user, call ask_user. Do not read another file unless it is the single specific file the plan requires.", consumed, threshold)
	case "chat":
		return fmt.Sprintf("You have made %d consecutive read-only tool calls (budget=%d) without making progress. Answer the user directly with what you have now. If you cannot answer, say so explicitly — do not keep reading.", consumed, threshold)
	default:
		return fmt.Sprintf("You have made %d consecutive read-only tool calls (budget=%d) without making progress. Stop reading and produce a final answer or a concrete mutating action.", consumed, threshold)
	}
}

// readBudgetHardStopForMode returns the trailing text appended to the final
// EventError message when the model exhausts the post-nudge grace window.
// Mirrors readBudgetNudgeForMode but in past-tense "you ignored the nudge".
func readBudgetHardStopForMode(mode string) string {
	switch mode {
	case "build":
		return "you ignored the soft nudge and kept reading. End the turn and choose the next task action (execute_task / edit_file / write_file) before continuing."
	case "plan":
		return "you ignored the soft nudge and kept reading. End the turn by calling plan_write + todo_write or by asking the user a focused question with ask_user."
	case "chat":
		return "you ignored the soft nudge and kept reading. Answer the user directly with what you have now."
	default:
		return "you ignored the soft nudge and kept reading. Stop and produce a final answer."
	}
}

func (r *Runtime) retryOnProviderTimeout() bool {
	return r.Config.Runtime.RetryOnProviderTimeout
}

// autoApproveMode reports whether the configured approval_profile bypasses
// the interactive prompt for file mutations and run_command. "auto" and
// "yolo" both opt in. Anything else (including the default "normal" and
// the safe "safe" profiles) keeps the prompt-on-each-mutation behaviour.
func (r *Runtime) autoApproveMode() bool {
	p := strings.ToLower(strings.TrimSpace(r.Config.ApprovalProfile))
	return p == "auto" || p == "yolo"
}

func withOptionalTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// summarizeForReprompt quotes a short snippet of a string for inclusion in
// a user reprompt message. Caps the length so a 100-char repeated line
// doesn't bloat every subsequent prompt, and JSON-quotes it so the
// model sees an obvious "this is what you said" rather than
// free-floating prose that could confuse a parser.
func summarizeForReprompt(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return `""`
	}
	const limit = 80
	if len([]rune(s)) > limit {
		runes := []rune(s)
		s = string(runes[:limit]) + "…"
	}
	return fmt.Sprintf("%q", s)
}

func classifyProviderFailure(err error) string {
	switch {
	case llm.IsProviderTimeout(err):
		return "timeout"
	case llm.IsProviderUnavailable(err):
		return "provider_down"
	default:
		return "tool_failure"
	}
}

// isToolFailureSummary detects whether a tool result summary represents a
// genuine tool failure that should count toward the loop-breaker guard.
//
// The previous implementation matched the substrings "error", "failed",
// "denied" and "not found" anywhere in the (lower-cased) summary. That
// triggered on legitimate tool output — for instance `run_command`
// summaries like "npm test failed: exit status 1" (a real test result the
// model needs to see, NOT a tool failure), or a search summary mentioning
// "no matches found" — and stopped real-world sessions on the second
// matching call. We now require a stronger signal: the failure indicator
// must appear with a colon (the canonical "{tool} failed: {err}" /
// "error: {err}" / "denied by {policy}" / "{path}: not found" pattern
// produced by the runtime's tool wrappers) or anchor at the start of the
// summary. Substrings inside command names or normal output no longer
// trip the guard.
func isToolFailureSummary(summary string) bool {
	s := strings.ToLower(strings.TrimSpace(summary))
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "error") || strings.HasPrefix(s, "failed") {
		return true
	}
	return strings.Contains(s, "failed: ") ||
		strings.Contains(s, "error: ") ||
		strings.Contains(s, "denied by ") ||
		strings.Contains(s, "not found: ") ||
		strings.HasSuffix(s, ": not found")
}

func executeTaskIDFromInput(input json.RawMessage) string {
	var req struct {
		TaskID string `json:"task_id"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return ""
	}
	if taskID := strings.TrimSpace(req.TaskID); taskID != "" {
		return taskID
	}
	return strings.TrimSpace(req.ID)
}

func buildExecuteTaskFailureResult(taskID string, runErr *subagentRunError) tools.Result {
	meta := executeTaskFailureMeta{TaskID: taskID}
	summary := fmt.Sprintf("builder failed task %s", taskID)
	if runErr != nil {
		meta.FailureKind = runErr.Kind
		meta.Phase = runErr.Phase
		meta.StepsUsed = runErr.StepsUsed
		meta.TimedOut = runErr.TimedOut
		if runErr.Cause != nil {
			meta.Cause = strings.TrimSpace(runErr.Cause.Error())
		}
		meta.Provider = strings.TrimSpace(runErr.Provider)
		meta.BaseURL = strings.TrimSpace(runErr.BaseURL)
		meta.Model = strings.TrimSpace(runErr.Model)
		meta.ModelRole = strings.TrimSpace(runErr.ModelRole)
		summary = summarizeExecuteTaskFailure(taskID, meta)
	}
	meta.Summary = summary
	payload, _ := json.Marshal(meta)
	return tools.Result{
		Title:   "execute_task",
		Summary: summary,
		Content: []tools.ContentBlock{{Type: "json", Text: string(payload)}},
	}
}

func parseExecuteTaskFailureMeta(result *tools.Result) (executeTaskFailureMeta, bool) {
	if result == nil {
		return executeTaskFailureMeta{}, false
	}
	for _, block := range result.Content {
		if block.Type != "json" || strings.TrimSpace(block.Text) == "" {
			continue
		}
		var meta executeTaskFailureMeta
		if err := json.Unmarshal([]byte(block.Text), &meta); err == nil && strings.TrimSpace(meta.TaskID) != "" && strings.TrimSpace(meta.FailureKind) != "" {
			return meta, true
		}
	}
	return executeTaskFailureMeta{}, false
}

func summarizeExecuteTaskFailure(taskID string, meta executeTaskFailureMeta) string {
	var b strings.Builder
	fmt.Fprintf(&b, "builder failed task %s", taskID)
	switch meta.FailureKind {
	case "timeout":
		b.WriteString(": timeout while waiting for provider response")
	case "provider_down":
		b.WriteString(": provider unavailable")
	case "":
	default:
		b.WriteString(": ")
		b.WriteString(meta.FailureKind)
	}
	if meta.Phase != "" {
		b.WriteString(" during ")
		b.WriteString(meta.Phase)
	}
	if meta.Provider != "" {
		b.WriteString(" from ")
		b.WriteString(meta.Provider)
	}
	if meta.BaseURL != "" {
		b.WriteString(" at ")
		b.WriteString(meta.BaseURL)
	}
	if meta.Model != "" {
		b.WriteString(" using model ")
		b.WriteString(meta.Model)
		if meta.ModelRole != "" {
			b.WriteString(" (role ")
			b.WriteString(meta.ModelRole)
			b.WriteString(")")
		}
	}
	if meta.StepsUsed > 0 {
		fmt.Fprintf(&b, " after %d step(s)", meta.StepsUsed)
	}
	if meta.Cause != "" {
		b.WriteString(": ")
		b.WriteString(meta.Cause)
	}
	return b.String()
}

func formatExecuteTaskRetryError(meta executeTaskFailureMeta) string {
	if strings.TrimSpace(meta.TaskID) == "" {
		return "execute_task failed repeatedly"
	}
	summary := strings.TrimSpace(meta.Summary)
	if summary == "" {
		summary = summarizeExecuteTaskFailure(meta.TaskID, meta)
	}
	return summary
}

func (r *Runtime) providerBaseURL(providerName string) string {
	if r == nil {
		return ""
	}
	switch strings.TrimSpace(providerName) {
	case "lmstudio":
		return strings.TrimSpace(r.Config.Providers.LMStudio.BaseURL)
	case "openai_compatible":
		return strings.TrimSpace(r.Config.Providers.OpenAICompatible.BaseURL)
	default:
		return ""
	}
}

func builderPhaseForTool(name string) string {
	switch name {
	case "read_file", "list_files", "search_text", "search_files", "git_status", "git_diff", "task_get":
		return "reading"
	case "edit_file", "write_file", "apply_patch":
		return "editing"
	case "run_command":
		return "verifying"
	case "task_update":
		return "completing"
	default:
		return ""
	}
}
