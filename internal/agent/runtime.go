package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"forge/internal/config"
	contextbuilder "forge/internal/context"
	"forge/internal/hooks"
	"forge/internal/llm"
	"forge/internal/patch"
	"forge/internal/permissions"
	"forge/internal/tasks"
	"forge/internal/tools"
)

const (
	EventAssistantText  = "assistant_text"
	EventAssistantDelta = "assistant_delta"
	EventClearStreaming  = "clear_streaming"
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
	Error    error
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
	Question string
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
	Policy    SprintPolicy
	Commands  permissions.CommandPolicy
	Tasks     *tasks.Store
	Subagents SubagentRegistry
	Hooks           *hooks.Runner
	Parsers         *ParserRegistry
	MaxParseRetries int
	LastTokensUsed    int
	LastTokensBudget  int
	LastModelUsed     string
	LastParserUsed    string
	LastTurnDuration  time.Duration
	LastTurnTokensIn  int
	LastTurnTokensOut int
	mu              sync.Mutex
	undoStack       []UndoEntry
}

func NewRuntime(cwd string, cfg config.Config, registry *tools.Registry, providers *llm.Registry) *Runtime {
	return &Runtime{
		CWD:       cwd,
		Config:    cfg,
		Tools:     registry,
		Providers: providers,
		Builder:   contextbuilder.NewBuilder(cwd, cfg, registry),
		MaxSteps:  24,
		Mode:      "build",
		Policy:    NewSprintPolicy(),
		Commands:  permissions.DefaultCommandPolicy(),
		Tasks:     tasks.New(cwd),
		Subagents: DefaultSubagents(),
		Parsers:   DefaultParsers(),
	}
}

// SetMode switches the agent to a different operating mode.
func (r *Runtime) SetMode(name string) error {
	mode, ok := GetMode(name)
	if !ok {
		return fmt.Errorf("unknown mode: %s (available: %s)", name, strings.Join(ModeNames(), ", "))
	}
	r.Mode = name
	r.Policy = mode.Policy
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

func (r *Runtime) Run(ctx context.Context, userMessage string) <-chan Event {
	events := make(chan Event)
	go func() {
		defer close(events)
		r.run(ctx, userMessage, events)
	}()
	return events
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

	snapshot := r.Builder.Build(userMessage)
	r.LastTurnTokensOut = 0
	r.LastTokensBudget = snapshot.TokensBudget

	// Include pending tasks so the model knows what to execute.
	pendingTasks := ""
	if r.Tasks != nil {
		if list, err := r.Tasks.List(); err == nil && len(list) > 0 {
			pendingTasks = tasks.Format(list)
		}
	}

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt(snapshot, supportsTools, r.Mode, r.Policy)},
		{Role: "user", Content: userPrompt(snapshot, userMessage, pendingTasks)},
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
		maxSteps = 24
	}
	// Plan mode needs fewer steps — just read + create plan.
	if r.Mode == "plan" && maxSteps > 6 {
		maxSteps = 6
	}
	model := r.Config.Models["chat"]
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

	for step := 0; step < maxSteps; step++ {
		req := llm.ChatRequest{
			Model:    model,
			Messages: messages,
			Tools:    toolDefs,
		}

		// Update context token count from current message history.
		msgChars := 0
		for _, msg := range messages {
			msgChars += len(msg.Content)
		}
		r.LastTokensUsed = msgChars / 4

		// Stream the response for real-time token display.
		accumulated, toolCalls, err := r.streamResponse(ctx, provider, req, events)
		r.LastTurnTokensOut += len(accumulated) / 4
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
			events <- Event{Type: EventError, Error: fmt.Errorf("parse error (attempt %d/%d): %w", parseFailures, maxRetries, err)}
			if parseFailures >= maxRetries {
				// Give up on tool calling — emit clean text without tool_call XML.
				clean := accumulated
				if idx := strings.Index(clean, "<tool_call>"); idx >= 0 {
					clean = strings.TrimSpace(clean[:idx])
				}
				if clean != "" {
					events <- Event{Type: EventAssistantText, Text: clean}
				}
				events <- Event{Type: EventDone}
				return
			}
			messages = append(messages,
				llm.Message{Role: "assistant", Content: accumulated},
				llm.Message{Role: "user", Content: "Tool call parse error: " + err.Error() + "\nReturn a final answer or a valid <tool_call>{...}</tool_call> block."},
			)
			continue
		}
		parseFailures = 0 // reset on successful parse

		if !parsed.Found {
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
			summaryAcc, _, err := r.streamResponse(ctx, provider, llm.ChatRequest{Model: model, Messages: messages}, events)
			_ = summaryAcc
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

	events <- Event{Type: EventError, Error: fmt.Errorf("agent stopped after %d steps", maxSteps)}
	events <- Event{Type: EventDone}
}

// streamResponse streams the LLM response, emitting deltas to the event channel,
// and returns the accumulated text content and any tool calls.
func (r *Runtime) streamResponse(ctx context.Context, provider llm.Provider, req llm.ChatRequest, events chan<- Event) (string, []llm.ToolCall, error) {
	stream, err := provider.Stream(ctx, req)
	if err != nil {
		// Fallback to non-streaming Chat if Stream fails.
		resp, chatErr := provider.Chat(ctx, req)
		if chatErr != nil {
			return "", nil, chatErr
		}
		return resp.Content, resp.ToolCalls, nil
	}

	var text strings.Builder
	var toolCalls []llm.ToolCall
	toolCallSeen := false

	for event := range stream {
		switch event.Type {
		case "text":
			text.WriteString(event.Text)
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
		case "error":
			return text.String(), toolCalls, event.Error
		case "done":
			// Stream finished.
		}
	}
	return text.String(), toolCalls, nil
}

