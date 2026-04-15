package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	contextbuilder "forge/internal/context"
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

For one focused read-only worker task, use spawn_subagent. For multiple independent read-only/analysis tasks, use spawn_subagents with max_concurrency up to 8.

For full planning documents, use plan_write and plan_get. Keep executable progress separate from the plan document.

For visible plans, prefer task_create / task_list / task_update for incremental changes. Use todo_write ONLY when the user explicitly asks for a fresh plan — it replaces the whole list and will be rejected if called with empty items while tasks exist.

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
		b.WriteString("This sprint allows read-only tools and planning tools only. Do not attempt to edit, write, or create files. Use plan_write for the full plan document and todo_write/task_* for the executable checklist. Do not request external tools, network tools, or MCP tools.\n\n")
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
	b.WriteString("\nFor multiple independent read-only/analysis tasks, use:\n")
	b.WriteString(`<tool_call>{"name":"spawn_subagents","input":{"max_concurrency":3,"tasks":[{"agent":"explorer","prompt":"find where tools are registered"},{"agent":"reviewer","prompt":"inspect current diff for risks"}]}}</tool_call>`)
	b.WriteString("\n\n")

	b.WriteString("For full plan documents, use:\n")
	b.WriteString(`<tool_call>{"name":"plan_get","input":{}}</tool_call>` + "  read the current plan document\n")
	b.WriteString(`<tool_call>{"name":"plan_write","input":{"summary":"goal","context":"repo facts","assumptions":["assumption"],"approach":"implementation strategy","stubs":["file/function stub"],"risks":["risk"],"validation":["go test ./..."]}}</tool_call>` + "  save the detailed plan\n\n")

	b.WriteString("For executable checklist management, prefer incremental task tools:\n")
	b.WriteString(`<tool_call>{"name":"task_list","input":{}}</tool_call>` + "  read current checklist\n")
	b.WriteString(`<tool_call>{"name":"task_create","input":{"title":"Step 1: do X"}}</tool_call>` + "  add a task\n")
	b.WriteString(`<tool_call>{"name":"task_update","input":{"id":"plan-1","status":"completed"}}</tool_call>` + "  mark progress\n")
	b.WriteString("\nUse todo_write only when starting from scratch with the full checklist; it replaces the checklist and is rejected if called with an empty items array while tasks exist. items accepts strings (preferred) or {title, status?, notes?} objects.\n")
	b.WriteString("When narrating progress in prose, ALWAYS use the exact IDs returned by task_list (e.g. plan-1, plan-2). Do NOT renumber tasks in your own narration — this confuses the user about which task you are working on.\n\n")

	b.WriteString("Allowed tools: " + strings.Join(allowedTools, ", ") + "\n")
	if len(askTools) > 0 {
		b.WriteString("Approval tools: " + strings.Join(askTools, ", ") + "\n")
	}

	b.WriteString("\nIf you have enough information, answer normally without a tool_call block.")

	return strings.TrimSpace(b.String())
}

func userPrompt(snapshot contextbuilder.Snapshot, userMessage, planBlock, mode, handoff, explorerHandoff, buildPreflight string) string {
	modeTag := ""
	if mode != "" {
		modeTag = fmt.Sprintf("[mode: %s]\n", strings.ToUpper(mode))
	}
	prompt := modeTag + "Context snapshot:\n" + snapshot.Render() + "\n\nUser request:\n" + userMessage
	if planBlock != "" {
		prompt += "\n\n" + planBlock
	}
	if handoff != "" {
		prompt += "\n\n" + handoff
	}
	if strings.TrimSpace(explorerHandoff) != "" {
		prompt += "\n\nEXPLORER FINDINGS:\n" + strings.TrimSpace(explorerHandoff) +
			"\n\nUse the explorer findings to confirm repository facts and create or refine the full plan document. Do not edit files. Use plan_write for the detailed plan and todo_write/task_* for the executable checklist."
	}
	if strings.TrimSpace(buildPreflight) != "" {
		prompt += "\n\nBUILD PREFLIGHT FINDINGS:\n" + strings.TrimSpace(buildPreflight) +
			"\n\nUse these read-only subagent findings to execute the existing plan. Do not rerun the same preflight unless new information is required."
	}
	return prompt
}
