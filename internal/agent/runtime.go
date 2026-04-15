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
	EventAssistantText  = "assistant_text"
	EventAssistantDelta = "assistant_delta"
	EventModelProgress  = "model_progress"
	EventClearStreaming = "clear_streaming"
	EventToolCall       = "tool_call"
	EventToolResult     = "tool_result"
	EventApproval       = "approval_required"
	EventAskUser        = "ask_user"
	EventError          = "error"
	EventDone           = "done"
)

type Event struct {
	Type     string
	Text     string
	ToolName string
	Input    json.RawMessage
	Result   *tools.Result
	Approval *ApprovalRequest
	AskUser  *AskUserRequest
	Progress *ModelProgress
	Error    error
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
	Policy                SprintPolicy
	Commands              permissions.CommandPolicy
	Plans                 *plans.Store
	Tasks                 *tasks.Store
	Subagents             SubagentRegistry
	Hooks                 *hooks.Runner
	Parsers               *ParserRegistry
	MaxParseRetries       int
	LastTokensUsed        int
	LastTokensBudget      int
	LastModelUsed         string
	LastParserUsed        string
	// ActiveParserName is the parser selected at model-load time (via
	// SetChatModel). Cached so the TUI can display it without re-running
	// ForModel every frame. The per-turn LastParserUsed still tracks which
	// parser actually handled the most recent response.
	ActiveParserName   string
	ActiveModelFamily  string
	currentLoadedModel string
	loadedModels       map[string]bool
	LastTurnDuration   time.Duration
	LastTurnTokensIn   int
	LastTurnTokensOut  int
	mu                 sync.Mutex
	undoStack          []UndoEntry
	// loadMu serializes actual provider.LoadModel calls. Without this, two
	// concurrent subagents with different role models race to swap the
	// currently-loaded model on LM Studio, causing thrash and starving the
	// real turn. Held only around the LoadModel call itself — not inference.
	loadMu sync.Mutex
	// EventTee, if set, receives a copy of every event emitted by Run. Used
	// by /remote-control to broadcast to connected web clients.
	EventTee EventTee
}

func NewRuntime(cwd string, cfg config.Config, registry *tools.Registry, providers *llm.Registry) *Runtime {
	return &Runtime{
		CWD:       cwd,
		Config:    cfg,
		Tools:     registry,
		Providers: providers,
		Builder:   contextbuilder.NewBuilder(cwd, cfg, registry),
		MaxSteps:  40,
		Mode:      "build",
		Policy:    NewSprintPolicy(),
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

func (r *Runtime) EnsureRoleModelLoaded(ctx context.Context, provider llm.Provider, role string) error {
	if r == nil || provider == nil || !r.Config.ModelLoading.Enabled {
		return nil
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
	if strategy == "parallel" {
		if r.loadedModels != nil && r.loadedModels[modelID] {
			r.mu.Unlock()
			return nil
		}
	} else if r.currentLoadedModel == modelID {
		r.mu.Unlock()
		return nil
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
	if strategy == "parallel" {
		if r.loadedModels != nil && r.loadedModels[modelID] {
			r.mu.Unlock()
			return nil
		}
	} else if r.currentLoadedModel == modelID {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	loadCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	if err := provider.LoadModel(loadCtx, modelID, llm.LoadConfig{
		ContextLength:  contextLength,
		FlashAttention: true,
		ParallelSlots:  r.Config.ModelLoading.ParallelSlots,
	}); err != nil {
		// Best-effort load: LM Studio may already have the model resident
		// (e.g. JIT cache or a prior session) and still reject an explicit
		// load request. Emit a warning via stderr and let the subsequent
		// Chat/Stream call be the real source of truth — if the model truly
		// isn't available, that call will fail with an actionable error.
		fmt.Fprintf(os.Stderr, "model-load warning (%s=%s): %v — proceeding anyway\n", role, modelID, err)
		r.MarkModelLoaded(modelID)
		return nil
	}
	r.MarkModelLoaded(modelID)
	return nil
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
func (r *Runtime) SetMode(name string) error {
	mode, ok := GetMode(name)
	if !ok {
		return fmt.Errorf("unknown mode: %s (available: %s)", name, strings.Join(ModeNames(), ", "))
	}
	previous := r.Mode
	r.Mode = name
	r.Policy = mode.Policy
	// Fire a one-shot handoff on the next turn so the model knows it switched
	// context. Especially important for plan→build so it executes instead of
	// re-planning.
	if previous != name {
		r.ModeSwitchedFrom = previous
	}
	return nil
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

func buildModeExecutionReprompt() string {
	return "You are in BUILD mode. The previous response was only a plan/checklist and did not execute the user's request. Do not provide another plan. Start implementing now using tool_call blocks. Create or edit the required files with write_file/edit_file/apply_patch, then run a relevant verification command. If you need the current files first, call list_files or read_file."
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

func (r *Runtime) run(ctx context.Context, userMessage string, events chan<- Event) {
	turnStart := time.Now()
	defer func() {
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
	snapshot := r.buildSnapshot(userMessage, roleConfig)
	r.LastTurnTokensOut = 0
	r.LastTokensBudget = snapshot.TokensBudget

	// Plan pointer: we don't dump the whole plan into the user prompt anymore
	// — the model calls task_list when it actually needs to see it. Injecting
	// the full list every turn (a) bloats context, and (b) encouraged the
	// model to re-emit the plan via todo_write, triggering our destructive
	// overwrite bug. Just tell it how many tasks exist and what state.
	planBlock := ""
	if r.Plans != nil {
		if doc, ok, err := r.Plans.Current(); err == nil && ok {
			summary := strings.TrimSpace(doc.Summary)
			if summary == "" {
				summary = "(no summary)"
			}
			planBlock = fmt.Sprintf("Plan document exists: %s. Call plan_get to read it before refining. Keep the executable checklist separate.", summary)
		}
	}
	if r.Tasks != nil {
		if list, err := r.Tasks.List(); err == nil && len(list) > 0 {
			pending, inProgress, done := 0, 0, 0
			for _, t := range list {
				switch t.Status {
				case "pending":
					pending++
				case "in_progress":
					inProgress++
				case "completed", "done":
					done++
				}
			}
			if pending+inProgress > 0 {
				taskBlock := fmt.Sprintf("Checklist: %d pending, %d in progress, %d done. Call task_list to read it; use task_update to mark progress. Do NOT call todo_write unless the user asks for a fresh checklist.", pending, inProgress, done)
				if planBlock != "" {
					planBlock += "\n" + taskBlock
				} else {
					planBlock = taskBlock
				}
			} else {
				taskBlock := fmt.Sprintf("Previous checklist complete (%d tasks done). Do NOT rewrite it. Respond to the user's current request.", len(list))
				if planBlock != "" {
					planBlock += "\n" + taskBlock
				} else {
					planBlock = taskBlock
				}
			}
		}
	}

	// Mode handoff: when the user just switched modes, give the model an
	// explicit one-turn signal so it adapts (e.g. plan→build should execute
	// the existing plan, not regenerate it).
	handoff := ""
	if r.ModeSwitchedFrom != "" && r.ModeSwitchedFrom != r.Mode {
		handoff = fmt.Sprintf("MODE SWITCHED: %s → %s. ", strings.ToUpper(r.ModeSwitchedFrom), strings.ToUpper(r.Mode))
		if r.ModeSwitchedFrom == "plan" && r.Mode == "build" {
			handoff += "Execute the existing plan above without rewriting it, unless the user explicitly asks for a new plan."
		} else if r.Mode == "plan" {
			handoff += "Focus on plan_write for the full plan and todo_write/task_* for the executable checklist. Do not edit files. " +
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
	buildPreflight := ""
	if r.Mode == "build" && strings.TrimSpace(r.PendingBuildPreflight) != "" {
		buildPreflight = r.PendingBuildPreflight
		r.PendingBuildPreflight = ""
	}

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt(snapshot, supportsTools, r.Mode, r.Policy)},
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
		toolDefs = r.Tools.ToolDefs(nil) // all tools
	}

	maxRetries := r.MaxParseRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}
	parseFailures := 0
	lastFailedTool := ""
	consecutiveToolFailures := 0
	consecutiveReadOnly := 0
	planOnlyReprompts := 0
	planModeReprompts := 0

	// Step loop: ask_user turns do NOT count toward maxSteps since they are
	// blocked on human input, not model work. See the post-dispatch decrement
	// below so long interviews don't starve plan_write / todo_write / edits.
	for step := 0; step < maxSteps; step++ {
		req := llm.ChatRequest{
			Model:    model,
			Messages: messages,
			Tools:    toolDefs,
		}

		// Update context token count from current message history.
		r.LastTurnTokensIn = estimateRequestTokens(req)
		r.LastTokensUsed = r.LastTurnTokensIn

		// Stream the response for real-time token display.
		accumulated, toolCalls, usage, err := r.streamResponse(ctx, provider, req, step+1, events)
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
			if r.Mode == "build" && planOnlyReprompts < 2 && looksLikePlanOnlyResponse(accumulated) {
				planOnlyReprompts++
				events <- Event{Type: EventClearStreaming}
				messages = append(messages,
					llm.Message{Role: "assistant", Content: accumulated},
					llm.Message{Role: "user", Content: buildModeExecutionReprompt()},
				)
				continue
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

		// No-progress stall guard: in build mode, if the agent keeps doing
		// reconnaissance (read_file, list_files, search_*) without any
		// mutating action (write_file, edit_file, apply_patch, todo_write,
		// task_update, run_command, apply_plan) for 10 steps in a row,
		// stop. Prevents the cap from being burned on aimless exploration.
		if r.Mode == "build" {
			if isMutatingToolCall(parsed.Call.Name) {
				consecutiveReadOnly = 0
			} else if isReadOnlyExploration(parsed.Call.Name) {
				consecutiveReadOnly++
				if consecutiveReadOnly >= 10 {
					events <- Event{Type: EventError, Error: fmt.Errorf("stopped: 10 consecutive read-only tool calls with no edits — switch goals or call apply_patch/write_file when ready")}
					events <- Event{Type: EventDone}
					return
				}
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

		messages = append(messages,
			llm.Message{Role: "assistant", Content: accumulated},
			llm.Message{Role: "user", Content: observation},
		)
		if parsed.After != "" {
			messages = append(messages, llm.Message{Role: "user", Content: "Text after tool call was ignored until the tool result was available: " + parsed.After})
		}
	}

	hint := ""
	if r.Mode == "plan" {
		hint = " (plan mode cap; switch to build with Shift+Tab or `/mode build` for longer iterations)"
	}
	events <- Event{Type: EventError, Error: fmt.Errorf("agent stopped after %d steps in %s mode%s", maxSteps, r.Mode, hint)}
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
	inputTokens := estimateRequestTokens(req)
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
				if idx := strings.Index(accumulated, "<tool_call>"); idx >= 0 {
					toolCallSeen = true
					// Don't emit any more deltas — the text will be processed by ParseToolCall.
				} else {
					events <- Event{Type: EventAssistantDelta, Text: event.Text}
				}
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
