package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"forge/internal/config"
	contextbuilder "forge/internal/context"
	"forge/internal/hooks"
	"forge/internal/llm"
	"forge/internal/patch"
	"forge/internal/permissions"
	"forge/internal/plans"
	"forge/internal/tasks"
	"forge/internal/tools"
)

const (
	EventAssistantText    = "assistant_text"
	EventAssistantDelta   = "assistant_delta"
	EventModelProgress    = "model_progress"
	EventClearStreaming   = "clear_streaming"
	EventToolCall         = "tool_call"
	EventToolResult       = "tool_result"
	EventApproval         = "approval_required"
	EventAskUser          = "ask_user"
	EventSubagentProgress = "subagent_progress"
	EventError            = "error"
	EventDone             = "done"
)

// SubagentProgress reports the lifecycle of one task within a spawn_subagents
// batch. The TUI keys on (BatchID, Index) to update the corresponding lane
// in the multi-lane view. Status values: "pending", "running", "completed",
// "error".
type SubagentProgress struct {
	BatchID string
	Index   int
	Total   int
	Agent   string
	Status  string
	Summary string
	Error   string
}

type Event struct {
	Type             string
	Text             string
	ToolName         string
	Input            json.RawMessage
	Result           *tools.Result
	Approval         *ApprovalRequest
	AskUser          *AskUserRequest
	Progress         *ModelProgress
	SubagentProgress *SubagentProgress
	Error            error
	// Side marks events emitted by a parallel `/btw` call. TUI renders these
	// muted with a [btw] prefix and they do not participate in the tool loop.
	Side bool
}

type ModelProgress struct {
	Phase           string
	Model           string
	Step            int
	InputTokens     int
	OutputTokens    int
	TotalTokens     int
	TokensPerSecond float64
	Elapsed         time.Duration
	Done            bool
}

type ApprovalRequest struct {
	ID       string
	ToolName string
	Input    json.RawMessage
	Summary  string
	Diff     string
	Response chan ApprovalResponse
	plan     patch.Plan
	command  *ToolCall
}

type ApprovalResponse struct {
	Approved bool
}

type AskUserRequest struct {
	Question  string
	Questions []string // optional — full list when the model sent multiple
	Index     int      // 0-based position of Question within Questions
	Total     int      // total number of questions in the batch
	// Options, when non-empty, are model-suggested canned answers. The TUI
	// renders them as selectable rows above a "Type my own answer" row so
	// the user can pick a suggestion with the arrow keys.
	Options  []string
	Response chan string
}

type UndoEntry struct {
	Summary   string
	Snapshots []patch.Snapshot
}

type Runtime struct {
	CWD       string
	Config    config.Config
	Tools     *tools.Registry
	Providers *llm.Registry
	Builder   *contextbuilder.Builder
	MaxSteps  int
	Mode      string
	// ModeSwitchedFrom, when non-empty, is the previous mode just before a
	// SetMode. Consumed by the next turn to emit a one-shot handoff message.
	ModeSwitchedFrom string
	// PendingExplorerContext is a one-turn handoff from the read-only explorer
	// subagent into plan mode. It is consumed by the next plan-mode run.
	PendingExplorerContext string
	// PendingBuildPreflight is a one-turn handoff from automatic read-only
	// subagents into build mode. It is consumed by the next build-mode run.
	PendingBuildPreflight string
	// PendingExplorePreflight mirrors PendingBuildPreflight but for the
	// explore-mode opt-in fan-out. Consumed at the top of the next
	// explore-mode run and injected into the tier-C handoff block so the
	// main explorer response is grounded in the preflight findings.
	PendingExplorePreflight string
	Policy                  SprintPolicy
	Commands                permissions.CommandPolicy
	Plans                   *plans.Store
	Tasks                   *tasks.Store
	Subagents               SubagentRegistry
	Hooks                   *hooks.Runner
	Parsers                 *ParserRegistry
	MaxParseRetries         int
	LastTokensUsed          int
	LastTokensBudget        int
	LastModelUsed           string
	LastParserUsed          string
	// ActiveParserName is the parser selected at model-load time (via
	// SetChatModel). Cached so the TUI can display it without re-running
	// ForModel every frame. The per-turn LastParserUsed still tracks which
	// parser actually handled the most recent response.
	ActiveParserName   string
	ActiveModelFamily  string
	currentLoadedModel string
	loadedModels       map[string]bool
	// startupReloadDone is set once after the first provider.LoadModel call
	// issued by this runtime. Until it is true, EnsureRoleModelLoaded bypasses
	// the "already loaded" short-circuit and calls LoadModel anyway — that
	// path is the only one that applies ModelLoading.ParallelSlots to the
	// backend. Without this, a model that was already resident in LM Studio
	// (from a prior session or a manual load) would stay on whatever slot
	// count LM Studio picked, typically 1.
	startupReloadDone    bool
	LastTurnDuration     time.Duration
	LastTurnTokensIn     int
	LastTurnTokensOut    int
	LastTurnTokensPerSec float64
	mu                   sync.Mutex
	undoStack            []UndoEntry
	// systemPromptCache memoizes the rendered system prompt by (nativeTools |
	// mode | policy.AllowedNames | policy.AskNames). The body is dynamic in
	// content but byte-stable across consecutive turns while the policy and
	// mode do not change — caching guarantees the stable prefix needed for
	// LM Studio's KV cache to hit turn over turn. Invalidated in SetMode.
	systemPromptCache map[string]string
	// preflightCache memoizes preflight subagent batch results by (mode|line)
	// with a short TTL. Lets consecutive refinements of the same request
	// skip re-dispatching the fan-out. Invalidated on any successful
	// mutating tool call (edit_file / write_file / apply_patch) since a
	// cached analysis of pre-mutation state no longer applies.
	preflightCache map[string]preflightCacheEntry
	// loadMu serializes actual provider.LoadModel calls. Without this, two
	// concurrent subagents with different role models race to swap the
	// currently-loaded model on LM Studio, causing thrash and starving the
	// real turn. Held only around the LoadModel call itself — not inference.
	loadMu sync.Mutex
	// EventTee, if set, receives a copy of every event emitted by Run. Used
	// by /remote-control to broadcast to connected web clients.
	EventTee EventTee
	// activeEvents is the events channel of the in-flight turn. Set at the
	// top of run() and cleared at the end. Subagents invoked during the turn
	// (e.g. the builder via execute_task) read it so they can raise approval
	// prompts for their own mutating tool calls. Nil when no turn is active.
	activeEvents chan<- Event
}

type preflightCacheEntry struct {
	Value     string
	ExpiresAt time.Time
}

const preflightCacheTTL = 10 * time.Minute

func NewRuntime(cwd string, cfg config.Config, registry *tools.Registry, providers *llm.Registry) *Runtime {
	return &Runtime{
		CWD:       cwd,
		Config:    cfg,
		Tools:     registry,
		Providers: providers,
		Builder:   contextbuilder.NewBuilder(cwd, cfg, registry),
		MaxSteps:  40,
		Mode:      "plan",
		Policy:    NewPlanPolicy(),
		Commands:  permissions.DefaultCommandPolicy(),
		Plans:     plans.New(cwd),
		Tasks:     tasks.New(cwd),
		Subagents: DefaultSubagents(),
		Parsers:   DefaultParsers(),
	}
}

// SetChatModel updates the active chat model and caches the parser that
// will be used to decode its tool calls. Callers (e.g. the /model form and
// the /model slash command) should use this instead of mutating
// Config.Models["chat"] directly so the resolved parser stays in sync.
func (r *Runtime) SetChatModel(modelID string) {
	r.SetRoleModel("chat", modelID)
}

func (r *Runtime) SetRoleModel(role, modelID string) {
	if r == nil {
		return
	}
	if r.Config.Models == nil {
		r.Config.Models = map[string]string{}
	}
	if role == "" {
		role = "chat"
	}
	r.Config.Models[role] = modelID
	if role == "chat" {
		if r.Parsers != nil && modelID != "" {
			r.ActiveParserName = r.Parsers.ForModel(modelID).Name()
		} else {
			r.ActiveParserName = ""
		}
		r.ActiveModelFamily = DetectModelFamily(modelID)
	}
}

func (r *Runtime) MarkModelLoaded(modelID string) {
	if r == nil || modelID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loadedModels == nil {
		r.loadedModels = map[string]bool{}
	}
	r.loadedModels[modelID] = true
	r.currentLoadedModel = modelID
	// If something upstream (test fixture, manual CLI hint) has explicitly
	// marked a model as loaded, treat the startup-reload obligation as
	// satisfied. Otherwise the next EnsureRoleModelLoaded would ignore the
	// mark and force a redundant provider.LoadModel call.
	r.startupReloadDone = true
}

func (r *Runtime) roleModel(role string) string {
	if r == nil {
		return ""
	}
	if role == "" {
		role = "chat"
	}
	if model := strings.TrimSpace(r.Config.Models[role]); model != "" {
		return model
	}
	if role == "explorer" {
		if model := strings.TrimSpace(r.Config.Models["planner"]); model != "" {
			return model
		}
	}
	return strings.TrimSpace(r.Config.Models["chat"])
}

func (r *Runtime) modelRoleForMode() string {
	if r == nil || !r.Config.ModelLoading.Enabled {
		return "chat"
	}
	switch r.Mode {
	case "plan":
		return "planner"
	case "explore":
		return "explorer"
	default:
		return "chat"
	}
}

func (r *Runtime) configForRole(role string) config.Config {
	return config.ConfigForModelRole(r.Config, role, r.roleModel(role))
}

func (r *Runtime) buildSnapshot(userMessage string, cfg config.Config) contextbuilder.Snapshot {
	if r == nil || r.Builder == nil {
		return contextbuilder.Snapshot{}
	}
	builder := *r.Builder
	builder.Config = cfg
	return builder.Build(userMessage)
}

func (r *Runtime) buildTaskSnapshot(userMessage, role string) contextbuilder.Snapshot {
	if r == nil {
		return contextbuilder.Snapshot{}
	}
	return r.buildSnapshot(userMessage, config.ConfigForTaskRole(r.Config, role, r.roleModel(role)))
}

func (r *Runtime) SharedTaskContext(userMessage string) string {
	if r == nil {
		return ""
	}
	snapshot := r.buildTaskSnapshot(userMessage, "explorer")
	return snapshot.Render()
}

func (r *Runtime) EnsureRoleModelLoaded(ctx context.Context, provider llm.Provider, role string) error {
	if r == nil || provider == nil {
		return nil
	}
	// Two independent concerns converge on LoadModel:
	//   1. ModelLoading.Enabled=true: the user wants forge to own model
	//      loading (including per-role model swaps).
	//   2. ParallelSlots > 1 and this is our first turn: even without (1),
	//      we still need to apply GEN slots once so LM Studio actually
	//      serves concurrent subagent requests instead of queueing them.
	// Bail out only when both conditions are false.
	if !r.Config.ModelLoading.Enabled {
		if r.Config.ModelLoading.ParallelSlots <= 1 {
			return nil
		}
		r.mu.Lock()
		done := r.startupReloadDone
		r.mu.Unlock()
		if done {
			return nil
		}
	}
	modelID := r.roleModel(role)
	if modelID == "" {
		return nil
	}
	strategy := strings.ToLower(strings.TrimSpace(r.Config.ModelLoading.Strategy))
	if strategy == "" {
		strategy = "single"
	}
	r.mu.Lock()
	startupDone := r.startupReloadDone
	if startupDone {
		if strategy == "parallel" {
			if r.loadedModels != nil && r.loadedModels[modelID] {
				r.mu.Unlock()
				return nil
			}
		} else if r.currentLoadedModel == modelID {
			r.mu.Unlock()
			return nil
		}
	}
	r.mu.Unlock()

	contextLength := r.Config.Context.ModelContextTokens
	if detected := config.DetectedForRole(r.Config, role, modelID); detected != nil && detected.LoadedContextLength > 0 {
		contextLength = detected.LoadedContextLength
	}
	if contextLength <= 0 {
		contextLength = 16384
	}
	// Serialize actual load calls so concurrent subagents can't trigger a
	// model-swap storm on single-slot backends (LM Studio). Re-check the
	// loaded state after taking the lock: a sibling may have just loaded the
	// model we need.
	r.loadMu.Lock()
	defer r.loadMu.Unlock()
	r.mu.Lock()
	startupDone = r.startupReloadDone
	if startupDone {
		if strategy == "parallel" {
			if r.loadedModels != nil && r.loadedModels[modelID] {
				r.mu.Unlock()
				return nil
			}
		} else if r.currentLoadedModel == modelID {
			r.mu.Unlock()
			return nil
		}
	}
	r.mu.Unlock()
	loadCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	loadErr := provider.LoadModel(loadCtx, modelID, llm.LoadConfig{
		ContextLength:  contextLength,
		FlashAttention: true,
		ParallelSlots:  r.Config.ModelLoading.ParallelSlots,
	})
	// Mark the startup reload as done regardless of success — the one-time
	// forced path has been exercised and subsequent EnsureRoleModelLoaded
	// calls can short-circuit on the loaded-model maps again.
	r.mu.Lock()
	r.startupReloadDone = true
	r.mu.Unlock()
	if loadErr != nil {
		// Best-effort load: LM Studio may already have the model resident
		// (e.g. JIT cache or a prior session) and still reject an explicit
		// load request. Emit a warning via stderr and let the subsequent
		// Chat/Stream call be the real source of truth — if the model truly
		// isn't available, that call will fail with an actionable error.
		fmt.Fprintf(os.Stderr, "model-load warning (%s=%s): %v — proceeding anyway\n", role, modelID, loadErr)
		r.MarkModelLoaded(modelID)
		return nil
	}
	r.MarkModelLoaded(modelID)
	return nil
}

// ReloadCurrentModel forces a provider-side load of the model for the current
// mode, applying the configured context length and parallel generation slots
// even if the runtime already marked that model as loaded.
func (r *Runtime) ReloadCurrentModel(ctx context.Context) (string, error) {
	if r == nil {
		return "", fmt.Errorf("runtime unavailable")
	}
	provider, _, err := r.resolveProvider()
	if err != nil {
		return "", err
	}
	role := r.modelRoleForMode()
	modelID := r.roleModel(role)
	if modelID == "" {
		return "", fmt.Errorf("model id is required")
	}
	contextLength := r.Config.Context.ModelContextTokens
	if detected := config.DetectedForRole(r.Config, role, modelID); detected != nil && detected.LoadedContextLength > 0 {
		contextLength = detected.LoadedContextLength
	}
	if contextLength <= 0 {
		contextLength = 16384
	}
	r.loadMu.Lock()
	defer r.loadMu.Unlock()
	loadCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	if err := provider.LoadModel(loadCtx, modelID, llm.LoadConfig{
		ContextLength:  contextLength,
		FlashAttention: true,
		ParallelSlots:  r.Config.ModelLoading.ParallelSlots,
	}); err != nil {
		return modelID, err
	}
	r.mu.Lock()
	r.startupReloadDone = true
	r.mu.Unlock()
	r.MarkModelLoaded(modelID)
	return modelID, nil
}

// Close releases resources owned by the runtime (currently the tasks DB).
// Main entrypoints should defer this so SQLite files get unlocked cleanly.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	var errs []string
	if r.Plans != nil {
		if err := r.Plans.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if r.Tasks != nil {
		if err := r.Tasks.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// SetMode switches the agent to a different operating mode.
//
// The historical "build" mode has been removed — execution is now delegated
// from plan mode to the "builder" subagent via execute_task. For backwards
// compatibility with persisted sessions, SetMode("build") silently re-maps
// to "plan" and logs a one-line notice to stderr.
func (r *Runtime) SetMode(name string) error {
	if name == "build" {
		fmt.Fprintln(os.Stderr, "build mode deprecated; remapped to plan — the planner now dispatches execute_task to the builder subagent")
		name = "plan"
	}
	mode, ok := GetMode(name)
	if !ok {
		return fmt.Errorf("unknown mode: %s (available: %s)", name, strings.Join(ModeNames(), ", "))
	}
	previous := r.Mode
	r.Mode = name
	r.Policy = mode.Policy
	r.invalidateSystemPromptCache()
	if previous != name {
		r.ModeSwitchedFrom = previous
	}
	return nil
}

func (r *Runtime) invalidateSystemPromptCache() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.systemPromptCache = nil
	r.mu.Unlock()
}

// cachedSystemPrompt returns the system prompt for the current (mode, policy,
// nativeTools) signature, computing and memoizing on first hit. Callers must
// not mutate the returned string.
func (r *Runtime) cachedSystemPrompt(nativeToolCalling bool) string {
	if r == nil {
		return ""
	}
	key := systemPromptCacheKey(nativeToolCalling, r.Mode, r.Policy)
	r.mu.Lock()
	if r.systemPromptCache != nil {
		if cached, ok := r.systemPromptCache[key]; ok {
			r.mu.Unlock()
			return cached
		}
	}
	r.mu.Unlock()
	rendered := systemPrompt(nativeToolCalling, r.Mode, r.Policy)
	r.mu.Lock()
	if r.systemPromptCache == nil {
		r.systemPromptCache = map[string]string{}
	}
	r.systemPromptCache[key] = rendered
	r.mu.Unlock()
	return rendered
}

// PreflightCacheGet returns a cached preflight result for (mode, line) if
// present and not expired. Callers still need to validate freshness against
// repo state beyond what the TTL implies.
func (r *Runtime) PreflightCacheGet(mode, line string) (string, bool) {
	if r == nil {
		return "", false
	}
	key := preflightCacheKey(mode, line)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.preflightCache == nil {
		return "", false
	}
	entry, ok := r.preflightCache[key]
	if !ok || time.Now().After(entry.ExpiresAt) {
		return "", false
	}
	return entry.Value, true
}

// PreflightCacheSet stores a preflight result with the package-defined TTL.
func (r *Runtime) PreflightCacheSet(mode, line, value string) {
	if r == nil || strings.TrimSpace(value) == "" {
		return
	}
	key := preflightCacheKey(mode, line)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.preflightCache == nil {
		r.preflightCache = map[string]preflightCacheEntry{}
	}
	r.preflightCache[key] = preflightCacheEntry{Value: value, ExpiresAt: time.Now().Add(preflightCacheTTL)}
}

// InvalidatePreflightCache drops every cached preflight result. Called by
// executeTool after a successful mutating tool, since any cached analysis of
// pre-mutation state is now stale.
func (r *Runtime) InvalidatePreflightCache() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.preflightCache = nil
	r.mu.Unlock()
}

func preflightCacheKey(mode, line string) string {
	return mode + "|" + strings.TrimSpace(line)
}

func systemPromptCacheKey(nativeTools bool, mode string, policy SprintPolicy) string {
	var b strings.Builder
	if nativeTools {
		b.WriteString("native|")
	} else {
		b.WriteString("text|")
	}
	b.WriteString(mode)
	b.WriteString("|allow:")
	b.WriteString(strings.Join(policy.AllowedNames(), ","))
	b.WriteString("|ask:")
	b.WriteString(strings.Join(policy.AskNames(), ","))
	return b.String()
}

func (r *Runtime) UndoLast() (string, error) {
	r.mu.Lock()
	if len(r.undoStack) == 0 {
		r.mu.Unlock()
		return "", fmt.Errorf("nothing to undo")
	}
	entry := r.undoStack[len(r.undoStack)-1]
	r.undoStack = r.undoStack[:len(r.undoStack)-1]
	r.mu.Unlock()
	if err := patch.Undo(r.CWD, entry.Snapshots); err != nil {
		return "", err
	}
	return entry.Summary, nil
}

// EventTee is an optional second sink that receives a copy of every event
// emitted by Run. Used by /remote-control to broadcast the session stream to
// connected web clients. Nil when no remote listeners are attached.
type EventTee interface {
	Publish(Event)
}

func (r *Runtime) Run(ctx context.Context, userMessage string) <-chan Event {
	events := make(chan Event)
	go func() {
		defer close(events)
		inner := make(chan Event, 32)
		go func() {
			defer close(inner)
			r.run(ctx, userMessage, inner)
		}()
		tee := r.EventTee
		for ev := range inner {
			if tee != nil {
				tee.Publish(ev)
			}
			events <- ev
		}
	}()
	return events
}

func looksLikePlanOnlyResponse(content string) bool {
	lower := strings.ToLower(content)
	if !strings.Contains(lower, "planning complete") &&
		!strings.Contains(lower, "next steps") &&
		!strings.Contains(lower, "todo") &&
		!strings.Contains(lower, "to-do") {
		return false
	}

	checklistLines := 0
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[ ]") ||
			strings.HasPrefix(trimmed, "[>]") ||
			strings.HasPrefix(trimmed, "[x]") ||
			strings.HasPrefix(trimmed, "- [ ]") ||
			strings.HasPrefix(trimmed, "- [x]") {
			checklistLines++
		}
	}
	return checklistLines >= 2
}

func looksLikePlanExecutionIntent(message string) bool {
	lower := strings.ToLower(message)
	for _, phrase := range []string{
		"execute", "implement", "build", "continue", "resume",
		"carry out", "do it", "run the plan", "execute the plan", "approved plan",
		"ejecut", "implement", "constru", "contin", "sigue", "hazlo", "hacerlo",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// ToolSupporter is implemented by providers that may support native tool calling.
type ToolSupporter interface {
	SupportsTools() bool
}

// resolveProvider returns the active provider and whether it supports native tool calling.
func (r *Runtime) resolveProvider() (llm.Provider, bool, error) {
	providerName := r.Config.Providers.Default.Name
	if providerName == "" {
		providerName = "lmstudio"
	}
	provider, ok := r.Providers.Get(providerName)
	if !ok {
		return nil, false, fmt.Errorf("provider %q is not registered", providerName)
	}
	supportsTools := false
	if ts, ok := provider.(ToolSupporter); ok {
		supportsTools = ts.SupportsTools()
	}
	return provider, supportsTools, nil
}

func (r *Runtime) planContextBlock(userMessage, switchedFrom string) string {
	var lines []string
	planSummary := ""
	if r.Plans != nil {
		if doc, ok, err := r.Plans.Current(); err == nil && ok {
			planSummary = strings.TrimSpace(doc.Summary)
			if planSummary == "" {
				planSummary = "(no summary)"
			}
		}
	}

	var taskList []tasks.Task
	if r.Tasks != nil {
		if list, err := r.Tasks.List(); err == nil {
			taskList = list
		}
	}
	pending, inProgress, done := 0, 0, 0
	for _, t := range taskList {
		switch t.Status {
		case "pending":
			pending++
		case "in_progress":
			inProgress++
		case "completed", "done":
			done++
		}
	}
	activeTasks := pending+inProgress > 0
	executeIntent := switchedFrom == "plan" || looksLikePlanExecutionIntent(userMessage)

	switch r.Mode {
	case "plan":
		if planSummary != "" {
			lines = append(lines, fmt.Sprintf("Plan document exists: %s. Call plan_get to read it before refining. Keep the executable checklist separate.", planSummary))
		}
		if executeIntent {
			if activeTasks {
				lines = append(lines, "Execution intent detected. Do NOT call plan_write or todo_write unless the user explicitly asks to re-plan. Read the existing checklist with task_list, execute only the remaining pending tasks via execute_task, and keep the checklist as-is.")
			} else if len(taskList) > 0 {
				lines = append(lines, "Execution intent detected, but the current checklist is already complete. Do NOT create a new plan or rewrite the checklist. Tell the user execution is already complete unless they explicitly ask to refine or re-plan.")
			}
		}
		if len(taskList) > 0 {
			if activeTasks {
				lines = append(lines, fmt.Sprintf("Active checklist: %d pending, %d in progress, %d done. Call task_list to read it. Dispatch pending tasks one-by-one to the builder subagent via execute_task (pass only the minimal relevant_files). Use task_update after the builder returns. Use todo_write only when starting a fresh checklist.", pending, inProgress, done))
			} else {
				lines = append(lines, fmt.Sprintf("Previous checklist complete (%d tasks done). If refining, call task_list before deciding whether to preserve or replace it.", len(taskList)))
			}
		}
	}

	return strings.Join(lines, "\n")
}

func (r *Runtime) run(ctx context.Context, userMessage string, events chan<- Event) {
	turnStart := time.Now()
	r.mu.Lock()
	r.activeEvents = events
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.activeEvents = nil
		r.mu.Unlock()
		r.LastTurnDuration = time.Since(turnStart)
	}()

	provider, supportsTools, err := r.resolveProvider()
	if err != nil {
		events <- Event{Type: EventError, Error: err}
		events <- Event{Type: EventDone}
		return
	}

	role := r.modelRoleForMode()
	model := r.roleModel(role)
	if err := r.EnsureRoleModelLoaded(ctx, provider, role); err != nil {
		events <- Event{Type: EventError, Error: err}
		events <- Event{Type: EventDone}
		return
	}
	roleConfig := r.configForRole(role)
	if r.Mode == "explore" {
		roleConfig = config.ConfigForTaskRole(r.Config, role, r.roleModel(role))
	}
	snapshot := r.buildSnapshot(userMessage, roleConfig)
	r.LastTurnTokensOut = 0
	r.LastTurnTokensPerSec = 0
	r.LastTokensBudget = snapshot.TokensBudget

	// Plan pointer: we don't dump the whole plan into the user prompt anymore
	// — the model calls task_list when it actually needs to see it. Injecting
	// the full list every turn (a) bloats context, and (b) encouraged the
	// model to re-emit the plan via todo_write, triggering our destructive
	// overwrite bug. Just tell it how many tasks exist and what state.
	switchedFrom := r.ModeSwitchedFrom
	planBlock := r.planContextBlock(userMessage, switchedFrom)
	executeIntent := r.Mode == "plan" && looksLikePlanExecutionIntent(userMessage)

	// Mode handoff: when the user just switched modes, give the model an
	// explicit one-turn signal so it adapts.
	handoff := ""
	if switchedFrom != "" && switchedFrom != r.Mode {
		handoff = fmt.Sprintf("MODE SWITCHED: %s → %s. ", strings.ToUpper(switchedFrom), strings.ToUpper(r.Mode))
		if r.Mode == "plan" {
			handoff += "Focus on plan_write for the full plan and todo_write/task_* for the executable checklist. Do not edit files directly — dispatch each task to the builder subagent via execute_task. " +
				"If the user's request leaves scope, constraints, or success criteria ambiguous, start by calling ask_user (3-6 focused questions) before drafting the plan. " +
				"After the interview, call plan_write AND todo_write in the same turn so the user ends with both artifacts."
		}
		r.ModeSwitchedFrom = ""
	}
	explorerHandoff := ""
	if r.Mode == "plan" && strings.TrimSpace(r.PendingExplorerContext) != "" {
		explorerHandoff = r.PendingExplorerContext
		r.PendingExplorerContext = ""
	}
	// BUILD mode is deprecated (the planner now dispatches execute_task to the
	// builder subagent), so PendingBuildPreflight is ignored even if legacy
	// callers still set it. Drop it here so it can't accidentally leak into a
	// future turn.
	buildPreflight := ""
	if strings.TrimSpace(r.PendingBuildPreflight) != "" {
		r.PendingBuildPreflight = ""
	}
	// Explore preflight is opt-in; when set, fold it into the explorerHandoff
	// channel (tier-C block below) so the main explorer turn has the
	// subagent fan-out findings as grounding. The plan-mode path reuses
	// PendingExplorerContext for the same purpose.
	if r.Mode == "explore" && strings.TrimSpace(r.PendingExplorePreflight) != "" {
		if explorerHandoff != "" {
			explorerHandoff += "\n\n" + r.PendingExplorePreflight
		} else {
			explorerHandoff = r.PendingExplorePreflight
		}
		r.PendingExplorePreflight = ""
	}

	messages := []llm.Message{
		{Role: "system", Content: r.cachedSystemPrompt(supportsTools)},
		{Role: "user", Content: userPrompt(snapshot, userMessage, planBlock, r.Mode, handoff, explorerHandoff, buildPreflight)},
	}

	// Track real token usage from actual messages.
	totalMsgChars := 0
	for _, msg := range messages {
		totalMsgChars += len(msg.Content)
	}
	r.LastTurnTokensIn = totalMsgChars / 4
	r.LastTokensUsed = r.LastTurnTokensIn
	r.LastTokensBudget = snapshot.TokensBudget

	maxSteps := r.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 40
	}
	r.LastModelUsed = model

	// Build tool definitions for native mode.
	var toolDefs []llm.ToolDef
	if supportsTools {
		toolDefs = r.Tools.ToolDefs(policyToolNames(r.Policy))
	}

	// Precompute tool-definition byte size once — it doesn't change across
	// steps within a single turn but used to be re-marshalled on every
	// estimateRequestTokens call. O(tools * tool_size) per turn instead of
	// per step.
	toolsChars := 0
	if len(toolDefs) > 0 {
		if data, err := json.Marshal(toolDefs); err == nil {
			toolsChars = len(data)
		}
	}

	maxRetries := r.MaxParseRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}
	parseFailures := 0
	lastFailedTool := ""
	consecutiveToolFailures := 0
	consecutiveReadOnly := 0
	planModeReprompts := 0

	// Step loop: ask_user turns do NOT count toward maxSteps since they are
	// blocked on human input, not model work. See the post-dispatch decrement
	// below so long interviews don't starve plan_write / todo_write / edits.
	for step := 0; step < maxSteps; step++ {
		// Bound the growth of tool-result payloads in the step history. Keeps
		// the last few verbatim and stubs the rest — the model can re-invoke
		// a tool if it still needs the detail.
		compactOldToolResults(messages, 3)

		req := llm.ChatRequest{
			Model:    model,
			Messages: messages,
			Tools:    toolDefs,
		}

		// Update context token count from current message history. Reuse the
		// precomputed toolsChars so we don't re-marshal the tool definitions
		// (unchanged across steps) on every iteration.
		inputTokens := estimateMessageTokens(messages, toolsChars)
		r.LastTurnTokensIn = inputTokens
		r.LastTokensUsed = inputTokens

		// Stream the response for real-time token display.
		accumulated, toolCalls, usage, err := r.streamResponseWithInput(ctx, provider, req, step+1, inputTokens, events)
		r.recordResponseUsage(accumulated, usage)
		if err != nil {
			events <- Event{Type: EventError, Error: err}
			events <- Event{Type: EventDone}
			return
		}

		// Handle empty responses from local models.
		if strings.TrimSpace(accumulated) == "" && len(toolCalls) == 0 {
			messages = append(messages,
				llm.Message{Role: "assistant", Content: ""},
				llm.Message{Role: "user", Content: "You returned an empty response. Please provide an answer or use a tool to gather information."},
			)
			continue
		}

		if supportsTools && len(toolCalls) > 0 {
			// Native tool calling path.
			// Text was already streamed as deltas — no need to emit again.

			// Build the assistant message with tool_calls for conversation history.
			assistantMsg := llm.Message{
				Role:      "assistant",
				Content:   accumulated,
				ToolCalls: toolCalls,
			}
			messages = append(messages, assistantMsg)

			// Execute each tool call and append role:tool responses.
			allDone := true
			for _, tc := range toolCalls {
				agentCall := FromNativeToolCall(tc)
				events <- Event{Type: EventToolCall, ToolName: agentCall.Name, Input: agentCall.Input}

				result, observation := r.executeTool(ctx, agentCall, events)
				if result != nil {
					events <- Event{Type: EventToolResult, ToolName: agentCall.Name, Result: result, Text: result.Summary}
				}

				// Track consecutive failures.
				nativeToolFailed := result != nil && (strings.Contains(result.Summary, "not found") ||
					strings.Contains(result.Summary, "error") ||
					strings.Contains(result.Summary, "denied"))
				if nativeToolFailed && agentCall.Name == lastFailedTool {
					consecutiveToolFailures++
					if consecutiveToolFailures >= 3 {
						events <- Event{Type: EventError, Error: fmt.Errorf("tool %s failed %d times — stopping", agentCall.Name, consecutiveToolFailures)}
						events <- Event{Type: EventDone}
						return
					}
				} else if nativeToolFailed {
					lastFailedTool = agentCall.Name
					consecutiveToolFailures = 1
				} else {
					lastFailedTool = ""
					consecutiveToolFailures = 0
				}

				// In plan mode, stop after todo_write.
				if r.Mode == "plan" && agentCall.Name == "todo_write" {
					if result != nil && strings.TrimSpace(result.Summary) != "" {
						events <- Event{Type: EventAssistantText, Text: "Plan created and saved.\n" + result.Summary}
					}
					events <- Event{Type: EventDone}
					return
				}

				messages = append(messages, llm.Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    observation,
				})
				allDone = false
			}
			if allDone {
				events <- Event{Type: EventDone}
				return
			}
			continue
		}

		// Text-based fallback path (SupportsTools=false or no native tool calls).
		// Use model-specific parser for better compatibility.
		parser := r.Parsers.ForModel(model)
		r.LastParserUsed = parser.Name()
		parsed, err := parser.Parse(accumulated)
		if err != nil {
			parseFailures++
			// Clear streamed text that contained broken tool_call XML.
			events <- Event{Type: EventClearStreaming}
			if parseFailures >= maxRetries {
				// Retries exhausted — surface the raw parse error so the user
				// can see what went wrong. Include a bounded preview of the
				// model output for parser-shape diagnostics.
				preview := strings.TrimSpace(accumulated)
				if len(preview) > 800 {
					preview = preview[:400] + "\n  ...[" + fmt.Sprintf("%d", len(preview)-800) + " chars elided]...\n" + preview[len(preview)-400:]
				}
				events <- Event{Type: EventError, Error: fmt.Errorf("parse error (attempt %d/%d) [parser=%s model=%s]: %w\nmodel emitted: %s", parseFailures, maxRetries, parser.Name(), model, err, preview)}
				// Give up on tool calling — emit clean text without tool_call XML.
				clean := stripPartialToolCallTail(accumulated)
				if clean != "" {
					events <- Event{Type: EventAssistantText, Text: clean}
				}
				events <- Event{Type: EventDone}
				return
			}
			// Silent retry — ask the model to re-emit a clean tool call. The
			// reprompt is tailored to the failure kind so the model gets a
			// concrete nudge instead of a raw parser error.
			messages = append(messages,
				llm.Message{Role: "assistant", Content: accumulated},
				llm.Message{Role: "user", Content: buildParseReprompt(accumulated, err)},
			)
			continue
		}
		parseFailures = 0 // reset on successful parse

		if !parsed.Found {
			// Detect leaked / partial <tool_call tags that none of the parsers
			// recognized (unclosed angle bracket, missing close tag, junk
			// between tag and JSON). Without this, the raw fragment would
			// leak into the final answer and the turn ends silently.
			if containsPartialToolCall(accumulated) && parseFailures < maxRetries {
				parseFailures++
				events <- Event{Type: EventClearStreaming}
				messages = append(messages,
					llm.Message{Role: "assistant", Content: accumulated},
					llm.Message{Role: "user", Content: "Your previous message contained a partial or malformed <tool_call> tag. Re-emit exactly one complete <tool_call>{\"name\":\"...\",\"input\":{...}}</tool_call> block with valid JSON, or give a final answer with NO tool_call tags at all."},
				)
				continue
			}
			if r.Mode == "plan" {
				if r.createPlanFromTextFallback(accumulated, events) {
					events <- Event{Type: EventDone}
					return
				}
				if planModeReprompts < 2 {
					planModeReprompts++
					messages = append(messages,
						llm.Message{Role: "assistant", Content: accumulated},
						llm.Message{Role: "user", Content: "You are in PLAN mode. Save the detailed plan with plan_write, then save the executable checklist with todo_write or task_* tools. Return exactly one valid tool_call now. Do not answer in prose only."},
					)
					continue
				}
			}
			// No tool call — this is the final answer.
			// Text was already streamed as deltas — no need to emit again.
			events <- Event{Type: EventDone}
			return
		}

		// Clear the streamed text that contained raw <tool_call> XML.
		events <- Event{Type: EventClearStreaming, Text: parsed.Before}
		if parsed.Before != "" {
			events <- Event{Type: EventAssistantText, Text: parsed.Before}
		}
		events <- Event{Type: EventToolCall, ToolName: parsed.Call.Name, Input: parsed.Call.Input}

		result, observation := r.executeTool(ctx, parsed.Call, events)
		if result != nil {
			events <- Event{Type: EventToolResult, ToolName: parsed.Call.Name, Result: result, Text: result.Summary}
		}

		// Don't charge non-productive calls against the step budget:
		//   ask_user       — blocked on human input
		//   task_list / plan_get / git_status — read-only status pings
		// These should never starve plan_write / todo_write / file edits.
		switch parsed.Call.Name {
		case "ask_user", "task_list", "plan_get", "git_status":
			step--
		}

		// No-progress stall guard: the planner should be dispatching
		// execute_task or mutating tools (plan_write / todo_write /
		// task_update). Long runs of read-only exploration without any of
		// those signals an aimless loop; stop so the cap isn't burned.
		if isMutatingToolCall(parsed.Call.Name) || parsed.Call.Name == "execute_task" {
			consecutiveReadOnly = 0
		} else if isReadOnlyExploration(parsed.Call.Name) {
			consecutiveReadOnly++
			if consecutiveReadOnly >= 10 {
				events <- Event{Type: EventError, Error: fmt.Errorf("stopped: 10 consecutive read-only tool calls with no edits — dispatch execute_task, call plan_write / todo_write, or answer the user directly")}
				events <- Event{Type: EventDone}
				return
			}
		}

		// Track consecutive failures of the same tool to break infinite loops.
		toolFailed := result != nil && (strings.Contains(result.Summary, "not found") ||
			strings.Contains(result.Summary, "error") ||
			strings.Contains(result.Summary, "denied"))
		if toolFailed && parsed.Call.Name == lastFailedTool {
			consecutiveToolFailures++
			if consecutiveToolFailures >= 3 {
				events <- Event{Type: EventError, Error: fmt.Errorf("tool %s failed %d times in a row — stopping to avoid infinite loop", parsed.Call.Name, consecutiveToolFailures)}
				events <- Event{Type: EventDone}
				return
			}
		} else if toolFailed {
			lastFailedTool = parsed.Call.Name
			consecutiveToolFailures = 1
		} else {
			lastFailedTool = ""
			consecutiveToolFailures = 0
		}

		// In plan mode, stop after todo_write — the plan is ready for user review.
		if r.Mode == "plan" && parsed.Call.Name == "todo_write" {
			// Let the model give a final summary by doing one more LLM call without tools.
			messages = append(messages,
				llm.Message{Role: "assistant", Content: accumulated},
				llm.Message{Role: "user", Content: observation + "\n\nThe plan has been created. Provide a brief summary of the plan to the user. Do not call any more tools."},
			)
			summaryAcc, _, summaryUsage, err := r.streamResponse(ctx, provider, llm.ChatRequest{Model: model, Messages: messages}, step+1, events)
			r.recordResponseUsage(summaryAcc, summaryUsage)
			if err != nil {
				events <- Event{Type: EventError, Error: err}
			}
			events <- Event{Type: EventDone}
			return
		}
		if r.Mode == "plan" && executeIntent && parsed.Call.Name == "execute_task" && !r.hasActiveChecklistTasks() {
			messages = append(messages,
				llm.Message{Role: "assistant", Content: accumulated},
				llm.Message{Role: "user", Content: observation + "\n\nAll checklist tasks are now complete. Do not call plan_write, todo_write, or execute_task again. Give a brief completion summary to the user and stop."},
			)
			summaryAcc, _, summaryUsage, err := r.streamResponse(ctx, provider, llm.ChatRequest{Model: model, Messages: messages}, step+1, events)
			r.recordResponseUsage(summaryAcc, summaryUsage)
			if err != nil {
				events <- Event{Type: EventError, Error: err}
			}
			events <- Event{Type: EventDone}
			return
		}

		messages = append(messages,
			llm.Message{Role: "assistant", Content: accumulated},
			llm.Message{Role: "user", Content: observation},
		)
		if parsed.After != "" {
			messages = append(messages, llm.Message{Role: "user", Content: "Text after tool call was ignored until the tool result was available: " + parsed.After})
		}
	}

	events <- Event{Type: EventError, Error: fmt.Errorf("agent stopped after %d steps in %s mode", maxSteps, r.Mode)}
	events <- Event{Type: EventDone}
}

// isMutatingToolCall returns true when the tool produces a real side effect
// (file edit, command run, plan/todo write, patch apply). Used by the
// no-progress stall guard in build mode.
func isMutatingToolCall(name string) bool {
	switch name {
	case "write_file", "edit_file", "apply_patch", "run_command",
		"plan_write", "todo_write", "task_update", "task_add", "task_complete":
		return true
	}
	return false
}

// isReadOnlyExploration returns true for tools that merely inspect state
// without changing it. Long runs of these with no mutation in between signal
// an aimless-exploration stall.
func isReadOnlyExploration(name string) bool {
	switch name {
	case "read_file", "list_files", "search_text", "search_files", "git_diff":
		return true
	}
	return false
}

func (r *Runtime) hasActiveChecklistTasks() bool {
	if r == nil || r.Tasks == nil {
		return false
	}
	list, err := r.Tasks.List()
	if err != nil {
		return false
	}
	for _, task := range list {
		switch task.Status {
		case "", "pending", "in_progress":
			return true
		}
	}
	return false
}

func policyToolNames(policy SprintPolicy) []string {
	names := append([]string{}, policy.AllowedNames()...)
	names = append(names, policy.AskNames()...)
	return names
}

func (r *Runtime) createPlanFromTextFallback(content string, events chan<- Event) bool {
	items := extractPlanItemsFromText(content)
	if len(items) == 0 {
		return false
	}
	payload, err := json.Marshal(map[string][]string{"items": items})
	if err != nil {
		return false
	}
	call := ToolCall{Name: "todo_write", Input: payload}
	events <- Event{Type: EventToolCall, ToolName: call.Name, Input: call.Input}
	result, _ := r.executeTodoWrite(call.Input)
	if result != nil {
		events <- Event{Type: EventToolResult, ToolName: call.Name, Result: result, Text: result.Summary}
		if strings.TrimSpace(result.Summary) != "" {
			events <- Event{Type: EventAssistantText, Text: "Plan created and saved.\n" + result.Summary}
		}
	}
	return true
}

func extractPlanItemsFromText(content string) []string {
	var items []string
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		item, ok := planItemFromLine(line, &inFence)
		if ok {
			items = append(items, item)
		}
	}
	return items
}

func planItemFromLine(line string, inFence *bool) (string, bool) {
	trimmed := strings.TrimSpace(stripCommonPlanLinePrefix(line))
	if trimmed == "" {
		return "", false
	}
	if strings.HasPrefix(trimmed, "```") {
		*inFence = !*inFence
		return "", false
	}
	if *inFence || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "---") {
		return "", false
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "please confirm") ||
		strings.HasPrefix(lower, "confirm if") ||
		strings.HasPrefix(lower, "summary:") ||
		strings.HasPrefix(lower, "resumen:") ||
		strings.HasSuffix(trimmed, ":") {
		return "", false
	}
	for _, prefix := range []string{"- [ ] ", "- [x] ", "- [X] ", "- [>] ", "* [ ] ", "* [x] ", "* [X] ", "* [>] "} {
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimSpace(trimmed[2:]), true
		}
	}
	for _, prefix := range []string{"[ ] ", "[x] ", "[X] ", "[>] "} {
		if strings.HasPrefix(trimmed, prefix) {
			return trimmed, true
		}
	}
	for _, prefix := range []string{"- ", "* ", "• "} {
		if strings.HasPrefix(trimmed, prefix) {
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			if item != "" && !strings.HasSuffix(item, ":") {
				return item, true
			}
		}
	}
	if item, ok := stripNumberedPlanPrefix(trimmed); ok {
		return item, true
	}
	if item, ok := stripStepPlanPrefix(trimmed); ok {
		return item, true
	}
	return "", false
}

func stripCommonPlanLinePrefix(line string) string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, ">")
	return strings.TrimSpace(trimmed)
}

func stripNumberedPlanPrefix(line string) (string, bool) {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(line) || (line[i] != '.' && line[i] != ')') {
		return "", false
	}
	item := strings.TrimSpace(line[i+1:])
	return item, item != "" && !strings.HasSuffix(item, ":")
}

func stripStepPlanPrefix(line string) (string, bool) {
	lower := strings.ToLower(line)
	if !strings.HasPrefix(lower, "step ") && !strings.HasPrefix(lower, "paso ") {
		return "", false
	}
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", false
	}
	item := strings.TrimSpace(line[idx+1:])
	return item, item != "" && !strings.HasSuffix(item, ":")
}

// streamResponse streams the LLM response, emitting deltas and live progress to
// the event channel, and returns the accumulated text content and any tool calls.
func (r *Runtime) streamResponse(ctx context.Context, provider llm.Provider, req llm.ChatRequest, step int, events chan<- Event) (string, []llm.ToolCall, *llm.TokenUsage, error) {
	return r.streamResponseWithInput(ctx, provider, req, step, estimateRequestTokens(req), events)
}

// streamResponseWithInput is streamResponse with a precomputed input-token
// estimate. The step loop in run() already maintains this incrementally with
// cached tool-definition bytes, so passing it through avoids a redundant
// walk+marshal on every step.
func (r *Runtime) streamResponseWithInput(ctx context.Context, provider llm.Provider, req llm.ChatRequest, step, inputTokens int, events chan<- Event) (string, []llm.ToolCall, *llm.TokenUsage, error) {
	start := time.Now()
	var firstTokenAt time.Time
	var text strings.Builder
	var usage *llm.TokenUsage

	emitProgress := func(phase string, done bool) {
		outputTokens := estimateTextTokens(text.String())
		elapsed := time.Since(start)
		if !firstTokenAt.IsZero() {
			elapsed = time.Since(firstTokenAt)
		}
		promptTokens := inputTokens
		totalTokens := promptTokens + outputTokens
		if usage != nil {
			if usage.PromptTokens > 0 {
				promptTokens = usage.PromptTokens
			}
			if usage.CompletionTokens > 0 {
				outputTokens = usage.CompletionTokens
			}
			if usage.TotalTokens > 0 {
				totalTokens = usage.TotalTokens
			} else {
				totalTokens = promptTokens + outputTokens
			}
		}
		tps := 0.0
		if elapsed > 0 && outputTokens > 0 {
			tps = float64(outputTokens) / elapsed.Seconds()
		}
		r.LastTokensUsed = totalTokens
		events <- Event{Type: EventModelProgress, Progress: &ModelProgress{
			Phase:           phase,
			Model:           req.Model,
			Step:            step,
			InputTokens:     promptTokens,
			OutputTokens:    outputTokens,
			TotalTokens:     totalTokens,
			TokensPerSecond: tps,
			Elapsed:         time.Since(start),
			Done:            done,
		}}
	}

	emitProgress("waiting", false)
	stream, err := provider.Stream(ctx, req)
	if err != nil {
		// Fallback to non-streaming Chat if Stream fails.
		resp, chatErr := provider.Chat(ctx, req)
		if chatErr != nil {
			return "", nil, nil, chatErr
		}
		text.WriteString(resp.Content)
		emitProgress("complete", true)
		return resp.Content, resp.ToolCalls, nil, nil
	}

	var toolCalls []llm.ToolCall
	toolCallSeen := false
	// searchFrom tracks how far into `text` we've already scanned for the
	// <tool_call> tag. Only the newly-written slice (minus a small back-off
	// covering a tag split across chunks) needs to be searched — avoids the
	// O(n²) full-buffer rescan that previously stalled the UI at high tk/s.
	const toolCallTag = "<tool_call>"
	searchFrom := 0

	for event := range stream {
		switch event.Type {
		case "text":
			if firstTokenAt.IsZero() {
				firstTokenAt = time.Now()
			}
			text.WriteString(event.Text)
			emitProgress("streaming", false)
			// Once we detect <tool_call> in the accumulated text, stop streaming to UI.
			if !toolCallSeen {
				accumulated := text.String()
				start := max(searchFrom-(len(toolCallTag)-1), 0)
				if idx := strings.Index(accumulated[start:], toolCallTag); idx >= 0 {
					toolCallSeen = true
					// Don't emit any more deltas — the text will be processed by ParseToolCall.
				} else {
					events <- Event{Type: EventAssistantDelta, Text: event.Text}
				}
				searchFrom = len(accumulated)
			}
		case "tool_calls":
			toolCalls = event.ToolCalls
			emitProgress("tool_call", false)
		case "usage":
			usage = event.Usage
			emitProgress("streaming", false)
		case "error":
			return text.String(), toolCalls, usage, event.Error
		case "done":
			// Stream finished.
		}
	}
	// Capture the final tk/s for this stream so the TUI footer and the
	// per-turn log line can show it alongside timing + token counts.
	if !firstTokenAt.IsZero() {
		outputTokens := estimateTextTokens(text.String())
		if usage != nil && usage.CompletionTokens > 0 {
			outputTokens = usage.CompletionTokens
		}
		elapsed := time.Since(firstTokenAt)
		if elapsed > 0 && outputTokens > 0 {
			r.LastTurnTokensPerSec = float64(outputTokens) / elapsed.Seconds()
		}
	}
	emitProgress("complete", true)
	return text.String(), toolCalls, usage, nil
}

func (r *Runtime) recordResponseUsage(content string, usage *llm.TokenUsage) {
	if usage != nil {
		if usage.PromptTokens > 0 {
			r.LastTurnTokensIn = usage.PromptTokens
		}
		if usage.CompletionTokens > 0 {
			r.LastTurnTokensOut += usage.CompletionTokens
			if usage.TotalTokens > 0 {
				r.LastTokensUsed = usage.TotalTokens
			}
			return
		}
	}
	r.LastTurnTokensOut += estimateTextTokens(content)
	r.LastTokensUsed = r.LastTurnTokensIn + r.LastTurnTokensOut
}

// estimateMessageTokens counts characters across messages and folds in a
// precomputed tools-payload byte size (produced once per turn by the caller).
// Avoids the per-step json.Marshal of req.Tools that estimateRequestTokens
// does on each invocation — tool defs don't change across steps in a turn.
func estimateMessageTokens(messages []llm.Message, toolsChars int) int {
	chars := toolsChars
	for _, msg := range messages {
		chars += len(msg.Role) + len(msg.Content) + len(msg.ToolCallID)
		for _, call := range msg.ToolCalls {
			chars += len(call.ID) + len(call.Type) + len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	return estimateTokenCount(chars)
}

func estimateRequestTokens(req llm.ChatRequest) int {
	chars := 0
	for _, msg := range req.Messages {
		chars += len(msg.Role) + len(msg.Content) + len(msg.ToolCallID)
		for _, call := range msg.ToolCalls {
			chars += len(call.ID) + len(call.Type) + len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	if len(req.Tools) > 0 {
		if data, err := json.Marshal(req.Tools); err == nil {
			chars += len(data)
		}
	}
	return estimateTokenCount(chars)
}

func estimateTextTokens(text string) int {
	return estimateTokenCount(len(text))
}

func estimateTokenCount(chars int) int {
	if chars <= 0 {
		return 0
	}
	tokens := chars / 4
	if tokens == 0 {
		return 1
	}
	return tokens
}
