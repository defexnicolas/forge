package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	contextbuilder "forge/internal/context"
	"forge/internal/patch"
	"forge/internal/permissions"
	"forge/internal/tasks"
	"forge/internal/tools"
)

func (r *Runtime) executeTool(ctx context.Context, call ToolCall, events chan<- Event) (*tools.Result, string) {
	if r.Hooks != nil {
		if err := r.Hooks.RunBefore("before:tool_call", call.Name); err != nil {
			result := tools.Result{Title: call.Name, Summary: "blocked by hook: " + err.Error()}
			return &result, "Tool result for " + call.Name + ": blocked by hook: " + err.Error()
		}
	}
	result, observation := r.executeToolInner(ctx, call, events)
	if r.Hooks != nil {
		var changed []string
		if result != nil {
			changed = result.ChangedFiles
		}
		r.Hooks.RunAfter("after:tool_call", call.Name, changed)
		if len(changed) > 0 {
			r.Hooks.RunAfter("after:patch", call.Name, changed)
		}
	}
	return result, observation
}

func (r *Runtime) executeToolInner(ctx context.Context, call ToolCall, events chan<- Event) (*tools.Result, string) {
	tool, ok := r.Tools.Get(call.Name)
	if !ok {
		result := tools.Result{Title: call.Name, Summary: "tool not found"}
		return &result, "Tool result: tool not found: " + call.Name
	}
	canonicalName := tool.Name()
	decision, reason := r.Policy.Decision(canonicalName)
	if decision == ToolDeny {
		hint := reason
		if r.Mode == "plan" {
			hint += ". In plan mode, use todo_write to describe proposed changes instead of editing files directly."
		}
		result := tools.Result{Title: canonicalName, Summary: hint}
		return &result, "Tool result for " + canonicalName + ": " + hint
	}
	if canonicalName == "run_command" {
		return r.executeCommand(ctx, call, events)
	}
	if canonicalName == "spawn_subagent" {
		return r.executeSubagent(ctx, call.Input)
	}
	if strings.HasPrefix(canonicalName, "task_") {
		return r.executeTask(ctx, canonicalName, call.Input)
	}
	if canonicalName == "todo_write" {
		return r.executeTodoWrite(call.Input)
	}
	if canonicalName == "ask_user" {
		return r.executeAskUser(ctx, call.Input, events)
	}
	if canonicalName == "powershell_command" {
		return r.executeCommand(ctx, ToolCall{Name: "run_command", Input: call.Input}, events)
	}
	if decision == ToolAsk {
		// Mark first pending task as in_progress when starting a mutation.
		r.markNextTaskInProgress()
		return r.requestApproval(ctx, canonicalName, call.Input, events)
	}
	result, err := tool.Run(tools.Context{
		Context: ctx,
		CWD:     r.CWD,
		Agent:   r.Config.DefaultAgent,
	}, call.Input)
	if err != nil {
		result := tools.Result{Title: canonicalName, Summary: err.Error()}
		return &result, "Tool result for " + canonicalName + ": error: " + err.Error()
	}
	return &result, "Tool result for " + canonicalName + ":\n" + summarizeResult(result)
}

func (r *Runtime) executeSubagent(ctx context.Context, input json.RawMessage) (*tools.Result, string) {
	var req SubagentRequest
	if err := json.Unmarshal(input, &req); err != nil {
		result := tools.Result{Title: "spawn_subagent", Summary: err.Error()}
		return &result, "Tool result for spawn_subagent: error: " + err.Error()
	}
	result, err := r.RunSubagent(ctx, req)
	if err != nil {
		result := tools.Result{Title: "spawn_subagent", Summary: err.Error()}
		return &result, "Tool result for spawn_subagent: error: " + err.Error()
	}
	return &result, "Tool result for spawn_subagent:\n" + summarizeResult(result)
}

func (r *Runtime) executeTodoWrite(input json.RawMessage) (*tools.Result, string) {
	var req struct {
		Items   []string `json:"items"`
		Content string   `json:"content"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		result := tools.Result{Title: "todo_write", Summary: err.Error()}
		return &result, "Tool result for todo_write: error: " + err.Error()
	}
	// If no items array but content string is provided, split content into items.
	if len(req.Items) == 0 && req.Content != "" {
		for _, line := range strings.Split(req.Content, "\n") {
			line = strings.TrimSpace(line)
			// Strip markdown list markers and checkbox markers.
			line = strings.TrimLeft(line, "-*•0123456789. ")
			line = strings.TrimPrefix(line, "[ ] ")
			line = strings.TrimPrefix(line, "[x] ")
			line = strings.TrimPrefix(line, "☐ ")
			line = strings.TrimPrefix(line, "☑ ")
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "---") {
				req.Items = append(req.Items, line)
			}
		}
	}
	plan, err := r.Tasks.ReplacePlan(req.Items)
	if err != nil {
		result := tools.Result{Title: "todo_write", Summary: err.Error()}
		return &result, "Tool result for todo_write: error: " + err.Error()
	}
	result := tools.Result{
		Title:   "Todo plan",
		Summary: "Updated plan",
		Content: []tools.ContentBlock{{Type: "text", Text: tasks.Format(plan)}},
	}
	return &result, "Tool result for todo_write:\n" + summarizeResult(result)
}

func (r *Runtime) executeTask(ctx context.Context, toolName string, input json.RawMessage) (*tools.Result, string) {
	_ = ctx
	result, err := r.runTaskTool(toolName, input)
	if err != nil {
		result := tools.Result{Title: toolName, Summary: err.Error()}
		return &result, "Tool result for " + toolName + ": error: " + err.Error()
	}
	return &result, "Tool result for " + toolName + ":\n" + summarizeResult(result)
}

func (r *Runtime) runTaskTool(toolName string, input json.RawMessage) (tools.Result, error) {
	switch toolName {
	case "task_create":
		var req struct {
			Title string `json:"title"`
			Notes string `json:"notes"`
		}
		if err := json.Unmarshal(input, &req); err != nil {
			return tools.Result{}, err
		}
		task, err := r.Tasks.Create(req.Title, req.Notes)
		if err != nil {
			return tools.Result{}, err
		}
		return taskResult("Created task", []tasks.Task{task}), nil
	case "task_list":
		list, err := r.Tasks.List()
		if err != nil {
			return tools.Result{}, err
		}
		return taskResult("Task list", list), nil
	case "task_get":
		var req struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(input, &req); err != nil {
			return tools.Result{}, err
		}
		task, err := r.Tasks.Get(req.ID)
		if err != nil {
			return tools.Result{}, err
		}
		return taskResult("Task", []tasks.Task{task}), nil
	case "task_update":
		var req struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Status string `json:"status"`
			Notes  string `json:"notes"`
		}
		if err := json.Unmarshal(input, &req); err != nil {
			return tools.Result{}, err
		}
		task, err := r.Tasks.Update(req.ID, req.Title, req.Status, req.Notes)
		if err != nil {
			return tools.Result{}, err
		}
		return taskResult("Updated task", []tasks.Task{task}), nil
	default:
		return tools.Result{}, fmt.Errorf("unknown task tool: %s", toolName)
	}
}

func taskResult(title string, list []tasks.Task) tools.Result {
	return tools.Result{
		Title:   title,
		Summary: fmt.Sprintf("%d task(s)", len(list)),
		Content: []tools.ContentBlock{{Type: "text", Text: tasks.Format(list)}},
	}
}

func (r *Runtime) executeCommand(ctx context.Context, call ToolCall, events chan<- Event) (*tools.Result, string) {
	var req struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(call.Input, &req); err != nil {
		result := tools.Result{Title: "run_command", Summary: err.Error()}
		return &result, "Tool result for run_command: error: " + err.Error()
	}
	decision, reason := r.Commands.Decide(req.Command)
	if decision == permissions.Deny {
		result := tools.Result{Title: "run_command", Summary: reason}
		return &result, "Tool result for run_command: " + reason
	}
	if decision == permissions.Ask {
		request := &ApprovalRequest{
			ID:       fmt.Sprintf("approval-command-%p", &call),
			ToolName: "run_command",
			Input:    call.Input,
			Summary:  req.Command,
			Diff:     "Command requires approval:\n" + req.Command,
			Response: make(chan ApprovalResponse, 1),
			command:  &call,
		}
		events <- Event{Type: EventApproval, ToolName: "run_command", Input: call.Input, Approval: request}
		select {
		case <-ctx.Done():
			result := tools.Result{Title: "run_command", Summary: ctx.Err().Error()}
			return &result, "Tool result for run_command: error: " + ctx.Err().Error()
		case response := <-request.Response:
			if !response.Approved {
				result := tools.Result{Title: "run_command", Summary: "rejected by user"}
				return &result, "Tool result for run_command: rejected by user"
			}
		}
	}
	return r.runCommandTool(ctx, call.Input, reason)
}

func (r *Runtime) runCommandTool(ctx context.Context, input json.RawMessage, reason string) (*tools.Result, string) {
	tool, _ := r.Tools.Get("run_command")
	result, err := tool.Run(tools.Context{
		Context: ctx,
		CWD:     r.CWD,
		Agent:   r.Config.DefaultAgent,
	}, input)
	if err != nil {
		result.Summary = result.Summary + " failed: " + err.Error()
	}
	if reason != "" {
		result.Summary = result.Summary + " (" + reason + ")"
	}
	return &result, "Tool result for run_command:\n" + summarizeResult(result)
}

func (r *Runtime) requestApproval(ctx context.Context, toolName string, input json.RawMessage, events chan<- Event) (*tools.Result, string) {
	plan, summary, err := r.prepareMutation(toolName, input)
	if err != nil {
		result := tools.Result{Title: toolName, Summary: err.Error()}
		return &result, "Tool result for " + toolName + ": error: " + err.Error()
	}
	request := &ApprovalRequest{
		ID:       fmt.Sprintf("approval-%p", &plan),
		ToolName: toolName,
		Input:    input,
		Summary:  summary,
		Diff:     patch.Diff(plan),
		Response: make(chan ApprovalResponse, 1),
		plan:     plan,
	}
	events <- Event{Type: EventApproval, ToolName: toolName, Input: input, Approval: request}
	select {
	case <-ctx.Done():
		result := tools.Result{Title: toolName, Summary: ctx.Err().Error()}
		return &result, "Tool result for " + toolName + ": error: " + ctx.Err().Error()
	case response := <-request.Response:
		if !response.Approved {
			result := tools.Result{Title: toolName, Summary: "rejected by user"}
			return &result, "Tool result for " + toolName + ": rejected by user"
		}
		snapshots, err := patch.Apply(r.CWD, request.plan)
		if err != nil {
			result := tools.Result{Title: toolName, Summary: err.Error()}
			return &result, "Tool result for " + toolName + ": error: " + err.Error()
		}
		r.mu.Lock()
		r.undoStack = append(r.undoStack, UndoEntry{Summary: summary, Snapshots: snapshots})
		r.mu.Unlock()
		changed := make([]string, 0, len(request.plan.Operations))
		for _, op := range request.plan.Operations {
			changed = append(changed, op.Path)
		}
		// Mark matching plan task as completed.
		r.completeMatchingTask(summary)
		result := tools.Result{
			Title:        toolName,
			Summary:      "approved and applied: " + summary,
			Content:      []tools.ContentBlock{{Type: "text", Text: request.Diff}},
			ChangedFiles: changed,
		}
		return &result, "Tool result for " + toolName + ":\n" + summarizeResult(result)
	}
}

func (r *Runtime) prepareMutation(toolName string, input json.RawMessage) (patch.Plan, string, error) {
	switch toolName {
	case "edit_file":
		var req struct {
			Path    string `json:"path"`
			OldText string `json:"old_text"`
			NewText string `json:"new_text"`
		}
		if err := json.Unmarshal(input, &req); err != nil {
			return patch.Plan{}, "", err
		}
		plan, err := patch.ExactReplace(r.CWD, req.Path, req.OldText, req.NewText)
		return plan, "edit " + req.Path, err
	case "write_file":
		var req struct {
			Path    string `json:"path"`
			Content string `json:"content"`
			NewText string `json:"new_text"` // alias: some models use new_text
			Text    string `json:"text"`     // alias: some models use text
		}
		if err := json.Unmarshal(input, &req); err != nil {
			return patch.Plan{}, "", err
		}
		// Accept content from any of the common field names models use.
		content := req.Content
		if content == "" {
			content = req.NewText
		}
		if content == "" {
			content = req.Text
		}
		plan, err := patch.NewFile(r.CWD, req.Path, content)
		return plan, "create " + req.Path, err
	case "apply_patch":
		var req struct {
			Patch string `json:"patch"`
		}
		if err := json.Unmarshal(input, &req); err != nil {
			return patch.Plan{}, "", err
		}
		plan, err := patch.UnifiedDiff(r.CWD, req.Patch)
		return plan, "apply patch", err
	default:
		return patch.Plan{}, "", fmt.Errorf("%s is not a mutating approval tool", toolName)
	}
}

func (r *Runtime) executeAskUser(ctx context.Context, input json.RawMessage, events chan<- Event) (*tools.Result, string) {
	var req struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		result := tools.Result{Title: "ask_user", Summary: err.Error()}
		return &result, "Tool result for ask_user: error: " + err.Error()
	}
	request := &AskUserRequest{
		Question: req.Question,
		Response: make(chan string, 1),
	}
	events <- Event{Type: EventAskUser, ToolName: "ask_user", Input: input, AskUser: request}
	select {
	case <-ctx.Done():
		result := tools.Result{Title: "ask_user", Summary: ctx.Err().Error()}
		return &result, "Tool result for ask_user: error: " + ctx.Err().Error()
	case answer := <-request.Response:
		result := tools.Result{
			Title:   "User answer",
			Summary: answer,
			Content: []tools.ContentBlock{{Type: "text", Text: "User answered: " + answer}},
		}
		return &result, "Tool result for ask_user: User answered: " + answer
	}
}

// markNextTaskInProgress marks the first pending task as in_progress.
func (r *Runtime) markNextTaskInProgress() {
	if r.Tasks == nil {
		return
	}
	list, err := r.Tasks.List()
	if err != nil {
		return
	}
	for _, task := range list {
		if task.Status == "pending" {
			_, _ = r.Tasks.Update(task.ID, "", "in_progress", "")
			return
		}
	}
}

// completeMatchingTask finds the first pending plan task that matches the action
// summary and marks it as completed.
func (r *Runtime) completeMatchingTask(summary string) {
	if r.Tasks == nil {
		return
	}
	list, err := r.Tasks.List()
	if err != nil || len(list) == 0 {
		return
	}
	summaryLower := strings.ToLower(summary)
	// First pass: try to find a task whose title words appear in the summary.
	for _, task := range list {
		if task.Status != "pending" && task.Status != "in_progress" {
			continue
		}
		titleLower := strings.ToLower(task.Title)
		// Check if key words from the task title appear in the summary.
		words := strings.Fields(titleLower)
		matches := 0
		for _, w := range words {
			if len(w) > 2 && strings.Contains(summaryLower, w) {
				matches++
			}
		}
		if matches > 0 && matches >= len(words)/2 {
			_, _ = r.Tasks.Update(task.ID, "", "completed", "")
			return
		}
	}
	// Fallback: mark the first pending task as completed (sequential execution).
	for _, task := range list {
		if task.Status == "pending" {
			_, _ = r.Tasks.Update(task.ID, "", "completed", "")
			return
		}
	}
}

func summarizeResult(result tools.Result) string {
	payload, err := json.Marshal(result)
	if err != nil {
		return result.Summary
	}
	if len(payload) > 16000 {
		payload = payload[:16000]
		return string(payload) + "\n[truncated]"
	}
	return string(payload)
}

func systemPrompt(snapshot contextbuilder.Snapshot, nativeToolCalling bool, modeName string, policy SprintPolicy) string {
	modePrefix := ""
	if mode, ok := GetMode(modeName); ok && modeName != "build" {
		modePrefix = mode.Prompt + "\n\n"
	}

	if nativeToolCalling {
		return strings.TrimSpace(modePrefix + `You are Forge, a coding agent inside a terminal workbench.

You have access to tools via function calling. Use them to read files, search code, run commands, and make edits. Edits require user approval and should be small and reversible.

For verification, use run_command with safe commands such as "go test ./..." or "git diff". Risky commands may require approval or be denied.

For focused read-only worker tasks, use spawn_subagent.

For visible plans, use todo_write or task_create/task_list/task_get/task_update.

If you have enough information, answer normally without calling a tool.`)
	}

	// Build dynamic tool lists based on the current policy.
	allowedTools := policy.AllowedNames()
	askTools := policy.AskNames()

	var b strings.Builder
	b.WriteString(modePrefix)
	b.WriteString("You are Forge, a coding agent inside a terminal workbench.\n\n")

	if len(askTools) > 0 {
		b.WriteString("This sprint allows read-only tools automatically, task/plan tools, limited read-only subagents, small file edits after user approval, and safe test/diff commands through run_command. Do not request external tools, network tools, or MCP tools.\n\n")
	} else {
		b.WriteString("This sprint allows read-only tools and planning tools only. Do not attempt to edit, write, or create files. Use todo_write to describe proposed changes. Do not request external tools, network tools, or MCP tools.\n\n")
	}

	b.WriteString("When you need information, request exactly one tool call using this format:\n")
	b.WriteString(`<tool_call>{"name":"read_file","input":{"path":"docs/ARCHITECTURE.md"}}</tool_call>`)
	b.WriteString("\n\n")

	// Only show edit examples if edit tools are available (ask policy).
	if len(askTools) > 0 {
		b.WriteString("For small edits, prefer:\n")
		b.WriteString(`<tool_call>{"name":"edit_file","input":{"path":"file.txt","old_text":"exact old text","new_text":"replacement text"}}</tool_call>`)
		b.WriteString("\n\nFor new files, use write_file. For patch-shaped output, use apply_patch. Edits require approval and should be small.\n\n")
		b.WriteString(`For verification, use run_command with safe commands such as "go test ./..." or "git diff". Risky commands may require approval or be denied.`)
		b.WriteString("\n\n")
	}

	b.WriteString("For focused read-only worker tasks, use:\n")
	b.WriteString(`<tool_call>{"name":"spawn_subagent","input":{"agent":"explorer","prompt":"find where tools are registered"}}</tool_call>`)
	b.WriteString("\n\n")

	b.WriteString("For visible plans, use todo_write with an items array:\n")
	b.WriteString(`<tool_call>{"name":"todo_write","input":{"items":["Step 1: do X","Step 2: do Y","Step 3: do Z"]}}</tool_call>`)
	b.WriteString("\nOther task tools: task_create, task_list, task_get, task_update.\n\n")

	b.WriteString("Allowed tools: " + strings.Join(allowedTools, ", ") + "\n")
	if len(askTools) > 0 {
		b.WriteString("Approval tools: " + strings.Join(askTools, ", ") + "\n")
	}

	b.WriteString("\nIf you have enough information, answer normally without a tool_call block.")

	return strings.TrimSpace(b.String())
}

func userPrompt(snapshot contextbuilder.Snapshot, userMessage string, pendingTasks string) string {
	prompt := "Context snapshot:\n" + snapshot.Render() + "\n\nUser request:\n" + userMessage
	if pendingTasks != "" {
		prompt += "\n\nPending plan tasks (execute these using write_file, edit_file, or run_command — do NOT just read or explore):\n" + pendingTasks
	}
	return prompt
}
