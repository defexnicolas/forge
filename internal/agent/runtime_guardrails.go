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
	secs := r.Config.Runtime.RequestTimeoutSeconds
	if secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
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
