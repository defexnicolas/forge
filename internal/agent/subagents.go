package agent

import (
	"context"
	"encoding/json"
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
			Name:         "builder",
			Description:  "Executes ONE checklist task: reads relevant files, edits/patches with user approval, runs verification. Dispatched by the planner via execute_task.",
			ModelRole:    "editor",
			ContextMode:  "forked",
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

	providerName := r.Config.Providers.Default.Name
	if providerName == "" {
		providerName = "lmstudio"
	}
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
	if err := r.EnsureRoleModelLoaded(ctx, provider, effectiveRole); err != nil {
		return tools.Result{}, err
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
	for step := 0; step < 4; step++ {
		resp, err := provider.Chat(ctx, llm.ChatRequest{
			Model:       model,
			Messages:    messages,
			Temperature: 0.1,
		})
		if err != nil {
			return tools.Result{}, err
		}
		parsed, err := ParseToolCall(resp.Content)
		if err != nil {
			messages = append(messages,
				llm.Message{Role: "assistant", Content: resp.Content},
				llm.Message{Role: "user", Content: "Tool call parse error: " + err.Error() + "\nReturn a final JSON result or a valid tool call."},
			)
			continue
		}
		if !parsed.Found {
			text := strings.TrimSpace(resp.Content)
			return tools.Result{
				Title:   "Subagent " + worker.Name,
				Summary: oneLine(text, 240),
				Content: []tools.ContentBlock{{Type: "text", Text: text}},
			}, nil
		}
		trace = append(trace, fmt.Sprintf("tool_call %s", parsed.Call.Name))
		observation, err := r.executeSubagentTool(ctx, worker, parsed.Call)
		if err != nil {
			observation = "Tool result for " + parsed.Call.Name + ": error: " + err.Error()
		}
		trace = append(trace, observation)
		messages = append(messages,
			llm.Message{Role: "assistant", Content: resp.Content},
			llm.Message{Role: "user", Content: observation},
		)
	}
	return tools.Result{
		Title:   "Subagent " + worker.Name,
		Summary: "subagent stopped after step limit",
		Content: []tools.ContentBlock{{Type: "text", Text: strings.Join(trace, "\n\n")}},
	}, nil
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
				return
			}

			agentName := strings.TrimSpace(task.Agent)
			if agentName == "" {
				agentName = "explorer"
			}
			worker, ok := r.Subagents.Get(agentName)
			if !ok {
				items[i] = SubagentBatchItem{Index: i, Agent: agentName, Status: "error", Error: "unknown subagent: " + agentName}
				return
			}
			if hasMutatingTools(worker.AllowedTools) {
				items[i] = SubagentBatchItem{Index: i, Agent: agentName, Status: "error", Error: "parallel subagents do not allow mutating tools"}
				return
			}
			result, err := r.RunSubagent(ctx, task)
			if err != nil {
				items[i] = SubagentBatchItem{Index: i, Agent: agentName, Status: "error", Error: err.Error()}
				return
			}
			items[i] = SubagentBatchItem{
				Index:   i,
				Agent:   agentName,
				Status:  "completed",
				Summary: result.Summary,
				Result:  result,
			}
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
		if decision == permissions.Ask {
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
	return "Tool result for " + canonicalName + ":\n" + summarizeResult(result), nil
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
	return strings.TrimSpace(`You are Forge subagent ` + worker.Name + `.

Role: ` + worker.Description + `
Context mode: ` + worker.ContextMode + `
Allowed tools: ` + strings.Join(worker.AllowedTools, ", ") + `

You are a limited worker. Prefer a concise final JSON object:
{"status":"completed","summary":"...","findings":[],"changed_files":[],"suggested_next_steps":[]}

If you need information, request exactly one tool call:
<tool_call>{"name":"read_file","input":{"path":"path/to/file"}}</tool_call>

Do not edit files. Do not request tools outside the allowed list.
Main context engine: ` + snapshot.ContextEngine)
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
	return strings.TrimSpace(string(raw))
}
