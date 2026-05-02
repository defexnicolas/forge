package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	contextbuilder "forge/internal/context"
	"forge/internal/llm"
	"forge/internal/permissions"
	"forge/internal/tools"
)

type Subagent struct {
	Name         string
	Description  string
	ModelRole    string
	ContextMode  string
	AllowedTools []string
}

type SubagentRequest struct {
	Agent   string          `json:"agent"`
	Prompt  string          `json:"prompt"`
	Input   string          `json:"input"`
	Context json.RawMessage `json:"context,omitempty"`
}

type SubagentBatchRequest struct {
	Tasks          []SubagentRequest `json:"tasks"`
	MaxConcurrency int               `json:"max_concurrency"`
}

type SubagentBatchItem struct {
	Index   int          `json:"index"`
	Agent   string       `json:"agent"`
	Status  string       `json:"status"`
	Summary string       `json:"summary"`
	Error   string       `json:"error,omitempty"`
	Result  tools.Result `json:"result,omitempty"`
}

type SubagentRegistry struct {
	agents map[string]Subagent
}

func DefaultSubagents() SubagentRegistry {
	agents := []Subagent{
		{
			Name:         "explorer",
			Description:  "Read-only worker for finding relevant files, symbols, and repository facts.",
			ModelRole:    "explorer",
			ContextMode:  "yarn",
			AllowedTools: []string{"read_file", "list_files", "search_text", "search_files", "git_status", "git_diff"},
		},
		{
			Name:         "reviewer",
			Description:  "Read-only worker for reviewing diffs and returning findings.",
			ModelRole:    "reviewer",
			ContextMode:  "shared-read",
			AllowedTools: []string{"read_file", "list_files", "search_text", "search_files", "git_status", "git_diff"},
		},
		{
			Name:         "tester",
			Description:  "Worker for running allowlisted test commands and summarizing failures.",
			ModelRole:    "reviewer",
			ContextMode:  "forked",
			AllowedTools: []string{"read_file", "search_text", "search_files", "git_status", "git_diff", "run_command"},
		},
		{
			Name:         "summarizer",
			Description:  "Compacts context, transcript, and session history into concise summaries.",
			ModelRole:    "summarizer",
			ContextMode:  "yarn",
			AllowedTools: []string{"read_file", "list_files", "search_text", "search_files"},
		},
		{
			Name:         "refactorer",
			Description:  "Applies scoped mechanical refactors: renames, extractions, moves.",
			ModelRole:    "editor",
			ContextMode:  "forked",
			AllowedTools: []string{"read_file", "list_files", "search_text", "search_files", "edit_file", "write_file"},
		},
		{
			Name:         "docs",
			Description:  "Updates documentation, README, and changelog based on recent changes.",
			ModelRole:    "editor",
			ContextMode:  "shared-read",
			AllowedTools: []string{"read_file", "list_files", "search_text", "search_files", "git_status", "git_diff", "edit_file", "write_file"},
		},
		{
			Name:         "commit",
			Description:  "Prepares git commits: reads diff, drafts conventional commit message, stages and commits.",
			ModelRole:    "editor",
			ContextMode:  "shared-read",
			AllowedTools: []string{"read_file", "list_files", "search_text", "git_status", "git_diff", "run_command"},
		},
		{
			Name:         "debug",
			Description:  "Debugs issues: reads code, runs tests, finds root cause. Reports findings without editing.",
			ModelRole:    "reviewer",
			ContextMode:  "forked",
			AllowedTools: []string{"read_file", "list_files", "search_text", "search_files", "git_status", "git_diff", "run_command"},
		},
		{
			Name:        "builder",
			Description: "Executes ONE checklist task: reads relevant files, edits/patches with user approval, runs verification. Dispatched by the planner via execute_task.",
			ModelRole:   "editor",
			ContextMode: "forked",
			AllowedTools: []string{
				"read_file", "list_files", "search_text", "search_files",
				"edit_file", "write_file", "apply_patch", "run_command",
				"git_status", "git_diff",
				"task_get", "task_update",
			},
		},
	}
	registry := SubagentRegistry{agents: map[string]Subagent{}}
	for _, agent := range agents {
		registry.agents[agent.Name] = agent
	}
	return registry
}

func (r SubagentRegistry) Get(name string) (Subagent, bool) {
	agent, ok := r.agents[name]
	return agent, ok
}

func (r SubagentRegistry) List() []Subagent {
	out := make([]Subagent, 0, len(r.agents))
	for _, agent := range r.agents {
		out = append(out, agent)
	}
	return out
}

func (r *Runtime) RunSubagent(ctx context.Context, request SubagentRequest) (tools.Result, error) {
	agentName := strings.TrimSpace(request.Agent)
	if agentName == "" {
		agentName = "explorer"
	}
	worker, ok := r.Subagents.Get(agentName)
	if !ok {
		return tools.Result{}, fmt.Errorf("unknown subagent: %s", agentName)
	}
	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" {
		prompt = strings.TrimSpace(request.Input)
	}
	if prompt == "" {
		return tools.Result{}, fmt.Errorf("subagent prompt is required")
	}
	subCtx, cancel := withOptionalTimeout(ctx, r.subagentTimeout())
	defer cancel()

	providerName := r.Config.Providers.Default.Name
	if providerName == "" {
		providerName = "lmstudio"
	}
	baseURL := r.providerBaseURL(providerName)
	provider, ok := r.Providers.Get(providerName)
	if !ok {
		return tools.Result{}, fmt.Errorf("provider %q is not registered", providerName)
	}
	// Decide which model this subagent runs on. On single-slot backends
	// (LM Studio with strategy="single") a worker-role swap would evict the
	// model the current turn just loaded, stalling every GEN slot. Reuse the
	// current mode's model in that case. Only when the user explicitly opts
	// into parallel model loading do we honor the worker's declared role.
	effectiveRole := worker.ModelRole
	strategy := strings.ToLower(strings.TrimSpace(r.Config.ModelLoading.Strategy))
	if strategy != "parallel" {
		effectiveRole = r.modelRoleForMode()
	}
	if err := r.EnsureRoleModelLoaded(subCtx, provider, effectiveRole); err != nil {
		return tools.Result{}, &subagentRunError{
			Agent:     worker.Name,
			Kind:      classifyProviderFailure(err),
			Phase:     "loading_model",
			TimedOut:  llm.IsProviderTimeout(err),
			Provider:  providerName,
			BaseURL:   baseURL,
			ModelRole: effectiveRole,
			Cause:     err,
		}
	}
	model := r.roleModel(effectiveRole)
	if model == "" {
		model = r.roleModel("chat")
	}
	snapshot := r.buildTaskSnapshot(prompt, worker.ModelRole)
	contextText := renderSubagentContext(request.Context)
	if contextText == "" {
		contextText = snapshot.Render()
	}
	messages := []llm.Message{
		{Role: "system", Content: subagentSystemPrompt(worker, snapshot)},
		{Role: "user", Content: "Context snapshot:\n" + contextText + "\n\nTask:\n" + prompt},
	}

	var trace []string
	stepLimit := subagentStepLimit(worker)
	lastPhase := "starting"
	parseFailures := 0
	emptyResponses := 0
	consecutiveReadLoops := 0
	for step := 0; step < stepLimit; step++ {
		stepsUsed := step + 1
		// Mirror the planner-loop compaction so prefill stays bounded as
		// the Builder accumulates read_file results across steps. Keep
		// only 2 verbatim — the builder turn is by definition build-mode
		// work where prefill on long sequences hurts the most.
		compactOldToolResults(messages, 2)
		reqCtx, reqCancel := withOptionalTimeout(subCtx, r.requestTimeout())
		accumulated, toolCalls, err := r.streamSubagentResponse(reqCtx, provider, llm.ChatRequest{
			Model:    model,
			Messages: messages,
		})
		reqCancel()
		if err != nil {
			return tools.Result{}, &subagentRunError{
				Agent:     worker.Name,
				Kind:      classifyProviderFailure(err),
				Phase:     lastPhase,
				StepsUsed: stepsUsed,
				TimedOut:  llm.IsProviderTimeout(err),
				Provider:  providerName,
				BaseURL:   baseURL,
				Model:     model,
				ModelRole: effectiveRole,
				Cause:     err,
			}
		}
		if len(toolCalls) > 0 {
			// Native tool-call path. Local providers like LM Studio with a
			// function-calling model emit OpenAI-style tool_calls instead of
			// the <tool_call>...</tool_call> text contract. Execute them
			// through the same subagent gate as the text path so the Builder
			// can make progress.
			emptyResponses = 0
			parseFailures = 0
			messages = append(messages, llm.Message{
				Role:      "assistant",
				Content:   accumulated,
				ToolCalls: toolCalls,
			})
			for _, tc := range toolCalls {
				agentCall := FromNativeToolCall(tc)
				if phase := builderPhaseForTool(agentCall.Name); phase != "" {
					lastPhase = phase
					if worker.Name == "builder" {
						if phase == "reading" {
							consecutiveReadLoops++
							if consecutiveReadLoops >= r.maxBuilderReadLoops() {
								return tools.Result{}, &subagentRunError{
									Agent:     worker.Name,
									Kind:      "no_progress",
									Phase:     lastPhase,
									StepsUsed: stepsUsed,
									Provider:  providerName,
									BaseURL:   baseURL,
									Model:     model,
									ModelRole: effectiveRole,
									Cause:     fmt.Errorf("%d consecutive builder read loops", consecutiveReadLoops),
								}
							}
						} else {
							consecutiveReadLoops = 0
						}
					}
				}
				trace = append(trace, fmt.Sprintf("tool_call %s", agentCall.Name))
				observation, err := r.executeSubagentTool(subCtx, worker, agentCall)
				if err != nil {
					observation = "Tool result for " + agentCall.Name + ": error: " + err.Error()
				}
				trace = append(trace, observation)
				messages = append(messages, llm.Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    observation,
				})
			}
			continue
		}
		if strings.TrimSpace(accumulated) == "" {
			emptyResponses++
			if emptyResponses >= r.maxEmptyResponses() {
				return tools.Result{}, &subagentRunError{
					Agent:     worker.Name,
					Kind:      "no_progress",
					Phase:     lastPhase,
					StepsUsed: stepsUsed,
					Provider:  providerName,
					BaseURL:   baseURL,
					Model:     model,
					ModelRole: effectiveRole,
					Cause:     fmt.Errorf("%d empty subagent responses", emptyResponses),
				}
			}
			messages = append(messages,
				llm.Message{Role: "assistant", Content: ""},
				llm.Message{Role: "user", Content: "Your response was empty. Return a final JSON result or exactly one valid tool call."},
			)
			continue
		}
		emptyResponses = 0
		parsed, err := ParseToolCall(accumulated)
		if err != nil {
			parseFailures++
			if parseFailures >= r.maxNoProgressSteps() {
				return tools.Result{}, &subagentRunError{
					Agent:     worker.Name,
					Kind:      "parse_failure",
					Phase:     lastPhase,
					StepsUsed: stepsUsed,
					Provider:  providerName,
					BaseURL:   baseURL,
					Model:     model,
					ModelRole: effectiveRole,
					Cause:     err,
				}
			}
			messages = append(messages,
				llm.Message{Role: "assistant", Content: accumulated},
				llm.Message{Role: "user", Content: "Tool call parse error: " + err.Error() + "\nReturn a final JSON result or a valid tool call."},
			)
			continue
		}
		parseFailures = 0
		if !parsed.Found {
			text := strings.TrimSpace(accumulated)
			return tools.Result{
				Title:   "Subagent " + worker.Name,
				Summary: oneLine(text, 240),
				Content: []tools.ContentBlock{{Type: "text", Text: text}},
			}, nil
		}
		if phase := builderPhaseForTool(parsed.Call.Name); phase != "" {
			lastPhase = phase
			if worker.Name == "builder" {
				if phase == "reading" {
					consecutiveReadLoops++
					if consecutiveReadLoops >= r.maxBuilderReadLoops() {
						return tools.Result{}, &subagentRunError{
							Agent:     worker.Name,
							Kind:      "no_progress",
							Phase:     lastPhase,
							StepsUsed: stepsUsed,
							Provider:  providerName,
							BaseURL:   baseURL,
							Model:     model,
							ModelRole: effectiveRole,
							Cause:     fmt.Errorf("%d consecutive builder read loops", consecutiveReadLoops),
						}
					}
				} else {
					consecutiveReadLoops = 0
				}
			}
		}
		trace = append(trace, fmt.Sprintf("tool_call %s", parsed.Call.Name))
		observation, err := r.executeSubagentTool(subCtx, worker, parsed.Call)
		if err != nil {
			observation = "Tool result for " + parsed.Call.Name + ": error: " + err.Error()
		}
		trace = append(trace, observation)
		messages = append(messages,
			llm.Message{Role: "assistant", Content: accumulated},
			llm.Message{Role: "user", Content: observation},
		)
	}
	return tools.Result{}, &subagentRunError{
		Agent:     worker.Name,
		Kind:      "no_progress",
		Phase:     lastPhase,
		StepsUsed: stepLimit,
		Provider:  providerName,
		BaseURL:   baseURL,
		Model:     model,
		ModelRole: effectiveRole,
		Cause:     fmt.Errorf("subagent stopped after step limit"),
	}
}

func (r *Runtime) streamSubagentResponse(ctx context.Context, provider llm.Provider, req llm.ChatRequest) (string, []llm.ToolCall, error) {
	stream, err := r.streamProvider(ctx, provider, req)
	if err != nil {
		if !errors.Is(err, llm.ErrNotSupported) {
			return "", nil, err
		}
	}
	if stream == nil {
		resp, chatErr := provider.Chat(ctx, req)
		if chatErr != nil {
			return "", nil, chatErr
		}
		return resp.Content, resp.ToolCalls, nil
	}

	var text strings.Builder
	var toolCalls []llm.ToolCall
	for event := range stream {
		switch event.Type {
		case "text":
			text.WriteString(event.Text)
		case "tool_calls":
			toolCalls = event.ToolCalls
		case "error":
			// If the stream errored AFTER a tool_call or parseable
			// <tool_call> already arrived, treat the partial as success
			// and let the step process it. Otherwise the Builder loses
			// real progress every time LM Studio cuts the stream early.
			if len(toolCalls) > 0 {
				return text.String(), toolCalls, nil
			}
			if hasParseableToolCall(text.String()) {
				return text.String(), nil, nil
			}
			return text.String(), toolCalls, event.Error
		}
	}
	return text.String(), toolCalls, nil
}

func hasParseableToolCall(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	parsed, err := ParseToolCall(text)
	return err == nil && parsed.Found
}

func (r *Runtime) RunSubagents(ctx context.Context, request SubagentBatchRequest) (tools.Result, error) {
	if len(request.Tasks) == 0 {
		return tools.Result{}, fmt.Errorf("subagent tasks are required")
	}
	maxConcurrency := request.MaxConcurrency
	if maxConcurrency <= 0 {
		maxConcurrency = 3
	}
	if maxConcurrency > 8 {
		maxConcurrency = 8
	}
	if maxConcurrency > len(request.Tasks) {
		maxConcurrency = len(request.Tasks)
	}
	// ModelLoading.Strategy governs model load/switch behavior (single vs parallel
	// loaded models), NOT inference concurrency. A "single" strategy still allows
	// multiple concurrent requests against the one loaded model.

	// Generate a batch id once so every progress event ties back to the same
	// lane group in the TUI. Pointer address is enough — unique per call.
	batchID := fmt.Sprintf("batch-%p", &request)
	total := len(request.Tasks)
	events := r.currentEvents()
	emit := func(idx int, agent, status, phase string, stepsUsed int, timedOut bool, summary, errText string) {
		if events == nil {
			return
		}
		// Non-blocking send: the TUI pump is the receiver and we don't want
		// a stalled consumer to deadlock the batch goroutines. On drop the
		// lane view simply won't update — the final result is still captured.
		prog := &SubagentProgress{
			BatchID:   batchID,
			Index:     idx,
			Total:     total,
			Agent:     agent,
			Status:    status,
			Phase:     phase,
			StepsUsed: stepsUsed,
			TimedOut:  timedOut,
			Summary:   summary,
			Error:     errText,
		}
		select {
		case events <- Event{Type: EventSubagentProgress, SubagentProgress: prog}:
		default:
		}
	}

	// Seed all tasks as pending so the TUI can draw the full batch skeleton
	// immediately, even before goroutines acquire the semaphore.
	for i, task := range request.Tasks {
		name := strings.TrimSpace(task.Agent)
		if name == "" {
			name = "explorer"
		}
		emit(i, name, "pending", "", 0, false, "", "")
	}

	items := make([]SubagentBatchItem, len(request.Tasks))
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	for i, task := range request.Tasks {
		i, task := i, task
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				items[i] = SubagentBatchItem{Index: i, Agent: strings.TrimSpace(task.Agent), Status: "error", Error: ctx.Err().Error()}
				emit(i, strings.TrimSpace(task.Agent), "error", "", 0, false, "", ctx.Err().Error())
				return
			}

			agentName := strings.TrimSpace(task.Agent)
			if agentName == "" {
				agentName = "explorer"
			}
			worker, ok := r.Subagents.Get(agentName)
			if !ok {
				items[i] = SubagentBatchItem{Index: i, Agent: agentName, Status: "error", Error: "unknown subagent: " + agentName}
				emit(i, agentName, "error", "", 0, false, "", "unknown subagent: "+agentName)
				return
			}
			if hasMutatingTools(worker.AllowedTools) {
				items[i] = SubagentBatchItem{Index: i, Agent: agentName, Status: "error", Error: "parallel subagents do not allow mutating tools"}
				emit(i, agentName, "error", "", 0, false, "", "parallel subagents do not allow mutating tools")
				return
			}
			emit(i, agentName, "running", "starting", 0, false, "", "")
			result, err := r.RunSubagent(ctx, task)
			if err != nil {
				var runErr *subagentRunError
				phase := ""
				stepsUsed := 0
				timedOut := false
				if errors.As(err, &runErr) {
					phase = runErr.Phase
					stepsUsed = runErr.StepsUsed
					timedOut = runErr.TimedOut
				}
				items[i] = SubagentBatchItem{Index: i, Agent: agentName, Status: "error", Error: err.Error()}
				emit(i, agentName, "error", phase, stepsUsed, timedOut, "", err.Error())
				return
			}
			items[i] = SubagentBatchItem{
				Index:   i,
				Agent:   agentName,
				Status:  "completed",
				Summary: result.Summary,
				Result:  result,
			}
			emit(i, agentName, "completed", "completed", 0, false, result.Summary, "")
		}()
	}
	wg.Wait()

	completed, failed := 0, 0
	var b strings.Builder
	for _, item := range items {
		if item.Status == "completed" {
			completed++
		} else {
			failed++
		}
		fmt.Fprintf(&b, "[%d] %s %s", item.Index, item.Agent, item.Status)
		if item.Summary != "" {
			fmt.Fprintf(&b, ": %s", item.Summary)
		}
		if item.Error != "" {
			fmt.Fprintf(&b, ": %s", item.Error)
		}
		b.WriteByte('\n')
		if len(item.Result.Content) > 0 {
			for _, block := range item.Result.Content {
				if strings.TrimSpace(block.Text) != "" {
					b.WriteString(strings.TrimSpace(block.Text))
					b.WriteString("\n")
				}
			}
		}
		b.WriteByte('\n')
	}
	payload, _ := json.Marshal(items)
	return tools.Result{
		Title:   "Subagents",
		Summary: fmt.Sprintf("%d subagent task(s): %d completed, %d failed", len(items), completed, failed),
		Content: []tools.ContentBlock{
			{Type: "text", Text: strings.TrimSpace(b.String())},
			{Type: "json", Text: string(payload)},
		},
	}, nil
}

func (r *Runtime) executeSubagentTool(ctx context.Context, worker Subagent, call ToolCall) (string, error) {
	tool, ok := r.Tools.Get(call.Name)
	if !ok {
		return "", fmt.Errorf("tool not found: %s", call.Name)
	}
	canonicalName := tool.Name()
	if !contains(worker.AllowedTools, canonicalName) {
		return "Tool result for " + canonicalName + ": denied by subagent policy", nil
	}
	// Subagents share the parent's per-turn read cache. Without this, four
	// parallel builders each re-read the same files from disk and re-prefill
	// the bytes into their separate message histories — multiplying token
	// usage by N for no useful work. Cache hits are also exempt from the
	// builder's read-loop guard since the read did no real work.
	if canonicalName == "read_file" {
		if cached, _, hit := r.lookupReadCache(call.Input); hit && cached != nil {
			return "Tool result for read_file:\n" + summarizeResult(*cached), nil
		}
	}
	if canonicalName == "run_command" {
		var req struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(call.Input, &req); err != nil {
			return "", err
		}
		decision, reason := r.Commands.Decide(req.Command)
		if decision == permissions.Deny {
			return "Tool result for run_command: " + reason, nil
		}
		if decision == permissions.Ask && !r.autoApproveMode() {
			events := r.currentEvents()
			if events == nil {
				return "Tool result for run_command: no event channel available for approval", nil
			}
			request := &ApprovalRequest{
				ID:       fmt.Sprintf("approval-subagent-command-%p", &call),
				ToolName: "run_command",
				Input:    call.Input,
				Summary:  req.Command,
				Diff:     "Subagent " + worker.Name + " requests command:\n" + req.Command,
				Response: make(chan ApprovalResponse, 1),
				command:  &call,
			}
			events <- Event{Type: EventApproval, ToolName: "run_command", Input: call.Input, Approval: request}
			select {
			case <-ctx.Done():
				return "Tool result for run_command: error: " + ctx.Err().Error(), nil
			case response := <-request.Response:
				if !response.Approved {
					return "Tool result for run_command: rejected by user", nil
				}
			}
		}
		result, _ := r.runCommandTool(ctx, call.Input, reason)
		return "Tool result for run_command:\n" + summarizeResult(*result), nil
	}
	if canonicalName == "edit_file" || canonicalName == "write_file" || canonicalName == "apply_patch" {
		events := r.currentEvents()
		if events == nil {
			return "Tool result for " + canonicalName + ": no event channel available for approval", nil
		}
		result, observation := r.requestApproval(ctx, canonicalName, call.Input, events)
		_ = result
		return observation, nil
	}
	if canonicalName == "task_get" || canonicalName == "task_update" {
		result, err := r.runTaskTool(canonicalName, call.Input)
		if err != nil {
			return "Tool result for " + canonicalName + ": error: " + err.Error(), nil
		}
		return "Tool result for " + canonicalName + ":\n" + summarizeResult(result), nil
	}
	result, err := tool.Run(tools.Context{
		Context: ctx,
		CWD:     r.CWD,
		Agent:   worker.Name,
	}, call.Input)
	if err != nil {
		return "", err
	}
	observation := "Tool result for " + canonicalName + ":\n" + summarizeResult(result)
	// Populate the shared read cache so a sibling subagent (or the parent
	// later in the turn) gets a hit instead of re-reading from disk.
	if canonicalName == "read_file" && len(result.Content) > 0 {
		r.storeReadCache(call.Input, &result, observation)
	}
	return observation, nil
}

// currentEvents returns the events channel of the in-flight turn, or nil when
// no turn is active. Used by subagents that need to raise approval prompts.
func (r *Runtime) currentEvents() chan<- Event {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeEvents
}

func subagentSystemPrompt(worker Subagent, snapshot contextbuilder.Snapshot) string {
	var rules strings.Builder
	if worker.Name == "builder" {
		rules.WriteString("You execute exactly ONE checklist task end-to-end.\n")
		rules.WriteString("You MAY read files, edit files, apply patches, run allowed verification commands, and update task state.\n")
		rules.WriteString("Do not re-plan, do not rewrite the checklist, and do not call execute_task or spawn_subagent.\n")
		rules.WriteString("Prefer this workflow: inspect task context -> read/search the minimal files -> apply the smallest viable edit -> verify if useful -> update the task if you changed its state -> return the final result.\n")
		rules.WriteString("This task is one section of a larger plan. Aim to finish in <=3 tool calls.\n")
		rules.WriteString("If the assigned section cannot be completed in <=3 tool calls, return findings='task_too_large' with a proposed sub-split and let the planner re-chunk. Do NOT try to deliver the whole feature in one task.\n")
		rules.WriteString("For large new files under scaffold_then_patch, create only a minimal scaffold first. Then fill the file section-by-section with edit_file or apply_patch. Do not write the full file in one shot.\n")
		rules.WriteString("File size limit: keep every produced file at or below ~600 lines. If the assigned task implies a single file >600 lines, stop, return findings='split_required' with a proposed multi-file split, and let the planner re-plan instead of writing one giant file. Exception: generated data, fixtures, or dense JSON/CSV may exceed the limit when the file's nature requires it — call that out in the result.\n")
		rules.WriteString("Stop once the single task is completed or clearly blocked.\n")
	} else if hasMutatingTools(worker.AllowedTools) {
		rules.WriteString("You may edit files only when the assigned task requires it.\n")
		rules.WriteString("Keep edits scoped and reversible.\n")
	} else {
		rules.WriteString("Do not edit files.\n")
	}
	return strings.TrimSpace(`You are Forge subagent ` + worker.Name + `.

Role: ` + worker.Description + `
Context mode: ` + worker.ContextMode + `
Allowed tools: ` + strings.Join(worker.AllowedTools, ", ") + `

You are a limited worker. Prefer a concise final JSON object:
{"status":"completed","summary":"...","findings":[],"changed_files":[],"suggested_next_steps":[]}

If you need information, request exactly one tool call:
<tool_call>{"name":"read_file","input":{"path":"path/to/file"}}</tool_call>

` + strings.TrimSpace(rules.String()) + `
Do not request tools outside the allowed list.
Main context engine: ` + snapshot.ContextEngine)
}

func subagentStepLimit(worker Subagent) int {
	if worker.Name == "builder" {
		// Tight budget on purpose: the planner is expected to chunk large
		// work via normalizeChecklistItems, so each Builder run is one small
		// section. If 6 steps are not enough, the right answer is to replan
		// (return findings='task_too_large') rather than burn context here.
		return 6
	}
	if hasMutatingTools(worker.AllowedTools) {
		return 8
	}
	return 4
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func oneLine(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if limit > 0 && len(text) > limit {
		return text[:limit] + "..."
	}
	return text
}

func hasMutatingTools(names []string) bool {
	for _, name := range names {
		switch name {
		case "edit_file", "write_file", "apply_patch":
			return true
		}
	}
	return false
}

func renderSubagentContext(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var payload struct {
		Text    string `json:"text"`
		Summary string `json:"summary"`
		Context string `json:"context"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil {
		for _, value := range []string{payload.Text, payload.Summary, payload.Context} {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	var structured map[string]any
	if err := json.Unmarshal(raw, &structured); err == nil && len(structured) > 0 {
		return formatStructuredSubagentContext(structured)
	}
	return strings.TrimSpace(string(raw))
}

func formatStructuredSubagentContext(payload map[string]any) string {
	var lines []string
	if gitState, ok := payload["git_state"].(string); ok && strings.TrimSpace(gitState) != "" {
		lines = append(lines, "Git state: "+strings.TrimSpace(gitState))
	}
	if task, ok := payload["task"]; ok {
		if taskJSON, err := json.Marshal(task); err == nil {
			lines = append(lines, "Task: "+string(taskJSON))
		}
	}
	if files, ok := payload["relevant_files"]; ok {
		if filesJSON, err := json.Marshal(files); err == nil {
			lines = append(lines, "Relevant files: "+string(filesJSON))
		}
	}
	for _, key := range []string{"target_file", "file_strategy", "section_goal"} {
		if value, ok := payload[key]; ok {
			if text := strings.TrimSpace(fmt.Sprintf("%v", value)); text != "" {
				lines = append(lines, strings.ReplaceAll(key, "_", " ")+": "+text)
			}
		}
	}
	if len(lines) > 0 {
		return strings.Join(lines, "\n")
	}
	return strings.TrimSpace(fmt.Sprintf("%v", payload))
}
