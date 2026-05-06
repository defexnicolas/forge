package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"forge/internal/config"
	contextbuilder "forge/internal/context"
	"forge/internal/gitops"
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
	EventReadBudget       = "read_budget"
	EventError            = "error"
	EventDone             = "done"
)

// ReadBudgetState reports the running count of consecutive read-only tool
// calls vs. the active threshold. Emitted on every read-only call so the TUI
// can render a live "reads: N/M" indicator. Threshold is 0 when the guard is
// disabled (e.g. explore mode) — TUI hides the indicator in that case.
type ReadBudgetState struct {
	Consumed  int
	Threshold int
	Mode      string
	Nudged    bool
}

// SubagentProgress reports the lifecycle of one task within a spawn_subagents
// batch. The TUI keys on (BatchID, Index) to update the corresponding lane
// in the multi-lane view. Status values: "pending", "running", "completed",
// "error".
type SubagentProgress struct {
	BatchID   string
	Index     int
	Total     int
	Agent     string
	Status    string
	Phase     string
	StepsUsed int
	TimedOut  bool
	Summary   string
	Error     string
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
	ReadBudget       *ReadBudgetState
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
	ID          string
	ToolName    string
	Input       json.RawMessage
	Summary     string
	Diff        string
	Response    chan ApprovalResponse
	plan        patch.Plan
	command     *ToolCall
	beforeApply func() error
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
	// LastProviderUsed is the resolved backend name (lmstudio / llama-server /
	// openai) of the provider that handled the last turn. Set alongside
	// LastModelUsed at the top of run(). The status bar reads this so it shows
	// the real backend even when the registry slot is named "lmstudio" but
	// points at a different server (a common config for llama-server users).
	LastProviderUsed        string
	LastParserUsed          string
	// lastNarrationCancel carries the offending repeated line when the
	// streaming narration-loop guard cancels a request. Set by the
	// streaming loop, consumed (and cleared) by the outer turn loop.
	// Used to (a) skip the empty-response counter for this iteration —
	// reasoning-only narration cancels leave `accumulated` empty and
	// would otherwise double-punish the model — and (b) emit a tailored
	// reprompt that names the offending line instead of the generic
	// "no tool call" prompt.
	lastNarrationCancel string
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
	LastTurnDuration      time.Duration
	LastTurnTokensIn      int
	LastTurnTokensOut     int
	LastTurnTokensPerSec  float64
	LastTurnStepsUsed     int
	LastTurnReadOnlySteps int
	LastTurnMutatingSteps int
	LastTurnCacheHits     int
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
	// readCache memoizes read_file results within a single turn. Reset at
	// the top of run() so a new turn always sees fresh disk state. Mutating
	// tools (edit_file/write_file/apply_patch) invalidate matching paths;
	// run_command flushes the whole cache because it can write arbitrarily.
	readCache *readCacheStore
	// Per-turn step efficiency counters. Reset at the top of run() and
	// surfaced through LastTurn* fields after the turn completes. Atomic
	// because parallel execute_task may bump turnStepsUsed concurrently.
	turnStepsUsed     atomic.Int64
	turnReadOnlySteps atomic.Int64
	turnMutatingSteps atomic.Int64
	// EventTee, if set, receives a copy of every event emitted by Run. Used
	// by /remote-control to broadcast to connected web clients.
	EventTee EventTee
	// activeEvents is the events channel of the in-flight turn. Set at the
	// top of run() and cleared at the end. Subagents invoked during the turn
	// (e.g. the builder via execute_task) read it so they can raise approval
	// prompts for their own mutating tool calls. Nil when no turn is active.
	activeEvents chan<- Event
	// activeBuilderTask carries the currently executing builder task metadata
	// so mutation approval can enforce per-task file strategies.
	activeBuilderTask *builderTaskGuard
	gitState          gitops.SessionState
	// readBudgetOverride, when set, replaces the threshold returned by
	// activeReadBudget() for the current session. Set by the /reads extend
	// slash command so the user can keep working past the configured limit
	// without editing .forge/config.toml and reloading. Zero = no override.
	// Negative = explicit opt-out (treated as no guard, like the config flag).
	readBudgetOverride int
	// lastReadBudget* are the most recent values emitted via EventReadBudget.
	// Read by /reads (with no args) so the user can see the current state of
	// the guard between turns. Updated under r.mu.
	lastReadBudgetConsumed  int
	lastReadBudgetThreshold int
}

type preflightCacheEntry struct {
	Value     string
	ExpiresAt time.Time
}

const preflightCacheTTL = 10 * time.Minute

func NewRuntime(cwd string, cfg config.Config, registry *tools.Registry, providers *llm.Registry) *Runtime {
	maxSteps := cfg.Runtime.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 40
	}
	commands := permissions.DefaultCommandPolicy()
	if name := strings.TrimSpace(cfg.PermissionsProfile); name != "" {
		if profile, ok := permissions.GetProfile(name); ok {
			commands = profile.Policy
		}
	}
	runtime := &Runtime{
		CWD:       cwd,
		Config:    cfg,
		Tools:     registry,
		Providers: providers,
		Builder:   contextbuilder.NewBuilder(cwd, cfg, registry),
		MaxSteps:  maxSteps,
		Mode:      "plan",
		Policy:    NewPlanPolicy(),
		Commands:  commands,
		Plans:     plans.New(cwd),
		Tasks:     tasks.New(cwd),
		Subagents: DefaultSubagents(),
		Parsers:   DefaultParsers(),
	}
	runtime.seedLoadedModelsFromConfig()
	runtime.RefreshGitSessionState()
	return runtime
}

func (r *Runtime) seedLoadedModelsFromConfig() {
	if r == nil {
		return
	}
	activeRole := r.modelRoleForMode()
	activeModel := r.roleModel(activeRole)
	if detected := config.DetectedForRole(r.Config, activeRole, activeModel); detected != nil {
		modelID := strings.TrimSpace(detected.ModelID)
		if modelID == "" {
			modelID = activeModel
		}
		if modelID != "" {
			r.MarkModelLoaded(modelID)
		}
	}
	if strings.EqualFold(strings.TrimSpace(r.Config.ModelLoading.Strategy), "parallel") {
		for role, detected := range r.Config.Context.DetectedByRole {
			modelID := strings.TrimSpace(detected.ModelID)
			if modelID == "" {
				modelID = r.roleModel(role)
			}
			if modelID == "" || detected.LoadedContextLength <= 0 {
				continue
			}
			if r.loadedModels == nil {
				r.loadedModels = map[string]bool{}
			}
			r.loadedModels[modelID] = true
		}
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

// ResetLoadedModels clears all "currently loaded" tracking. Called when the
// active provider or its model selection changes mid-session (hub > model-multi,
// /provider) so the next turn re-evaluates against the new backend instead of
// trusting stale LM Studio bookkeeping.
func (r *Runtime) ResetLoadedModels() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadedModels = nil
	r.currentLoadedModel = ""
	r.startupReloadDone = false
}

// SetDetectedContext writes a fresh probe result into the runtime's config
// under the given role. Used by the hub-side model-multi flow to push the new
// model's loaded context length into the status bar without waiting for the
// first user turn.
func (r *Runtime) SetDetectedContext(role string, detected *config.DetectedContext) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	config.SetDetectedForRole(&r.Config, role, detected)
}

// SetLastProviderUsed updates the cached backend label used by the status bar.
// Provider-form / model-form / model-multi-form flows call this so the bar
// reflects the new backend immediately, without waiting for the first turn to
// resolve it via run().
func (r *Runtime) SetLastProviderUsed(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.LastProviderUsed = strings.TrimSpace(name)
}

// ModelRoleForActiveMode is the public form of modelRoleForMode. UI code uses
// this to decide which model id ("chat" vs "planner" vs "explorer" vs "editor")
// is active for the current mode without reaching into runtime internals.
func (r *Runtime) ModelRoleForActiveMode() string {
	if r == nil {
		return "chat"
	}
	return r.modelRoleForMode()
}

// ResolveProvider returns the active provider from the registry. Public form
// of resolveProvider. UI code (e.g. status bar) uses this to read backend
// metadata without duplicating the lookup logic. Returns a non-nil error
// (instead of panicking) when called before the runtime is fully wired —
// status-bar layout tests construct a runtime without a Providers registry,
// and we want a clean fallback rather than a nil-deref during render.
func (r *Runtime) ResolveProvider() (llm.Provider, bool, error) {
	if r == nil {
		return nil, false, fmt.Errorf("runtime unavailable")
	}
	if r.Providers == nil {
		return nil, false, fmt.Errorf("provider registry not initialized")
	}
	return r.resolveProvider()
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
	case "build":
		return "editor"
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

type PromptPreview struct {
	System           string
	User             string
	SupportsTools    bool
	ArtifactStrategy string
	Snapshot         contextbuilder.Snapshot
}

func (r *Runtime) PreviewPrompt(userMessage string) (PromptPreview, error) {
	if r == nil {
		return PromptPreview{}, fmt.Errorf("runtime is nil")
	}
	_, supportsTools, err := r.resolveProvider()
	if err != nil {
		return PromptPreview{}, err
	}
	role := r.modelRoleForMode()
	roleConfig := r.configForRole(role)
	if r.Mode == "explore" {
		roleConfig = config.ConfigForTaskRole(r.Config, role, r.roleModel(role))
	}
	snapshot := r.buildSnapshot(userMessage, roleConfig)
	return PromptPreview{
		System:           r.cachedSystemPrompt(supportsTools),
		User:             userPrompt(snapshot, userMessage, r.planContextBlock(userMessage, ""), r.Mode, "", "", ""),
		SupportsTools:    supportsTools,
		ArtifactStrategy: inferDefaultFileStrategy(userMessage, ""),
		Snapshot:         snapshot,
	}, nil
}

func (r *Runtime) EnsureRoleModelLoaded(ctx context.Context, provider llm.Provider, role string) error {
	if r == nil || provider == nil {
		return nil
	}
	// Backends that can't honor a programmatic load (llama-server, generic
	// OpenAI-compatible) skip the entire dance. Trying anyway used to issue
	// LM Studio-only endpoints, fail, and log "proceeding anyway" once per
	// turn. Marking the model as loaded here also satisfies the
	// startupReloadDone short-circuit so subsequent turns stay quiet.
	if loader, ok := provider.(llm.ExplicitLoader); ok && !loader.SupportsExplicitLoad() {
		modelID := r.roleModel(role)
		if modelID != "" {
			r.MarkModelLoaded(modelID)
		}
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

// ReloadResult describes the outcome of a /model reload. Loaded is false when
// the backend doesn't support programmatic reload (llama-server, generic
// OpenAI) — but Refreshed is still true because we always re-classify the
// backend, re-probe the model, and update LastProviderUsed +
// DetectedContext. The caller renders different messages for each combo.
type ReloadResult struct {
	ModelID   string
	Backend   string // resolved backend name (lmstudio / llama-server / openai)
	Refreshed bool   // metadata caches were updated (always true on success)
	Loaded    bool   // server-side LoadModel actually ran
}

// ReloadCurrentModel re-detects the active provider's backend, re-probes the
// model for fresh metadata, and (when supported) forces a server-side reload
// applying the configured context length and parallel slots. It is the
// user's "refresh everything about the current model" hammer — invoking it
// after hot-swapping the listening server (llama-server ↔ LM Studio at the
// same URL) recovers from a stale BackendKind cache that the ordinary
// per-turn flow can't invalidate.
func (r *Runtime) ReloadCurrentModel(ctx context.Context) (ReloadResult, error) {
	if r == nil {
		return ReloadResult{}, fmt.Errorf("runtime unavailable")
	}
	provider, _, err := r.resolveProvider()
	if err != nil {
		return ReloadResult{}, err
	}
	role := r.modelRoleForMode()
	modelID := r.roleModel(role)
	if modelID == "" {
		return ReloadResult{}, fmt.Errorf("model id is required")
	}
	// Force backend re-classification before any capability checks. Catches
	// the case where the user swapped the server listening at BaseURL
	// (llama-server ↔ LM Studio) without going through Forge's provider
	// form — the cached BackendKind would otherwise stay stuck on the
	// previous answer.
	if refresher, ok := provider.(llm.BackendRefresher); ok {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		refresher.RefreshBackend(rctx)
		cancel()
	}
	// Refresh the per-role detected context too. Probe is cheap (one HTTP
	// call) and gives us LoadedContextLength for the status bar denominator
	// without waiting for the next user turn. Also covers /props for
	// llama-server, where /v1/models doesn't carry the field.
	pctx, pcancel := context.WithTimeout(ctx, 5*time.Second)
	if info, perr := provider.ProbeModel(pctx, modelID); perr == nil && info != nil && info.LoadedContextLength > 0 {
		r.SetDetectedContext(role, &config.DetectedContext{
			ModelID:             info.ID,
			LoadedContextLength: info.LoadedContextLength,
			MaxContextLength:    info.MaxContextLength,
			ProbedAt:            time.Now().UTC(),
		})
	}
	pcancel()
	backend := provider.Name()
	if bn, ok := provider.(llm.BackendNamer); ok {
		backend = bn.BackendName()
	}
	r.SetLastProviderUsed(backend)
	result := ReloadResult{ModelID: modelID, Backend: backend, Refreshed: true}
	// Backends that can't honor a programmatic load (llama-server, generic
	// OpenAI): the metadata refresh is the most useful work we can do. The
	// caller decides whether to surface this as informational or as a
	// soft-error — an error is returned so existing UIs don't silently
	// pretend the actual load happened.
	if loader, ok := provider.(llm.ExplicitLoader); ok && !loader.SupportsExplicitLoad() {
		return result, fmt.Errorf("provider %q (%s) does not support programmatic model reload — restart the server with the desired --ctx-size to change the window", provider.Name(), backend)
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
		return result, err
	}
	r.mu.Lock()
	r.startupReloadDone = true
	r.mu.Unlock()
	r.MarkModelLoaded(modelID)
	result.Loaded = true
	return result, nil
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
// not mutate the returned string. The output style appendix (if any) is
// folded into the cached value so a config change requires reset (which
// SetMode already does).
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
	if appendix := r.outputStyleAppendix(); appendix != "" {
		rendered = rendered + "\n\n" + appendix
	}
	r.mu.Lock()
	if r.systemPromptCache == nil {
		r.systemPromptCache = map[string]string{}
	}
	r.systemPromptCache[key] = rendered
	r.mu.Unlock()
	return rendered
}

// outputStyleAppendix loads the configured output-style markdown and wraps
// it in a clearly-marked section. Best-effort: a missing file returns ""
// and the agent continues with the default voice. The file is read on
// demand and not cached separately because cachedSystemPrompt's own cache
// already amortizes it across the whole turn-chain.
func (r *Runtime) outputStyleAppendix() string {
	path := strings.TrimSpace(r.Config.OutputStyle)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	body := strings.TrimSpace(string(data))
	if body == "" {
		return ""
	}
	return "--- output style ---\n" + body
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
	r.RefreshGitSessionState()
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
	lower := strings.TrimSpace(strings.ToLower(message))
	if strings.HasPrefix(lower, "plan refinement request:") || strings.HasPrefix(lower, "new plan goal:") {
		return false
	}
	if len(strings.Fields(lower)) > 6 {
		return false
	}
	for _, phrase := range []string{
		"execute", "run the plan", "execute the plan", "build it",
		"do it", "go ahead", "proceed", "start", "let's go", "run it",
		"ejecuta", "hazlo", "hacerlo", "adelante", "dale", "procede", "implementa",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func looksLikePlanRefinementIntent(message string) bool {
	lower := strings.ToLower(message)
	for _, phrase := range []string{
		"refine", "adjust", "update the plan", "modify the plan", "change the plan",
		"add to the plan", "remove from the plan", "tweak", "revise",
		"refina", "ajusta", "ajusta el plan", "actualiza el plan", "modifica el plan",
		"cambia el plan", "agrega al plan", "quita del plan", "revisa el plan",
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
	var planDoc plans.Document
	hasPlanDoc := false
	if r.Plans != nil {
		if doc, ok, err := r.Plans.Current(); err == nil && ok {
			planDoc = doc
			hasPlanDoc = true
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
		if digest := compactPlanDigest(planDoc, taskList); hasPlanDoc && digest != "" {
			lines = append(lines, digest)
		}
		if planSummary != "" && !executeIntent {
			if looksLikePlanRefinementIntent(userMessage) || strings.TrimSpace(userMessage) != "" {
				lines = append(lines, "Default to refining the existing plan and checklist. Do NOT start over unless the user explicitly asks for a fresh plan. Prefer task_list + task_update/task_create for incremental checklist changes; use todo_write only for an explicit reset.")
				lines = append(lines, "For small follow-ups, use the plan/checklist digest below as context and avoid extra plan_get/task_list calls unless the digest is insufficient.")
			}
		}
		if executeIntent {
			if activeTasks {
				lines = append(lines, "Execution intent detected. The plan and checklist already exist. Tell the user to run /mode build to execute the remaining pending tasks; do NOT re-plan or rewrite the checklist.")
			} else if len(taskList) > 0 {
				lines = append(lines, "Execution intent detected, but the current checklist is already complete. Tell the user execution is already complete unless they explicitly ask to refine or re-plan.")
			}
		}
		if len(taskList) > 0 {
			if activeTasks {
				lines = append(lines, fmt.Sprintf("Active checklist: %d pending, %d in progress, %d done. Plan mode does not execute — once the checklist is ready the user runs /mode build to work it.", pending, inProgress, done))
			} else {
				lines = append(lines, fmt.Sprintf("Previous checklist complete (%d tasks done). If refining, call task_list before deciding whether to preserve or replace it.", len(taskList)))
			}
		}
	case "build":
		if planSummary != "" {
			lines = append(lines, fmt.Sprintf("Approved plan in scope: %s. Use the digest below first; call plan_get only if that digest is insufficient.", planSummary))
		}
		if digest := compactPlanDigest(planDoc, taskList); hasPlanDoc && digest != "" {
			lines = append(lines, digest)
		}
		switch {
		case activeTasks:
			lines = append(lines, fmt.Sprintf("Checklist: %d pending, %d in progress, %d done. Take the next pending task in order, mark it in_progress with task_update, do the work directly with read_file + edit_file/write_file/apply_patch (each mutation requires user approval), then task_update(status=\"completed\"). Repeat until no pending tasks remain.", pending, inProgress, done))
		case len(taskList) > 0:
			lines = append(lines, "Checklist already complete. Tell the user there is nothing pending and stop.")
		default:
			lines = append(lines, "No checklist found. Tell the user to switch back to plan mode (/mode plan) to draft a plan first.")
		}
	}

	return strings.Join(lines, "\n")
}

func compactPlanDigest(doc plans.Document, taskList []tasks.Task) string {
	if strings.TrimSpace(doc.Summary) == "" && strings.TrimSpace(doc.Approach) == "" && len(doc.Validation) == 0 && len(taskList) == 0 {
		return ""
	}
	var parts []string
	if summary := strings.TrimSpace(doc.Summary); summary != "" {
		parts = append(parts, "summary="+truncatePlanDigest(summary, 120))
	}
	if approach := strings.TrimSpace(doc.Approach); approach != "" {
		parts = append(parts, "approach="+truncatePlanDigest(firstDigestLine(approach), 140))
	}
	if len(doc.Validation) > 0 {
		parts = append(parts, "validation="+truncatePlanDigest(strings.Join(doc.Validation[:minInt(len(doc.Validation), 2)], "; "), 120))
	}
	if preview := compactChecklistPreview(taskList); preview != "" {
		parts = append(parts, "tasks="+preview)
	}
	if len(parts) == 0 {
		return ""
	}
	return "Plan digest: " + strings.Join(parts, " | ")
}

func compactChecklistPreview(taskList []tasks.Task) string {
	var active []string
	doneCount := 0
	for _, task := range taskList {
		switch task.Status {
		case "pending", "in_progress":
			if len(active) < 3 {
				active = append(active, fmt.Sprintf("%s:%s", task.Status, truncatePlanDigest(strings.TrimSpace(task.Title), 48)))
			}
		case "completed", "done":
			doneCount++
		}
	}
	if len(active) == 0 && doneCount == 0 {
		return ""
	}
	preview := strings.Join(active, "; ")
	if doneCount > 0 {
		if preview != "" {
			preview += "; "
		}
		preview += fmt.Sprintf("done:%d", doneCount)
	}
	return preview
}

func firstDigestLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func truncatePlanDigest(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit || limit <= 0 {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (r *Runtime) run(ctx context.Context, userMessage string, events chan<- Event) {
	turnStart := time.Now()
	r.mu.Lock()
	r.activeEvents = events
	r.mu.Unlock()
	r.turnStepsUsed.Store(0)
	r.turnReadOnlySteps.Store(0)
	r.turnMutatingSteps.Store(0)
	r.resetReadCache()
	defer func() {
		r.mu.Lock()
		r.activeEvents = nil
		r.LastTurnStepsUsed = int(r.turnStepsUsed.Load())
		r.LastTurnReadOnlySteps = int(r.turnReadOnlySteps.Load())
		r.LastTurnMutatingSteps = int(r.turnMutatingSteps.Load())
		r.LastTurnCacheHits = r.readCacheHits()
		r.mu.Unlock()
		r.LastTurnDuration = time.Since(turnStart)
	}()

	// Plan -> Build auto-handoff: if the user is in plan mode, has an
	// approved plan with active tasks, and expresses execution intent
	// ("yes", "execute", "run the plan", etc.), switch into build mode and
	// continue the turn there. This avoids the "plan keeps asking 'execute
	// this plan?' in a loop" trap when the user types confirmations the
	// planner cannot act on.
	if r.Mode == "plan" && looksLikePlanExecutionIntent(userMessage) {
		hasPlanDoc := false
		if r.Plans != nil {
			if _, ok, err := r.Plans.Current(); err == nil && ok {
				hasPlanDoc = true
			}
		}
		hasActive := r.hasActiveChecklistTasks()
		if hasPlanDoc && hasActive {
			_ = r.SetMode("build")
			events <- Event{Type: EventAssistantText, Text: "Auto-switched to build mode to execute the approved plan."}
		}
	}

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
	if r.Mode == "build" {
		// Build mode already gets the approved plan/checklist digest in the
		// prompt. Dropping recent session timeline here keeps follow-up build
		// turns materially smaller after a long plan/build/refine cycle.
		roleConfig.Context.Yarn.HistoryEvents = 0
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

	// Mode handoff: when the user just switched modes, give the model an
	// explicit one-turn signal so it adapts.
	handoff := ""
	if switchedFrom != "" && switchedFrom != r.Mode {
		handoff = fmt.Sprintf("MODE SWITCHED: %s → %s. ", strings.ToUpper(switchedFrom), strings.ToUpper(r.Mode))
		if r.Mode == "plan" {
			handoff += "Focus on plan_write for the full plan and todo_write/task_* for the executable checklist. Do not edit files directly. After plan_write and todo_write your turn ends and the user runs /mode build to execute. " +
				"If the user's request leaves scope, constraints, or success criteria ambiguous, start by calling ask_user (3-6 focused questions) before drafting the plan. " +
				"After the interview, call plan_write AND todo_write in the same turn so the user ends with both artifacts."
		}
		if r.Mode == "build" {
			handoff += "You are the executor: use the plan/checklist digest already in prompt first, and only call plan_get/task_list if the digest is insufficient. Then take the next pending task and edit files directly with edit_file/write_file/apply_patch (each prompts for approval). After each task call task_update(status=\"completed\"). Do NOT call execute_task, plan_write, or todo_write."
		}
		r.ModeSwitchedFrom = ""
	}
	explorerHandoff := ""
	if r.Mode == "plan" && strings.TrimSpace(r.PendingExplorerContext) != "" {
		explorerHandoff = r.PendingExplorerContext
		r.PendingExplorerContext = ""
	}
	// PendingBuildPreflight is reserved for future build-mode warm-up data
	// (e.g. relevant_files hints from a preflight scan). For now we just
	// drop it so it can't leak across turns.
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
	// Build mode legitimately needs more steps than plan mode: each
	// task runs read → analyze → edit → verify, and a feature with N
	// tasks easily multiplies that. MaxStepsBuild lets the user lift
	// the cap just for build without making plan-mode interviews wander.
	if r.Mode == "build" && r.Config.Runtime.MaxStepsBuild > maxSteps {
		maxSteps = r.Config.Runtime.MaxStepsBuild
	}
	r.LastModelUsed = model
	if bn, ok := provider.(llm.BackendNamer); ok {
		r.LastProviderUsed = bn.BackendName()
	} else {
		r.LastProviderUsed = provider.Name()
	}

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
	emptyResponses := 0
	noProgressSteps := 0
	lastFailedTool := ""
	consecutiveToolFailures := 0
	consecutiveReadOnly := 0
	readBudgetNudged := false
	lastBuildReadKey := ""
	sameBuildReadCount := 0
	planModeReprompts := 0
	taskAttempts := map[string]int{}
	blockedTaskRetries := map[string]bool{}
	lastExecuteTaskFailure := map[string]executeTaskFailureMeta{}

	// Step loop: ask_user turns do NOT count toward maxSteps since they are
	// blocked on human input, not model work. See the post-dispatch decrement
	// below so long interviews don't starve plan_write / todo_write / edits.
	for step := 0; step < maxSteps; step++ {
		// Bound the growth of tool-result payloads in the step history. Keeps
		// the last few verbatim and stubs the rest — the model can re-invoke
		// a tool if it still needs the detail. Build mode keeps fewer (2)
		// because each task fans out into more tool calls per turn.
		compactOldToolResults(messages, keepLastToolResultsForMode(r.Mode))

		req := llm.ChatRequest{
			Model:    model,
			Messages: messages,
			Tools:    toolDefs,
		}
		r.applySamplingDefaults(&req)

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

		// Narration-loop cancellation handoff. The streaming guard sets
		// r.lastNarrationCancel before returning when it kills a
		// reasoning-only or text-only loop. We emit a tailored reprompt
		// that names the offending line (a generic "use a tool" nudge
		// is too vague when the model just got cut off) and SHORT-CIRCUIT
		// the empty-response counter — reasoning-channel cancellations
		// produce no `accumulated` text and would otherwise be
		// double-punished as both a narration loop AND an empty turn.
		// Counts as a no_progress step so repeated narration cancels
		// still hit the cap and stop the agent gracefully.
		if r.lastNarrationCancel != "" {
			loopLine := r.lastNarrationCancel
			r.lastNarrationCancel = ""
			noProgressSteps++
			if noProgressSteps >= r.maxNoProgressSteps() {
				events <- Event{Type: EventError, Error: fmt.Errorf("stopped: %d narration-loop cancellations in a row — the model is stuck looping in reasoning. Switch back to plan mode and split the work into smaller, more concrete tasks", noProgressSteps)}
				events <- Event{Type: EventDone}
				return
			}
			messages = append(messages,
				llm.Message{Role: "assistant", Content: accumulated},
				llm.Message{Role: "user", Content: "Your last response was cancelled because you kept repeating the line " + summarizeForReprompt(loopLine) + " in your reasoning without producing a tool_call. STOP analysing. Pick the simplest pending task. If a task is too vague, call task_update with notes describing what you actually need to do, then call edit_file/write_file/apply_patch on the FIRST file you would touch — even a partial commit is better than another reasoning loop. If you genuinely cannot proceed, call ask_user with a single concrete question. Return exactly ONE tool call now, no prose."},
			)
			continue
		}

		// Handle empty responses from local models.
		if strings.TrimSpace(accumulated) == "" && len(toolCalls) == 0 {
			emptyResponses++
			if emptyResponses >= r.maxEmptyResponses() {
				events <- Event{Type: EventError, Error: fmt.Errorf("stopped: %d empty model responses in a row", emptyResponses)}
				events <- Event{Type: EventDone}
				return
			}
			messages = append(messages,
				llm.Message{Role: "assistant", Content: ""},
				llm.Message{Role: "user", Content: "You returned an empty response. Please provide an answer or use a tool to gather information."},
			)
			continue
		}
		emptyResponses = 0

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

			// Pre-execute parallelizable batches (multiple execute_task calls
			// in a single response) concurrently. Mixed batches and single-
			// tool batches stay sequential; only N>1 pure execute_task
			// batches take the fast path. Returns nil for the sequential
			// case so the loop below behaves identically.
			preComputed := r.maybePreExecuteParallelExecuteTasks(ctx, toolCalls, events)

			// Execute each tool call and append role:tool responses.
			allDone := true
			for _, tc := range toolCalls {
				agentCall := FromNativeToolCall(tc)
				events <- Event{Type: EventToolCall, ToolName: agentCall.Name, Input: agentCall.Input}

				var result *tools.Result
				var observation string
				cacheHitsBefore := r.readCacheHits()
				if pre, ok := preComputed[tc.ID]; ok {
					result, observation = pre.result, pre.observation
				} else {
					result, observation = r.executeTool(ctx, agentCall, events)
				}
				cacheHit := r.readCacheHits() > cacheHitsBefore
				if result != nil {
					events <- Event{Type: EventToolResult, ToolName: agentCall.Name, Result: result, Text: result.Summary}
				}
				r.turnStepsUsed.Add(1)
				if isMutatingToolCall(agentCall.Name) {
					r.turnMutatingSteps.Add(1)
				} else {
					r.turnReadOnlySteps.Add(1)
				}
				if agentCall.Name == "execute_task" && result != nil {
					taskID := executeTaskIDFromInput(agentCall.Input)
					if meta, ok := parseExecuteTaskFailureMeta(result); ok {
						lastExecuteTaskFailure[meta.TaskID] = meta
					} else if taskID != "" {
						delete(lastExecuteTaskFailure, taskID)
					}
				}
				if shouldRefundToolStep(agentCall.Name) {
					step--
				}
				if err := r.enforceRepeatedReadFileGuard(agentCall.Name, agentCall.Input, &lastBuildReadKey, &sameBuildReadCount); err != nil {
					events <- Event{Type: EventError, Error: err}
					events <- Event{Type: EventDone}
					return
				}
				// Read-budget guard with soft-nudge / explore-opt-out / hard-stop.
				// Cache-served reads did no real work — applyReadBudgetGuard
				// short-circuits on cacheHit so the cache helps the agent
				// rather than using up its exploration budget twice as fast.
				nudge, budget, hardStop := r.applyReadBudgetGuard(agentCall.Name, cacheHit, &consecutiveReadOnly, &noProgressSteps, &readBudgetNudged)
				if budget != nil {
					events <- Event{Type: EventReadBudget, ReadBudget: budget}
				}
				if hardStop != nil {
					events <- Event{Type: EventError, Error: hardStop}
					events <- Event{Type: EventDone}
					return
				}
				if nudge != "" {
					observation = observation + "\n\n[system] " + nudge
				}

				// Track consecutive failures.
				nativeToolFailed := result != nil && isToolFailureSummary(result.Summary)
				if nativeToolFailed && agentCall.Name == lastFailedTool {
					consecutiveToolFailures++
					if consecutiveToolFailures >= r.maxSameToolFailures() {
						if agentCall.Name == "execute_task" {
							taskID := executeTaskIDFromInput(agentCall.Input)
							if meta, ok := lastExecuteTaskFailure[taskID]; ok {
								events <- Event{Type: EventError, Error: fmt.Errorf("tool execute_task failed %d times - stopping: %s", consecutiveToolFailures, formatExecuteTaskRetryError(meta))}
								events <- Event{Type: EventDone}
								return
							}
						}
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

				// In plan mode, stop after todo_write — plan mode never executes.
				if r.Mode == "plan" && agentCall.Name == "todo_write" {
					msg := "Plan saved."
					if result != nil && strings.TrimSpace(result.Summary) != "" {
						msg += "\n" + result.Summary
					}
					msg += "\n\nReady to execute? Run /mode build to switch to build mode and work the checklist."
					events <- Event{Type: EventAssistantText, Text: msg}
					events <- Event{Type: EventDone}
					return
				}
				// In build mode, after every task_update that completes the
				// checklist, surface a final summary and stop the turn so the
				// LLM doesn't keep looping for more work.
				if r.Mode == "build" && agentCall.Name == "task_update" && !r.hasActiveChecklistTasks() {
					if result != nil && strings.TrimSpace(result.Summary) != "" {
						events <- Event{Type: EventAssistantText, Text: "All checklist tasks complete. " + result.Summary}
					} else {
						events <- Event{Type: EventAssistantText, Text: "All checklist tasks complete."}
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
			if r.Mode == "build" && r.hasActiveChecklistTasks() {
				noProgressSteps++
				if noProgressSteps >= r.maxNoProgressSteps() {
					events <- Event{Type: EventError, Error: fmt.Errorf("stopped: %d build response(s) in prose with checklist tasks still active", noProgressSteps)}
					events <- Event{Type: EventDone}
					return
				}
				messages = append(messages,
					llm.Message{Role: "assistant", Content: accumulated},
					llm.Message{Role: "user", Content: "You are in BUILD mode and checklist tasks still remain. Do not summarize or restate gaps. Return exactly one valid tool_call for the next action now: usually task_update(status=\"in_progress\"), read_file, edit_file, write_file, apply_patch, or run_command. Only answer in prose if you are blocked and need the user to switch back to plan mode."},
				)
				continue
			}
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
				noProgressSteps++
				if noProgressSteps >= r.maxNoProgressSteps() {
					events <- Event{Type: EventError, Error: fmt.Errorf("stopped: %d planner step(s) with no actionable progress", noProgressSteps)}
					events <- Event{Type: EventDone}
					return
				}
				if planModeReprompts < r.maxPlannerSummarySteps() {
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
		if r.Mode == "plan" && parsed.Call.Name == "execute_task" {
			taskID := executeTaskIDFromInput(parsed.Call.Input)
			if taskID != "" && taskAttempts[taskID] > 0 {
				if blockedTaskRetries[taskID] {
					if meta, ok := lastExecuteTaskFailure[taskID]; ok {
						events <- Event{Type: EventError, Error: fmt.Errorf("task %s already failed in this turn; refusing repeated execute_task retry: %s", taskID, formatExecuteTaskRetryError(meta))}
					} else {
						events <- Event{Type: EventError, Error: fmt.Errorf("task %s already failed in this turn due to timeout/no progress; refusing repeated execute_task retry", taskID)}
					}
					events <- Event{Type: EventDone}
					return
				}
				blockedTaskRetries[taskID] = true
				noProgressSteps++
				messages = append(messages,
					llm.Message{Role: "assistant", Content: accumulated},
					llm.Message{Role: "user", Content: fmt.Sprintf("Task %s already failed in this turn. Do not retry it. Report the blocker or choose a different remaining task.", taskID)},
				)
				continue
			}
		}
		events <- Event{Type: EventToolCall, ToolName: parsed.Call.Name, Input: parsed.Call.Input}

		cacheHitsBefore := r.readCacheHits()
		result, observation := r.executeTool(ctx, parsed.Call, events)
		cacheHit := r.readCacheHits() > cacheHitsBefore
		if result != nil {
			events <- Event{Type: EventToolResult, ToolName: parsed.Call.Name, Result: result, Text: result.Summary}
		}
		r.turnStepsUsed.Add(1)
		if isMutatingToolCall(parsed.Call.Name) {
			r.turnMutatingSteps.Add(1)
		} else {
			r.turnReadOnlySteps.Add(1)
		}

		// Don't charge non-productive calls against the step budget:
		//   ask_user       — blocked on human input
		//   task_list / plan_get / git_status — read-only status pings
		// These should never starve plan_write / todo_write / file edits.
		switch parsed.Call.Name {
		case "ask_user", "task_list", "plan_get", "git_status":
			step--
		}

		if err := r.enforceRepeatedReadFileGuard(parsed.Call.Name, parsed.Call.Input, &lastBuildReadKey, &sameBuildReadCount); err != nil {
			events <- Event{Type: EventError, Error: err}
			events <- Event{Type: EventDone}
			return
		}

		// No-progress stall guard: the planner should be dispatching
		// execute_task or mutating tools (plan_write / todo_write /
		// task_update). Long runs of read-only exploration without any of
		// those signals an aimless loop; stop so the cap isn't burned.
		// A cache-served read did no real work — exempt it so the cache
		// doesn't help latency at the cost of burning the read budget.
		// Read-budget guard with soft-nudge / explore-opt-out / hard-stop.
		// Shared with the native tool-call path above via applyReadBudgetGuard
		// so both paths stop with the same mode-aware messaging instead of
		// the legacy "dispatch execute_task" string in non-build modes.
		nudge, budget, hardStop := r.applyReadBudgetGuard(parsed.Call.Name, cacheHit, &consecutiveReadOnly, &noProgressSteps, &readBudgetNudged)
		if budget != nil {
			events <- Event{Type: EventReadBudget, ReadBudget: budget}
		}
		if hardStop != nil {
			events <- Event{Type: EventError, Error: hardStop}
			events <- Event{Type: EventDone}
			return
		}
		if nudge != "" {
			observation = observation + "\n\n[system] " + nudge
		}

		if parsed.Call.Name == "execute_task" && result != nil {
			taskID := executeTaskIDFromInput(parsed.Call.Input)
			if meta, ok := parseExecuteTaskFailureMeta(result); ok {
				lastExecuteTaskFailure[meta.TaskID] = meta
				switch meta.FailureKind {
				case "timeout", "no_progress":
					taskAttempts[meta.TaskID]++
				}
			} else if taskID != "" {
				delete(lastExecuteTaskFailure, taskID)
			}
		}

		// Track consecutive failures of the same tool to break infinite loops.
		toolFailed := result != nil && isToolFailureSummary(result.Summary)
		if toolFailed && parsed.Call.Name == lastFailedTool {
			consecutiveToolFailures++
			if consecutiveToolFailures >= r.maxSameToolFailures() {
				if parsed.Call.Name == "execute_task" {
					taskID := executeTaskIDFromInput(parsed.Call.Input)
					if meta, ok := lastExecuteTaskFailure[taskID]; ok {
						events <- Event{Type: EventError, Error: fmt.Errorf("tool execute_task failed %d times in a row - stopping to avoid infinite loop: %s", consecutiveToolFailures, formatExecuteTaskRetryError(meta))}
						events <- Event{Type: EventDone}
						return
					}
				}
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

		// In plan mode, stop after todo_write — plan mode never executes.
		// Use a hardcoded handoff message (not another LLM call) so the
		// planner cannot keep asking "execute this plan?" in a loop while
		// staying in plan mode.
		if r.Mode == "plan" && parsed.Call.Name == "todo_write" {
			msg := "Plan saved."
			if result != nil && strings.TrimSpace(result.Summary) != "" {
				msg += "\n" + result.Summary
			}
			msg += "\n\nReady to execute? Run /mode build (or type 'execute the plan') to switch to build mode and work the checklist."
			events <- Event{Type: EventAssistantText, Text: msg}
			events <- Event{Type: EventDone}
			return
		}
		// In build mode, stop once the checklist is fully complete.
		if r.Mode == "build" && parsed.Call.Name == "task_update" && !r.hasActiveChecklistTasks() {
			msg := "All checklist tasks complete."
			if result != nil && strings.TrimSpace(result.Summary) != "" {
				msg += " " + result.Summary
			}
			events <- Event{Type: EventAssistantText, Text: msg}
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
// no-progress stall guard in build mode. task_create counts here too —
// in build mode the executor uses it to externalize newly-discovered
// work so it doesn't have to keep the discovery in prose only and risk
// re-rediscovering it on the next read loop.
func isMutatingToolCall(name string) bool {
	switch name {
	case "write_file", "edit_file", "apply_patch", "run_command",
		"plan_write", "todo_write", "task_update", "task_create",
		"task_add", "task_complete":
		return true
	}
	return false
}

func shouldRefundToolStep(name string) bool {
	switch name {
	case "ask_user", "task_list", "plan_get", "git_status":
		return true
	case "execute_task":
		// The real work runs inside the builder subagent's separate step
		// budget (subagentStepLimit). Charging the parent for orchestrating
		// the dispatch made a 7-task plan eat ~10 parent steps per task and
		// hit the cap before finishing. Per-task accounting lives on the
		// subagent now; the parent's MaxSteps governs orchestration only.
		return true
	}
	return false
}

// isReadOnlyExploration returns true for tools that merely inspect state
// without changing it. Long runs of these with no mutation in between signal
// an aimless-exploration stall.
func isReadOnlyExploration(name string) bool {
	switch name {
	case "read_file", "read_files", "list_files", "search_text", "search_files", "git_diff":
		return true
	}
	return false
}

// applyReadBudgetGuard implements the consecutive-read-only budget state
// machine used by BOTH the native tool-call path and the text-based fallback.
// It centralizes (a) explore-mode opt-out, (b) the soft-nudge on first
// crossing of the threshold, (c) the hard-stop after the post-nudge grace
// window, and (d) the per-call EventReadBudget snapshot for the TUI.
//
// Returns:
//   - nudge: a system message the caller should append to the next user
//     observation when the model first crosses the threshold. "" otherwise.
//   - budget: a snapshot for the caller to emit as EventReadBudget. nil when
//     nothing changed (mutating tool / cache hit / non-exploration tool).
//   - hardStop: a terminal error when the model exhausted the grace window.
//     The caller emits EventError + EventDone and returns.
//
// All three may be zero values for a single call. The caller is expected to
// check them in order (budget event → hardStop → nudge) and act accordingly.
func (r *Runtime) applyReadBudgetGuard(name string, cacheHit bool, consecutiveReadOnly, noProgressSteps *int, readBudgetNudged *bool) (nudge string, budget *ReadBudgetState, hardStop error) {
	if isMutatingToolCall(name) || name == "execute_task" {
		*consecutiveReadOnly = 0
		*noProgressSteps = 0
		*readBudgetNudged = false
		return "", nil, nil
	}
	if !isReadOnlyExploration(name) || cacheHit {
		return "", nil, nil
	}
	*consecutiveReadOnly++
	// Explore mode is read-only by design — the repeated-same-file guard and
	// max_steps still cap genuine loops, but a "consecutive reads" budget
	// would only fire spuriously mid-investigation.
	if r.Mode == "explore" {
		r.recordReadBudgetSnapshot(*consecutiveReadOnly, 0)
		return "", &ReadBudgetState{
			Consumed:  *consecutiveReadOnly,
			Threshold: 0,
			Mode:      r.Mode,
			Nudged:    false,
		}, nil
	}
	threshold := r.activeReadBudget()
	r.recordReadBudgetSnapshot(*consecutiveReadOnly, threshold)
	snap := &ReadBudgetState{
		Consumed:  *consecutiveReadOnly,
		Threshold: threshold,
		Mode:      r.Mode,
		Nudged:    *readBudgetNudged,
	}
	if threshold > 0 && *consecutiveReadOnly >= threshold {
		if !*readBudgetNudged {
			*readBudgetNudged = true
			return readBudgetNudgeForMode(r.Mode, *consecutiveReadOnly, threshold), snap, nil
		}
		if *consecutiveReadOnly >= threshold+r.readBudgetGracePastNudge() {
			return "", snap, fmt.Errorf("stopped: %d consecutive read-only tool calls — %s", *consecutiveReadOnly, readBudgetHardStopForMode(r.Mode))
		}
	}
	return "", snap, nil
}

// repeatedReadFileGuardThreshold is the number of identical read_file
// calls (same path AND same offset/limit) tolerated before the guard
// fires. Tuned higher than the original 3 because Qwen-class models
// frequently re-issue the same call mid-reasoning ("let me re-check
// what's in here") and the read cache already serves repeats with an
// inline annotation telling the model it just looped — the cap is the
// safety net for when the model also ignores that signal.
const repeatedReadFileGuardThreshold = 5

func (r *Runtime) enforceRepeatedReadFileGuard(name string, input json.RawMessage, lastBuildReadKey *string, sameBuildReadCount *int) error {
	if r.Mode != "build" {
		*lastBuildReadKey = ""
		*sameBuildReadCount = 0
		return nil
	}
	if isMutatingToolCall(name) || name == "execute_task" {
		*lastBuildReadKey = ""
		*sameBuildReadCount = 0
		return nil
	}
	if name != "read_file" {
		*lastBuildReadKey = ""
		*sameBuildReadCount = 0
		return nil
	}
	// Use the FULL input shape (path + offset + limit) as the key so a
	// legitimate paginated walk through a large file ("offset=1,
	// offset=151, offset=301") counts each page as progress, not as a
	// repeat. Re-reading the same window is what we want to catch —
	// reading new windows is what offset/limit was added for.
	key := readFileFingerprintFromInput(input)
	if key == "" {
		*lastBuildReadKey = ""
		*sameBuildReadCount = 0
		return nil
	}
	if strings.EqualFold(key, *lastBuildReadKey) {
		*sameBuildReadCount++
	} else {
		*lastBuildReadKey = key
		*sameBuildReadCount = 1
	}
	if *sameBuildReadCount >= repeatedReadFileGuardThreshold {
		path := readFilePathFromInput(input)
		return fmt.Errorf("stopped: repeated read_file on %s with the same offset/limit in build mode (%d times) with no edits yet — stop re-reading the same window and either edit something, paginate to a different range, or ask for refinement", path, *sameBuildReadCount)
	}
	return nil
}

func readFilePathFromInput(input json.RawMessage) string {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return ""
	}
	return strings.TrimSpace(req.Path)
}

// readFileFingerprintFromInput returns a canonical "path|offset|limit"
// string for a read_file payload. Two reads collide on this
// fingerprint only when they would return identical bytes (modulo
// disk mutations); paginated reads of the same path with different
// offsets/limits produce distinct fingerprints and don't count toward
// the repeat guard.
func readFileFingerprintFromInput(input json.RawMessage) string {
	var req struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return ""
	}
	path := strings.TrimSpace(req.Path)
	if path == "" {
		return ""
	}
	return fmt.Sprintf("%s|%d|%d", path, req.Offset, req.Limit)
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

	emitProgress("waiting_on_provider", false)
	requestCtx, cancel := withOptionalTimeout(ctx, r.requestTimeout())
	defer cancel()
	stream, err := r.streamProvider(requestCtx, provider, req)
	if err != nil {
		if len(req.Tools) > 0 && llm.IsToolCallingUnsupported(err) {
			fallbackReq := req
			fallbackReq.Tools = nil
			return r.streamResponseWithInput(ctx, provider, fallbackReq, step, estimateRequestTokens(fallbackReq), events)
		}
		if !r.retryOnProviderTimeout() && (llm.IsProviderTimeout(err) || llm.IsProviderUnavailable(err)) {
			return "", nil, nil, err
		}
		// Fallback to non-streaming Chat if Stream fails.
		chatCtx, chatCancel := withOptionalTimeout(ctx, r.requestTimeout())
		defer chatCancel()
		resp, chatErr := provider.Chat(chatCtx, req)
		if chatErr != nil {
			if len(req.Tools) > 0 && llm.IsToolCallingUnsupported(chatErr) {
				fallbackReq := req
				fallbackReq.Tools = nil
				return r.streamResponseWithInput(ctx, provider, fallbackReq, step, estimateRequestTokens(fallbackReq), events)
			}
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

	// Narration-loop guard. Small/medium local models occasionally
	// degenerate into self-talk loops where they keep emitting "Let me
	// make this edit" / "OK, I'm going to do it now" / "Alright, let me
	// start with X" without ever producing a tool_call tag. The per-turn
	// no_progress guard catches this AFTER the response finishes — by
	// then the user has already watched 2000+ tokens of useless prose
	// stream to the viewport. We watch the streamed text for repeated
	// substantial lines and cancel the request the moment a loop is
	// confirmed. The outer runtime then sees an empty/short response and
	// applies its normal "no tool call" reprompt path.
	//
	// The guard runs against BOTH the regular text stream and the
	// reasoning_content stream (Qwen3, GPT-OSS, etc.) — many models do
	// their narrating inside the dedicated reasoning channel, so a
	// text-only guard would miss the case entirely. The seen-map is
	// shared because a single repeated line across any mix of channels
	// is still loop-shaped behaviour.
	loopSeen := map[string]int{}
	loopTextLineStart := 0
	var loopReasoningBuf strings.Builder
	loopReasoningLineStart := 0
	const loopMinLineLen = 15
	const loopThreshold = 3
	checkLoopGuard := func(buf string, lineStart *int) (string, int, bool) {
		for {
			rest := buf[*lineStart:]
			nl := strings.IndexByte(rest, '\n')
			if nl < 0 {
				return "", 0, false
			}
			line := rest[:nl]
			*lineStart += nl + 1
			norm := strings.ToLower(strings.TrimSpace(line))
			if len(norm) < loopMinLineLen {
				continue
			}
			if len(norm) > 100 {
				norm = norm[:100]
			}
			loopSeen[norm]++
			if loopSeen[norm] >= loopThreshold {
				return norm, loopSeen[norm], true
			}
		}
	}

	// inReasoning tracks whether the most recent stream chunk was a
	// reasoning_content delta (Qwen, GPT-OSS, etc. emit reasoning over a
	// dedicated SSE field separate from content). The TUI renders thinking
	// when it sees literal <think>...</think> in the assistant deltas, so
	// we synthesize those tags around the reasoning stream — the model
	// never emits the tags itself but the user-facing semantics are the
	// same. Reasoning text is intentionally NOT appended to `text` so the
	// tool-call scanner and final assistant text stay clean.
	inReasoning := false
	openReasoning := func() {
		if !inReasoning {
			inReasoning = true
			events <- Event{Type: EventAssistantDelta, Text: "<think>"}
		}
	}
	closeReasoning := func() {
		if inReasoning {
			inReasoning = false
			events <- Event{Type: EventAssistantDelta, Text: "</think>"}
		}
	}

	for event := range stream {
		switch event.Type {
		case "reasoning":
			if firstTokenAt.IsZero() {
				firstTokenAt = time.Now()
			}
			if event.Text == "" {
				continue
			}
			openReasoning()
			events <- Event{Type: EventAssistantDelta, Text: event.Text}
			emitProgress("streaming", false)
			loopReasoningBuf.WriteString(event.Text)
			if line, count, hit := checkLoopGuard(loopReasoningBuf.String(), &loopReasoningLineStart); hit {
				cancel()
				for range stream {
				}
				r.lastNarrationCancel = line
				// Drop the half-streamed reasoning from the TUI so the
				// next turn's stream doesn't accumulate on top of it —
				// otherwise the user sees the cancelled monologue plus
				// the recovered turn's output side by side, which reads
				// as duplicated lines after the dust settles.
				events <- Event{Type: EventClearStreaming}
				events <- Event{Type: EventError, Error: fmt.Errorf("narration loop detected in reasoning (line %q repeated %d times); cancelled stream and re-prompting for a tool_call", line, count)}
				return text.String(), toolCalls, usage, nil
			}
		case "text":
			if firstTokenAt.IsZero() {
				firstTokenAt = time.Now()
			}
			closeReasoning()
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

				// Narration-loop check on the regular text channel.
				// Scoped to the pre-tool-call window so legitimate long
				// final answers (no tool call needed) aren't penalized
				// after they've structurally committed. On detection we
				// cancel the SSE, drain the channel, emit a visible
				// warning, and return without error so the outer
				// runtime's existing "no tool call" branch reprompts.
				if line, count, hit := checkLoopGuard(accumulated, &loopTextLineStart); hit {
					cancel()
					for range stream {
					}
					r.lastNarrationCancel = line
					events <- Event{Type: EventClearStreaming}
					events <- Event{Type: EventError, Error: fmt.Errorf("narration loop detected (line %q repeated %d times); cancelled stream and re-prompting for a tool_call", line, count)}
					return text.String(), toolCalls, usage, nil
				}
			}
		case "tool_calls":
			closeReasoning()
			toolCalls = event.ToolCalls
			emitProgress("tool_call", false)
		case "usage":
			usage = event.Usage
			emitProgress("streaming", false)
		case "error":
			closeReasoning()
			return text.String(), toolCalls, usage, event.Error
		case "done":
			// Stream finished.
		}
	}
	// Stream may have ended mid-reasoning (no text follow-up). Close the
	// synthesized tag so the UI's <think> filter sees a balanced block.
	closeReasoning()
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
