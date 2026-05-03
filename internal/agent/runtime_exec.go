package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	contextbuilder "forge/internal/context"
	"forge/internal/llm"
	"forge/internal/patch"
	"forge/internal/permissions"
	"forge/internal/plans"
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
	// read_file fast-path: serve from per-turn cache if we already read this
	// path in the current turn. The agent often re-reads the same file
	// across consecutive steps; without this each one is a wasted round-trip.
	if call.Name == "read_file" {
		if cached, obs, ok := r.lookupReadCache(call.Input); ok {
			return cached, obs
		}
	}
	result, observation := r.executeToolInner(ctx, call, events)
	if call.Name == "read_file" && result != nil && strings.TrimSpace(result.Summary) != "" {
		// Only cache successful reads — failed reads (parse error, missing
		// file) leave Content empty and the model should retry, not stick.
		if len(result.Content) > 0 {
			r.storeReadCache(call.Input, result, observation)
		}
	}
	var changed []string
	if result != nil {
		changed = result.ChangedFiles
	}
	if len(changed) > 0 {
		// Any file-mutating tool invalidates cached preflight findings —
		// those were computed against pre-mutation state.
		r.InvalidatePreflightCache()
		// Drop just the affected paths from the read cache so the next
		// read_file on those paths sees the post-mutation bytes.
		r.invalidateReadCachePaths(changed)
	}
	if call.Name == "run_command" {
		// run_command can write any file (build outputs, generated code,
		// etc.) and we have no way to enumerate them from the result —
		// flush the entire read cache to be safe.
		r.flushReadCache()
	}
	if r.Hooks != nil {
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
			hint += ". In plan mode, use plan_write for the full plan and todo_write/task_* for the executable checklist instead of editing files directly."
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
	if canonicalName == "spawn_subagents" {
		return r.executeSubagents(ctx, call.Input)
	}
	if canonicalName == "execute_task" {
		return r.executeExecuteTask(ctx, call.Input)
	}
	if strings.HasPrefix(canonicalName, "plan_") {
		return r.executePlan(canonicalName, call.Input)
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

func (r *Runtime) executeSubagents(ctx context.Context, input json.RawMessage) (*tools.Result, string) {
	var req SubagentBatchRequest
	if err := json.Unmarshal(input, &req); err != nil {
		result := tools.Result{Title: "spawn_subagents", Summary: err.Error()}
		return &result, "Tool result for spawn_subagents: error: " + err.Error()
	}
	result, err := r.RunSubagents(ctx, req)
	if err != nil {
		result := tools.Result{Title: "spawn_subagents", Summary: err.Error()}
		return &result, "Tool result for spawn_subagents: error: " + err.Error()
	}
	return &result, "Tool result for spawn_subagents:\n" + summarizeResult(result)
}

// executeExecuteTask delegates one checklist task to the "builder" subagent.
// The builder receives ONLY the task (title + notes) and an optional
// relevant_files hint as context — never the full plan document. This keeps
// the editor-role prompt lean so a smaller/faster model can execute while the
// planner (Gemma) keeps the high-level state.
func (r *Runtime) executeExecuteTask(ctx context.Context, input json.RawMessage) (*tools.Result, string) {
	var req executeTaskDispatchRequest
	if err := json.Unmarshal(input, &req); err != nil {
		result := tools.Result{Title: "execute_task", Summary: err.Error()}
		return &result, "Tool result for execute_task: error: " + err.Error()
	}
	taskID := strings.TrimSpace(req.TaskID)
	if taskID == "" {
		taskID = strings.TrimSpace(req.ID)
	}
	if taskID == "" {
		msg := "task_id is required"
		result := tools.Result{Title: "execute_task", Summary: msg}
		return &result, "Tool result for execute_task: error: " + msg
	}
	if r.Tasks == nil {
		msg := "tasks store unavailable"
		result := tools.Result{Title: "execute_task", Summary: msg}
		return &result, "Tool result for execute_task: error: " + msg
	}
	task, err := r.Tasks.Get(taskID)
	if err != nil {
		result := tools.Result{Title: "execute_task", Summary: err.Error()}
		return &result, "Tool result for execute_task: error: " + err.Error()
	}
	prompt := task.Title
	if task.Notes != "" {
		prompt += "\n\n" + task.Notes
	}
	if strings.TrimSpace(req.Notes) != "" {
		prompt += "\n\nAdditional guidance:\n" + strings.TrimSpace(req.Notes)
	}
	guard := deriveBuilderTaskGuard(task, req)
	contextPayload := map[string]any{
		"task": map[string]string{
			"id":     task.ID,
			"title":  task.Title,
			"status": task.Status,
			"notes":  task.Notes,
		},
	}
	if gitFact := strings.TrimSpace(r.GitSessionState().PromptFact()); gitFact != "" {
		contextPayload["git_state"] = gitFact
	}
	if len(req.RelevantFiles) > 0 {
		contextPayload["relevant_files"] = req.RelevantFiles
	}
	if guard.TargetFile != "" {
		contextPayload["target_file"] = guard.TargetFile
	}
	if guard.FileStrategy != "" {
		contextPayload["file_strategy"] = guard.FileStrategy
	}
	if guard.SectionGoal != "" {
		contextPayload["section_goal"] = guard.SectionGoal
	}
	if r.Plans != nil && r.Tasks != nil {
		if planDoc, ok, err := r.Plans.Current(); err == nil && ok {
			taskList, _ := r.Tasks.List()
			if digest := compactPlanDigest(planDoc, taskList); digest != "" {
				contextPayload["approved_plan_digest"] = digest
			}
		}
	}
	contextJSON, _ := json.Marshal(contextPayload)
	subReq := SubagentRequest{
		Agent:   "builder",
		Prompt:  prompt,
		Context: contextJSON,
	}
	taskCtx, cancel := withOptionalTimeout(ctx, r.taskTimeout())
	defer cancel()
	r.setActiveBuilderTask(&guard)
	defer r.setActiveBuilderTask(nil)
	result, err := r.RunSubagent(taskCtx, subReq)
	if err != nil {
		var runErr *subagentRunError
		if errors.As(err, &runErr) {
			out := buildExecuteTaskFailureResult(task.ID, runErr)
			return &out, "Tool result for execute_task: error: " + out.Summary
		}
		out := tools.Result{Title: "execute_task", Summary: err.Error()}
		return &out, "Tool result for execute_task: error: " + err.Error()
	}
	summary := fmt.Sprintf("builder completed task %s: %s", task.ID, result.Summary)
	out := tools.Result{
		Title:        "execute_task",
		Summary:      summary,
		Content:      result.Content,
		ChangedFiles: result.ChangedFiles,
	}
	return &out, "Tool result for execute_task:\n" + summarizeResult(out)
}

func (r *Runtime) executeTodoWrite(input json.RawMessage) (*tools.Result, string) {
	items, content, err := parseTodoWriteInput(input)
	if err != nil {
		result := tools.Result{Title: "todo_write", Summary: err.Error()}
		return &result, "Tool result for todo_write: error: " + err.Error()
	}
	// If no items but content string is provided, split content into items.
	if len(items) == 0 && content != "" {
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			line = strings.TrimLeft(line, "-*•0123456789. ")
			line = strings.TrimPrefix(line, "[ ] ")
			line = strings.TrimPrefix(line, "[x] ")
			line = strings.TrimPrefix(line, "☐ ")
			line = strings.TrimPrefix(line, "☑ ")
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "---") {
				items = append(items, line)
			}
		}
	}
	items = normalizeChecklistItems(items)
	plan, err := r.Tasks.ReplacePlan(items)
	if err != nil {
		result := tools.Result{Title: "todo_write", Summary: err.Error()}
		return &result, "Tool result for todo_write: error: " + err.Error()
	}
	result := tools.Result{
		Title:   "Todo plan",
		Summary: formatPlanForDisplay(plan),
		Content: []tools.ContentBlock{{Type: "text", Text: tasks.Format(plan)}},
	}
	return &result, "Tool result for todo_write:\n" + summarizeResult(result)
}

// parseTodoWriteInput accepts both `items:["a","b"]` and the object form
// `items:[{"title":"a","status":"completed"}]` that small models often emit
// after seeing task_list output. Object items get converted to strings with
// the right status prefix (e.g. "[x] title") so parsePlanStatus normalizes
// them through the existing path.
func parseTodoWriteInput(input json.RawMessage) (items []string, content string, err error) {
	var raw struct {
		Items   json.RawMessage `json:"items"`
		Content string          `json:"content"`
	}
	if uerr := json.Unmarshal(input, &raw); uerr != nil {
		return nil, "", fmt.Errorf("todo_write: invalid JSON payload: %v", uerr)
	}
	content = raw.Content
	if len(raw.Items) == 0 || string(raw.Items) == "null" {
		return nil, content, nil
	}
	// Fast path: array of strings.
	var asStrings []string
	if jerr := json.Unmarshal(raw.Items, &asStrings); jerr == nil {
		return asStrings, content, nil
	}
	// Object form.
	var objs []struct {
		Title  string `json:"title"`
		Status string `json:"status"`
		Notes  string `json:"notes"`
	}
	if jerr := json.Unmarshal(raw.Items, &objs); jerr == nil {
		for _, o := range objs {
			title := strings.TrimSpace(o.Title)
			if title == "" {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(o.Status)) {
			case "completed", "done":
				title = "[x] " + title
			case "in_progress", "doing", "running":
				title = "[>] " + title
			}
			if n := strings.TrimSpace(o.Notes); n != "" {
				title = title + " — " + n
			}
			items = append(items, title)
		}
		return items, content, nil
	}
	preview := string(raw.Items)
	if len(preview) > 200 {
		preview = preview[:200] + "..."
	}
	return nil, content, fmt.Errorf("todo_write: 'items' must be an array of strings (e.g. [\"step 1\",\"step 2\"]) OR an array of {title, status?, notes?} objects. Got: %s", preview)
}

// formatPlanForDisplay renders the plan as a multi-line summary with status icons
// so the TUI can show the full plan inline rather than a truncated "Updated plan" line.
func formatPlanForDisplay(plan []tasks.Task) string {
	if len(plan) == 0 {
		return "Plan cleared"
	}
	var b strings.Builder
	b.WriteString("Updated checklist:\n")
	for _, task := range plan {
		icon := "[ ]"
		switch task.Status {
		case "completed", "done":
			icon = "[x]"
		case "in_progress", "running":
			icon = "[>]"
		}
		b.WriteString("  ")
		b.WriteString(icon)
		b.WriteString(" ")
		b.WriteString(task.Title)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
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

func (r *Runtime) executePlan(toolName string, input json.RawMessage) (*tools.Result, string) {
	result, err := r.runPlanTool(toolName, input)
	if err != nil {
		result := tools.Result{Title: toolName, Summary: err.Error()}
		return &result, "Tool result for " + toolName + ": error: " + err.Error()
	}
	return &result, "Tool result for " + toolName + ":\n" + summarizeResult(result)
}

func (r *Runtime) runPlanTool(toolName string, input json.RawMessage) (tools.Result, error) {
	switch toolName {
	case "plan_write":
		var req struct {
			Summary     string          `json:"summary"`
			Context     string          `json:"context"`
			Assumptions json.RawMessage `json:"assumptions"`
			Approach    string          `json:"approach"`
			Stubs       json.RawMessage `json:"stubs"`
			Risks       json.RawMessage `json:"risks"`
			Validation  json.RawMessage `json:"validation"`
		}
		if err := json.Unmarshal(input, &req); err != nil {
			return tools.Result{}, err
		}
		doc, err := r.Plans.Save(plans.Document{
			Summary:     req.Summary,
			Context:     req.Context,
			Assumptions: parsePlanList(req.Assumptions),
			Approach:    req.Approach,
			Stubs:       parsePlanList(req.Stubs),
			Risks:       parsePlanList(req.Risks),
			Validation:  parsePlanList(req.Validation),
		})
		if err != nil {
			return tools.Result{}, err
		}
		return planResult("Saved plan", doc), nil
	case "plan_get":
		doc, ok, err := r.Plans.Current()
		if err != nil {
			return tools.Result{}, err
		}
		if !ok {
			return tools.Result{Title: "Plan", Summary: "No plan yet.", Content: []tools.ContentBlock{{Type: "text", Text: "No plan yet."}}}, nil
		}
		return planResult("Plan", doc), nil
	default:
		return tools.Result{}, fmt.Errorf("unknown plan tool: %s", toolName)
	}
}

func planResult(title string, doc plans.Document) tools.Result {
	text := plans.Format(doc)
	return tools.Result{
		Title:   title,
		Summary: text,
		Content: []tools.ContentBlock{{Type: "text", Text: text}},
	}
}

func parsePlanList(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		lines := strings.Split(single, "\n")
		out := make([]string, 0, len(lines))
		for _, line := range lines {
			line = strings.TrimSpace(strings.TrimLeft(line, "-*0123456789. "))
			if line != "" {
				out = append(out, line)
			}
		}
		return out
	}
	return nil
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
	if decision == permissions.Ask && !r.autoApproveMode() {
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
	approvalDiff, beforeApply, err := r.prepareGitBackedMutation(summary, plan)
	if err != nil {
		result := tools.Result{Title: toolName, Summary: err.Error()}
		return &result, "Tool result for " + toolName + ": error: " + err.Error()
	}
	request := &ApprovalRequest{
		ID:          fmt.Sprintf("approval-%p", &plan),
		ToolName:    toolName,
		Input:       input,
		Summary:     summary,
		Diff:        approvalDiff,
		Response:    make(chan ApprovalResponse, 1),
		plan:        plan,
		beforeApply: beforeApply,
	}
	if r.autoApproveMode() {
		// Auto-approve mode: skip the interactive prompt entirely. The
		// mutation still flows through prepareMutation / patch.Apply /
		// undoStack below, so the audit trail (diff in Result.Content,
		// undo entry, RefreshGitSessionState) is identical to the
		// approved path. Only the human round-trip is removed.
		request.Response <- ApprovalResponse{Approved: true}
	} else {
		events <- Event{Type: EventApproval, ToolName: toolName, Input: input, Approval: request}
	}
	select {
	case <-ctx.Done():
		result := tools.Result{Title: toolName, Summary: ctx.Err().Error()}
		return &result, "Tool result for " + toolName + ": error: " + ctx.Err().Error()
	case response := <-request.Response:
		if !response.Approved {
			result := tools.Result{Title: toolName, Summary: "rejected by user"}
			return &result, "Tool result for " + toolName + ": rejected by user"
		}
		if len(request.plan.Operations) == 0 {
			// This can happen when prepareMutation succeeded structurally
			// (no Unmarshal error) but the resulting plan had no
			// operations — e.g. a patch with zero hunks. Surface it
			// explicitly instead of silently "approving" a no-op.
			msg := "no file operations in plan — nothing to apply"
			fmt.Fprintf(os.Stderr, "approval-apply skipped (%s): %s\n", toolName, msg)
			result := tools.Result{Title: toolName, Summary: msg}
			return &result, "Tool result for " + toolName + ": " + msg
		}
		if request.beforeApply != nil {
			if err := request.beforeApply(); err != nil {
				result := tools.Result{Title: toolName, Summary: "prepare failed: " + err.Error()}
				return &result, "Tool result for " + toolName + ": error: " + err.Error()
			}
		}
		snapshots, err := patch.Apply(r.CWD, request.plan)
		if err != nil {
			fmt.Fprintf(os.Stderr, "approval-apply failed (%s): %v — plan had %d op(s)\n", toolName, err, len(request.plan.Operations))
			result := tools.Result{Title: toolName, Summary: "apply failed: " + err.Error()}
			return &result, "Tool result for " + toolName + ": error: " + err.Error()
		}
		r.RefreshGitSessionState()
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
		if err := r.validateWriteFileMutation(req.Path, content); err != nil {
			return patch.Plan{}, "", err
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
	// Models have been observed emitting ask_user in several shapes:
	//   {"question": "..."}             — the documented form
	//   {"message": "..."}              — common alternate
	//   {"prompt": "..."} / {"text": …} — also seen in the wild
	//   "just a raw string"             — some models skip the object wrapper
	// Accept all of them so the user always sees the actual question.
	question := ""
	var questions []string
	var options []string
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var raw string
		if err := json.Unmarshal(trimmed, &raw); err == nil {
			question = raw
		}
	} else {
		var req struct {
			Question    string   `json:"question"`
			Message     string   `json:"message"`
			Prompt      string   `json:"prompt"`
			Text        string   `json:"text"`
			Questions   []string `json:"questions"`
			Options     []string `json:"options"`
			Choices     []string `json:"choices"`
			Suggestions []string `json:"suggestions"`
		}
		if err := json.Unmarshal(input, &req); err != nil {
			result := tools.Result{Title: "ask_user", Summary: err.Error()}
			return &result, "Tool result for ask_user: error: " + err.Error()
		}
		question = req.Question
		for _, alt := range []string{req.Message, req.Prompt, req.Text} {
			if question == "" && alt != "" {
				question = alt
			}
		}
		for _, q := range req.Questions {
			if trimmed := strings.TrimSpace(q); trimmed != "" {
				questions = append(questions, trimmed)
			}
		}
		if question == "" && len(questions) > 0 {
			question = strings.Join(questions, "\n")
		}
		// Collect suggested answers from any of the known aliases and cap at
		// 3 — the TUI always appends a "Write my own" row after them.
		for _, src := range [][]string{req.Options, req.Choices, req.Suggestions} {
			for _, s := range src {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					options = append(options, trimmed)
				}
			}
		}
		if len(options) > 3 {
			options = options[:3]
		}
	}
	if question == "" && len(questions) == 0 {
		question = "(model asked a question with no text — please type your answer or /reject)"
	}
	// Normalize into a list so we can ask them one-by-one. Single-question
	// payloads produce a one-element list.
	askList := questions
	if len(askList) == 0 {
		askList = []string{question}
	}

	var answers []string
	for i, q := range askList {
		request := &AskUserRequest{
			Question: q,
			Index:    i,
			Total:    len(askList),
			Response: make(chan string, 1),
		}
		if len(askList) > 1 {
			request.Questions = append([]string(nil), askList...)
		}
		// Attach suggested answers only to the first question when the model
		// emitted a single options list; the subsequent questions are free-form.
		if i == 0 && len(options) > 0 {
			request.Options = append([]string(nil), options...)
		}
		events <- Event{Type: EventAskUser, ToolName: "ask_user", Input: input, AskUser: request}
		select {
		case <-ctx.Done():
			result := tools.Result{Title: "ask_user", Summary: ctx.Err().Error()}
			return &result, "Tool result for ask_user: error: " + ctx.Err().Error()
		case answer := <-request.Response:
			answers = append(answers, answer)
		}
	}

	var summary string
	var content string
	if len(askList) == 1 {
		summary = answers[0]
		content = "User answered: " + answers[0]
	} else {
		var b strings.Builder
		b.WriteString("User answered:")
		for i, a := range answers {
			fmt.Fprintf(&b, "\n  %d. %s — %s", i+1, askList[i], a)
		}
		summary = b.String()
		content = summary
	}
	result := tools.Result{
		Title:   "User answer",
		Summary: summary,
		Content: []tools.ContentBlock{{Type: "text", Text: content}},
	}
	return &result, "Tool result for ask_user: " + content
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

// compactOldToolResults stubs all tool-role messages except the last `keep`,
// replacing their content with a short marker. Tool results are the single
// largest growing fraction of prompt tokens on long agent runs — a single
// read_file can push 10k tokens that stay in the context for the rest of
// the turn-chain. The model can re-invoke the tool if it needs the detail
// again; that's cheaper than paying to re-prefill the old result on every
// subsequent step.
//
// The stub names the originating tool when it can be inferred from the
// existing observation prefix ("Tool result for <name>: ..."). Naming the
// tool helps the model decide whether re-calling is worth the round-trip:
// re-reading a file is cheap, re-running a long shell command is not.
func compactOldToolResults(messages []llm.Message, keep int) {
	if keep < 0 {
		keep = 0
	}
	toolIdx := make([]int, 0, len(messages))
	for i, m := range messages {
		if m.Role == "tool" {
			toolIdx = append(toolIdx, i)
		}
	}
	if len(toolIdx) <= keep {
		return
	}
	stubCount := len(toolIdx) - keep
	for i := 0; i < stubCount; i++ {
		idx := toolIdx[i]
		content := messages[idx].Content
		if strings.HasPrefix(content, "[compacted]") {
			continue
		}
		name := extractToolNameFromObservation(content)
		if name == "" {
			messages[idx].Content = "[compacted] earlier tool result omitted — re-call the tool if you need the content again."
			continue
		}
		messages[idx].Content = "[compacted] earlier " + name + " result omitted — re-call " + name + " if you need it again."
	}
}

// extractToolNameFromObservation parses "Tool result for <name>: ..." (the
// stable prefix produced by executeToolInner) and returns the bare tool name.
// Returns "" if the prefix is missing — preserving the generic stub.
func extractToolNameFromObservation(content string) string {
	const prefix = "Tool result for "
	if !strings.HasPrefix(content, prefix) {
		return ""
	}
	rest := content[len(prefix):]
	colon := strings.IndexByte(rest, ':')
	if colon <= 0 {
		return ""
	}
	name := strings.TrimSpace(rest[:colon])
	// Defensively cap length so a malformed observation cannot inject a
	// novel-length string into the prompt.
	if len(name) > 64 {
		return ""
	}
	return name
}

// keepLastToolResultsForMode chooses how many recent tool results survive
// compactOldToolResults. Build mode runs many more tool calls per turn than
// plan/explore (read → analyze → edit → verify per task), so the prefill
// savings from a tighter window are larger there.
func keepLastToolResultsForMode(mode string) int {
	if mode == "build" {
		return 2
	}
	return 3
}

func summarizeResult(result tools.Result) string {
	compacted := compactResultForPrompt(result)
	payload, err := json.Marshal(compacted)
	if err != nil {
		return compacted.Summary
	}
	limit := 8000
	if isReadFileResult(result) {
		limit = 14000
	}
	if len(payload) > limit {
		payload = payload[:limit]
		return string(payload) + "\n[truncated]"
	}
	return string(payload)
}

func compactResultForPrompt(result tools.Result) tools.Result {
	out := result
	if isReadFileResult(out) {
		if len(out.Content) > 1 {
			out.Content = append([]tools.ContentBlock(nil), out.Content[:1]...)
		}
		for i := range out.Content {
			out.Content[i].Text = compactPromptText(out.Content[i].Text, 9000)
		}
		if out.Summary != "" {
			out.Summary = compactPromptText(out.Summary, 400)
		}
		return out
	}
	if len(out.Content) > 2 {
		out.Content = append([]tools.ContentBlock(nil), out.Content[:2]...)
	}
	for i := range out.Content {
		out.Content[i].Text = compactPromptText(out.Content[i].Text, 2800)
	}
	if out.Summary != "" {
		out.Summary = compactPromptText(out.Summary, 1200)
	}
	return out
}

func isReadFileResult(result tools.Result) bool {
	if strings.EqualFold(strings.TrimSpace(result.Title), "Read file") {
		return true
	}
	if len(result.Content) == 1 && strings.TrimSpace(result.Content[0].Path) != "" && len(result.ChangedFiles) == 0 {
		return true
	}
	return false
}

func compactPromptText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit < 64 {
		return text[:limit]
	}
	marker := "\n[...truncated...]\n"
	head := (limit * 2) / 3
	tail := limit - head - len(marker)
	if tail < 16 {
		tail = 16
		head = limit - tail - len(marker)
	}
	if head < 16 {
		head = 16
	}
	return text[:head] + marker + text[len(text)-tail:]
}

func systemPrompt(nativeToolCalling bool, modeName string, policy SprintPolicy) string {
	modePrefix := ""
	if mode, ok := GetMode(modeName); ok {
		modePrefix = mode.Prompt + "\n\n"
	}
	allowedTool := func(name string) bool {
		ok, _ := policy.Allowed(name)
		return ok
	}

	if nativeToolCalling {
		suffix := "If you have enough information, answer normally without calling a tool."
		if modeName == "build" {
			suffix = "If checklist tasks remain, do not answer normally. Return exactly one tool call for the next action. Only answer in prose when all tasks are complete or when blocked and sending the user back to plan mode."
		}
		return strings.TrimSpace(modePrefix + `You are Forge, a coding agent inside a terminal workbench.

You have access to tools via function calling. Use them to read files, search code, run commands, and make edits. Edits require user approval and should be small and reversible.

For verification, use run_command with safe commands such as "go test ./..." or "git diff". Risky commands may require approval or be denied.

For small self-contained artifacts, prefer one coherent runnable result over scaffold/layout/polish fragmentation. Only use section-by-section execution for genuinely large files or when the task context explicitly requires it.

For one focused read-only worker task, use spawn_subagent. For multiple independent read-only/analysis tasks, use spawn_subagents with max_concurrency up to 8.

For full planning documents, use plan_write and plan_get. Keep executable progress separate from the plan document.

For visible plans, prefer task_create / task_list / task_update for incremental changes. Use todo_write ONLY when the user explicitly asks for a fresh plan — it replaces the whole list and will be rejected if called with empty items while tasks exist.

` + suffix)
	}

	// Build dynamic tool lists based on the current policy.
	allowedTools := policy.AllowedNames()
	askTools := policy.AskNames()

	var b strings.Builder
	b.WriteString(modePrefix)
	b.WriteString("You are Forge, a coding agent inside a terminal workbench.\n\n")

	if len(askTools) > 0 {
		b.WriteString("This mode allows read-only tools automatically, task/plan tools, limited read-only subagents, small file edits after user approval, and safe test/diff commands through run_command. Do not request external tools, network tools, or MCP tools.\n\n")
	} else {
		b.WriteString("This mode allows read-only tools and planning tools only. Do not attempt to edit, write, or create files. Use plan_write for the full plan document and todo_write/task_* for the executable checklist. Do not request external tools, network tools, or MCP tools.\n\n")
	}

	b.WriteString("When you need information, request exactly one tool call using this format:\n")
	b.WriteString(`<tool_call>{"name":"read_file","input":{"path":"docs/ARCHITECTURE.md"}}</tool_call>`)
	b.WriteString("\n\n")

	// Only show edit examples if edit tools are available (ask policy).
	if len(askTools) > 0 {
		b.WriteString("For small edits, prefer:\n")
		b.WriteString(`<tool_call>{"name":"edit_file","input":{"path":"file.txt","old_text":"exact old text","new_text":"replacement text"}}</tool_call>`)
		b.WriteString("\n\nFor small self-contained artifacts, prefer one coherent write_file or apply_patch that leaves the artifact runnable. Use scaffold_then_patch only for genuinely large files or when task context explicitly asks for section-by-section work. Do not turn a simple new HTML/CSS/JS artifact into scaffold/layout/polish subtasks unless the task is clearly large.\n\n")
		b.WriteString(`For verification, use run_command with safe commands such as "go test ./..." or "git diff". Risky commands may require approval or be denied.`)
		b.WriteString("\n\n")
	}

	if allowedTool("spawn_subagent") || allowedTool("spawn_subagents") {
		b.WriteString("For focused read-only worker tasks, use:\n")
		b.WriteString(`<tool_call>{"name":"spawn_subagent","input":{"agent":"explorer","prompt":"find where tools are registered"}}</tool_call>`)
		b.WriteString("\nFor multiple independent read-only/analysis tasks, use:\n")
		b.WriteString(`<tool_call>{"name":"spawn_subagents","input":{"max_concurrency":3,"tasks":[{"agent":"explorer","prompt":"find where tools are registered"},{"agent":"reviewer","prompt":"inspect current diff for risks"}]}}</tool_call>`)
		b.WriteString("\n\n")
	}

	if allowedTool("plan_get") || allowedTool("plan_write") {
		b.WriteString("For full plan documents, use:\n")
		if allowedTool("plan_get") {
			b.WriteString(`<tool_call>{"name":"plan_get","input":{}}</tool_call>` + "  read the current plan document\n")
		}
		if allowedTool("plan_write") {
			b.WriteString(`<tool_call>{"name":"plan_write","input":{"summary":"goal","context":"repo facts","assumptions":["assumption"],"approach":"implementation strategy","stubs":["file/function stub"],"risks":["risk"],"validation":["go test ./..."]}}</tool_call>` + "  save the detailed plan\n")
		}
		b.WriteString("\n")
	}

	if allowedTool("task_list") || allowedTool("task_create") || allowedTool("task_update") {
		b.WriteString("For executable checklist management, prefer incremental task tools:\n")
		if allowedTool("task_list") {
			b.WriteString(`<tool_call>{"name":"task_list","input":{}}</tool_call>` + "  read current checklist\n")
		}
		if allowedTool("task_create") {
			b.WriteString(`<tool_call>{"name":"task_create","input":{"title":"Step 1: do X"}}</tool_call>` + "  add a task\n")
		}
		if allowedTool("task_update") {
			b.WriteString(`<tool_call>{"name":"task_update","input":{"id":"plan-1","status":"completed"}}</tool_call>` + "  mark progress\n")
		}
		if allowedTool("todo_write") {
			b.WriteString("\nUse todo_write only when starting from scratch with the full checklist; it replaces the checklist and is rejected if called with an empty items array while tasks exist. items accepts strings (preferred) or {title, status?, notes?} objects.\n")
		}
		b.WriteString("When narrating progress in prose, ALWAYS use the exact IDs returned by task_list (e.g. plan-1, plan-2). Do NOT renumber tasks in your own narration — this confuses the user about which task you are working on.\n\n")
	}

	b.WriteString("Allowed tools: " + strings.Join(allowedTools, ", ") + "\n")
	if len(askTools) > 0 {
		b.WriteString("Approval tools: " + strings.Join(askTools, ", ") + "\n")
	}

	if modeName == "build" {
		b.WriteString("\nIf checklist tasks remain, do not answer normally without a tool_call block. Return exactly one tool_call for the next action. Only answer in prose when all tasks are complete or when blocked and sending the user back to plan mode.")
	} else {
		b.WriteString("\nIf you have enough information, answer normally without a tool_call block.")
	}

	return strings.TrimSpace(b.String())
}

// userPrompt assembles the user-role message as a tiered prompt, ordered
// strictly stable → session → turn → user request. The first two tiers are
// byte-identical across consecutive turns within a session while their source
// items do not change, so LM Studio's token-level prompt cache hits the
// entire prefix and only re-prefills tier C plus the fresh user request.
// Everything volatile (yarn-scored context, handoffs, the user message
// itself) is pushed to the tail.
func userPrompt(snapshot contextbuilder.Snapshot, userMessage, planBlock, mode, handoff, explorerHandoff, buildPreflight string) string {
	var b strings.Builder

	b.WriteString("=== STABLE CONTEXT ===\n")
	b.WriteString(snapshot.RenderStable())
	b.WriteString("\n\n")

	if session := snapshot.RenderSession(); session != "" {
		b.WriteString("=== SESSION CONTEXT ===\n")
		b.WriteString(session)
		b.WriteString("\n\n")
	}

	b.WriteString("=== TURN CONTEXT ===\n")
	if turn := snapshot.RenderTurn(); turn != "" {
		b.WriteString(turn)
		b.WriteString("\n")
	}
	if strings.TrimSpace(planBlock) != "" {
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(planBlock))
		b.WriteString("\n")
	}
	if strings.TrimSpace(handoff) != "" {
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(handoff))
		b.WriteString("\n")
	}
	if strings.TrimSpace(explorerHandoff) != "" {
		b.WriteString("\nEXPLORER FINDINGS:\n")
		b.WriteString(strings.TrimSpace(explorerHandoff))
		b.WriteString("\nUse the explorer findings to confirm repository facts and create or refine the full plan document. Do not edit files. Use plan_write for the detailed plan and todo_write/task_* for the executable checklist.\n")
	}
	if strings.TrimSpace(buildPreflight) != "" {
		b.WriteString("\nBUILD PREFLIGHT FINDINGS:\n")
		b.WriteString(strings.TrimSpace(buildPreflight))
		b.WriteString("\nUse these read-only subagent findings to execute the existing plan. Do not rerun the same preflight unless new information is required.\n")
	}

	if mode != "" {
		fmt.Fprintf(&b, "\nMode: %s\n", strings.ToUpper(mode))
	}
	b.WriteString("\n=== USER REQUEST ===\n")
	b.WriteString(userMessage)
	return b.String()
}
