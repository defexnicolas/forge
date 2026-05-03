package claw

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"forge/internal/llm"
	"forge/internal/tools"
)

// allowedClawToolNames is the read-only, workspace-agnostic subset of tools
// Claw is permitted to invoke. Mutating tools (write_file/edit_file/etc.)
// and workspace-scoped readers (read_file/list_files) are intentionally
// excluded — Claw runs without a workspace cwd, so anything that resolves
// paths against ctx.CWD would either fail or read from whatever directory
// the user happened to launch forge from.
//
// web_search and web_fetch cover the realistic "look something up online"
// use case the user actually asked for. Future additions (e.g. a
// project-state read-only summary) should be limited to tools whose
// PermissionRequest is PermissionAllow and whose Run() does not touch
// ctx.CWD.
var allowedClawToolNames = []string{
	"web_search",
	"web_fetch",
	// whatsapp_send is the only mutating tool Claw can invoke. Its
	// permission is PermissionAsk so the user still gets a confirmation
	// prompt unless they have approval_profile = 'auto'. The channel-
	// level guardrails (typing simulation, rate limit, link guard)
	// stay active either way.
	"whatsapp_send",
	// Contact store: read + write. Both are PermissionAllow because
	// they only touch Claw's local state and never reach the network.
	"claw_save_contact",
	"claw_lookup_contact",
	// Fact memory: free-form preferences, allergies, schedules, etc.
	"claw_remember",
	"claw_recall",
	// Scheduled reminders: one-shot timers that fire a message
	// through a channel at a given time.
	"claw_schedule_reminder",
	"claw_list_reminders",
	"claw_cancel_reminder",
	// Workspace note: lets Claw append prose to its own markdown
	// personality files (MEMORY/SOUL/USER/TOOLS/IDENTITY) mid-
	// conversation. AGENTS.md and HEARTBEAT.md are operator-edited.
	"claw_workspace_note",
}

// clawToolDefs builds the []llm.ToolDef Claw advertises to the LLM. Pulls
// each tool's Schema from the registry so the model sees the same JSON
// schema the main agent does. Returns nil when registry is nil or empty.
func clawToolDefs(registry *tools.Registry) []llm.ToolDef {
	if registry == nil {
		return nil
	}
	defs := make([]llm.ToolDef, 0, len(allowedClawToolNames))
	for _, name := range allowedClawToolNames {
		t, ok := registry.Get(name)
		if !ok {
			continue
		}
		var params json.RawMessage = t.Schema()
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object"}`)
		}
		defs = append(defs, llm.ToolDef{
			Type: "function",
			Function: llm.FunctionDef{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  params,
			},
		})
	}
	return defs
}

// clawToolNamesAllowed mirrors allowedClawToolNames as a quick lookup
// helper for the dispatch loop. Centralised so the slice stays the single
// source of truth.
func clawToolNamesAllowed(name string) bool {
	for _, allowed := range allowedClawToolNames {
		if allowed == name {
			return true
		}
	}
	return false
}

// dispatchClawTool runs one tool call inline. Returns a ContentBlock-style
// observation string suitable for the role:tool message that goes back to
// the model. Errors become tool-result text — the LLM should be allowed
// to see "search failed" and decide what to do, the same way the main
// agent does.
//
// Each dispatch logs to stderr so the user can see in the live forge
// log whether Claw actually invoked a backend (and what it got back).
// Without this it was impossible to tell "the search returned nothing"
// from "the model never called the tool" when an answer felt thin.
func dispatchClawTool(ctx context.Context, registry *tools.Registry, call llm.ToolCall) string {
	if !clawToolNamesAllowed(call.Function.Name) {
		fmt.Fprintf(os.Stderr, "claw tool denied: %s\n", call.Function.Name)
		return "tool not allowed for Claw: " + call.Function.Name
	}
	t, ok := registry.Get(call.Function.Name)
	if !ok {
		fmt.Fprintf(os.Stderr, "claw tool missing from registry: %s\n", call.Function.Name)
		return "tool not registered: " + call.Function.Name
	}
	fmt.Fprintf(os.Stderr, "claw tool dispatch: %s args=%s\n", call.Function.Name, call.Function.Arguments)
	res, err := t.Run(tools.Context{
		Context: ctx,
		// CWD intentionally empty: tools in allowedClawToolNames must not
		// depend on a workspace path. If a future tool here does, this
		// will surface as a path error rather than silently reading from
		// the wrong directory.
		CWD:   "",
		Agent: "claw",
	}, json.RawMessage(call.Function.Arguments))
	if err != nil {
		fmt.Fprintf(os.Stderr, "claw tool error %s: %v\n", call.Function.Name, err)
		return "error: " + err.Error()
	}
	formatted := formatClawToolResult(res)
	previewLen := len(formatted)
	if previewLen > 280 {
		previewLen = 280
	}
	fmt.Fprintf(os.Stderr, "claw tool result %s (%d chars): %s\n", call.Function.Name, len(formatted), formatted[:previewLen])
	return formatted
}

// formatClawToolResult flattens a tools.Result into a single string the
// model can prefix-match. We emit the Summary on its own line and then any
// text Content blocks; everything else (binary blobs, ChangedFiles) is
// dropped because Claw's allowed tools never produce them.
func formatClawToolResult(r tools.Result) string {
	var b strings.Builder
	if s := strings.TrimSpace(r.Summary); s != "" {
		b.WriteString(s)
	}
	for _, block := range r.Content {
		if block.Type != "text" || strings.TrimSpace(block.Text) == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(block.Text)
	}
	if b.Len() == 0 {
		return "(empty result)"
	}
	return b.String()
}

// claw tool-use loop bound. Higher numbers risk pathological back-and-forth
// where Claw keeps re-querying instead of answering; lower numbers cut off
// legitimate "search → read first hit → answer" chains. 4 picks the middle.
const clawMaxToolIterations = 4

// runClawChatWithTools is the tool-aware Chat loop. Returns the assistant's
// final text message after at most clawMaxToolIterations rounds of tool
// dispatch. The conversation passed in is mutated as turns are appended.
//
// When toolsEnabled is false, no tool defs are advertised to the model
// and the loop short-circuits to a single Chat call — the user gets
// plain conversational behaviour with zero tool spend (useful for
// chitchat-heavy use where the model's over-eager web_search invocation
// would otherwise burn the user's Ollama API quota).
//
// When the iteration cap is hit without a tool-free response (Claw kept
// asking for more tools), one final tools-less Chat is issued asking the
// model to synthesize an answer from the tool results gathered so far.
// That converts the previous "(claw stopped after N rounds)" dead-end
// into an actual answer the user can read.
func runClawChatWithTools(ctx context.Context, provider llm.Provider, modelID string, registry *tools.Registry, msgs []llm.Message, temperature *float64, toolsEnabled bool) (string, error) {
	if !toolsEnabled {
		// Plain chat — no tool defs advertised, no loop. The system
		// prompt may still mention the tools exist; the user knows
		// they have to flip ToolsEnabled to actually invoke any.
		resp, err := provider.Chat(ctx, llm.ChatRequest{
			Model:       modelID,
			Messages:    msgs,
			Temperature: temperature,
		})
		if err != nil {
			return "", err
		}
		if resp == nil {
			return "", fmt.Errorf("empty claw chat response")
		}
		return strings.TrimSpace(resp.Content), nil
	}
	defs := clawToolDefs(registry)
	for iter := 0; iter < clawMaxToolIterations; iter++ {
		req := llm.ChatRequest{
			Model:       modelID,
			Messages:    msgs,
			Temperature: temperature,
		}
		if len(defs) > 0 {
			req.Tools = defs
		}
		resp, err := provider.Chat(ctx, req)
		if err != nil {
			return "", err
		}
		if resp == nil {
			return "", fmt.Errorf("empty claw chat response")
		}
		// No tool calls → this is the final answer.
		if len(resp.ToolCalls) == 0 {
			return strings.TrimSpace(resp.Content), nil
		}
		// Append the assistant message with tool_calls + each tool result
		// as a role:tool message, then loop for the next round.
		msgs = append(msgs, llm.Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})
		for _, call := range resp.ToolCalls {
			msgs = append(msgs, llm.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    dispatchClawTool(ctx, registry, call),
			})
		}
	}
	// Iteration cap reached. Force a final answer by asking the model
	// once more *without* tools — it has to write text now. The prompt
	// is firmer than a generic "give an answer" because local models
	// often produce a vague half-sentence after a tool round; this
	// nudges them to actually use the tool data they just collected.
	msgs = append(msgs, llm.Message{
		Role:    "user",
		Content: "Stop calling tools. The tool results above contain real information from the web. Quote the most relevant facts from those results in your answer — do NOT respond as if the search returned nothing. If the results were genuinely empty or off-topic, say so plainly. Reply in the user's language.",
	})
	finalResp, err := provider.Chat(ctx, llm.ChatRequest{
		Model:       modelID,
		Messages:    msgs,
		Temperature: temperature,
		// Tools intentionally omitted — this turn must be text-only.
	})
	if err != nil {
		return "", fmt.Errorf("final claw synthesis failed: %w", err)
	}
	if finalResp == nil || strings.TrimSpace(finalResp.Content) == "" {
		return "(claw could not synthesize an answer from " + fmt.Sprintf("%d", clawMaxToolIterations) + " tool rounds)", nil
	}
	return strings.TrimSpace(finalResp.Content), nil
}
