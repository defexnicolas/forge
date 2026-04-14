package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

type SubagentRegistry struct {
	agents map[string]Subagent
}

func DefaultSubagents() SubagentRegistry {
	agents := []Subagent{
		{
			Name:         "explorer",
			Description:  "Read-only worker for finding relevant files, symbols, and repository facts.",
			ModelRole:    "planner",
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
	model := r.Config.Models[worker.ModelRole]
	if model == "" {
		model = r.Config.Models["chat"]
	}
	snapshot := r.Builder.Build(prompt)
	messages := []llm.Message{
		{Role: "system", Content: subagentSystemPrompt(worker, snapshot)},
		{Role: "user", Content: "Context snapshot:\n" + snapshot.Render() + "\n\nTask:\n" + prompt},
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
		if decision != permissions.Allow {
			return "Tool result for run_command: " + reason, nil
		}
		result, _ := r.runCommandTool(ctx, call.Input, reason)
		return "Tool result for run_command:\n" + summarizeResult(*result), nil
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
