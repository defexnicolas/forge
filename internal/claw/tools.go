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
	// Recurring crons: heartbeat-driven prompts that re-fire on a
	// schedule. Each firing runs as its own Claw chat with tools, so
	// "every morning send Sebastián a check-in" really sends.
	"claw_add_cron",
	"claw_list_crons",
	"claw_remove_cron",
	// Self-introspection: lets Claw read its own recent memory/facts
	// before answering, and trigger a dream pass on demand.
	"claw_recent_memory",
	"claw_dream_now",
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
//
// Two distinct bailout-retry paths run inside the loop:
//
//   - looksLikeToolBailout: model refuses outright at the prefix
//     ("Lo siento, no puedo configurar recordatorios..."). Caught when
//     resp.ToolCalls is empty on iter 0.
//   - looksLikePartialBailout: model completes one tool but invents an
//     excuse for skipping another ("El recordatorio está configurado.
//     Sin embargo, el mensaje por WhatsApp no se pudo enviar porque el
//     canal no está registrado..." while whatsapp_send was never even
//     called). Caught at every iteration when calledTools shows the
//     model skipped the tool the user clearly asked for. Retries once
//     per call with a nudge that names the missing tool.
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
	calledTools := map[string]bool{}
	partialBailoutRetried := false
	// forceToolName, when non-empty, pins tool_choice to that specific
	// function for ONE iteration. Cleared after the call so the loop
	// returns to its normal "auto" behaviour. Used exclusively by the
	// partial-bailout retry path to make the model unable to fall back
	// into prose excuses — the provider literally rejects a text-only
	// completion when tool_choice is pinned.
	forceToolName := ""
	for iter := 0; iter < clawMaxToolIterations; iter++ {
		req := llm.ChatRequest{
			Model:       modelID,
			Messages:    msgs,
			Temperature: temperature,
		}
		if len(defs) > 0 {
			req.Tools = defs
			if forceToolName != "" {
				req.ToolChoice = map[string]any{
					"type":     "function",
					"function": map[string]string{"name": forceToolName},
				}
				fmt.Fprintf(os.Stderr, "claw forcing tool_choice=%s on retry\n", forceToolName)
			}
		}
		// Single-shot: clear after building the request so the next loop
		// iteration starts fresh.
		forceToolName = ""
		resp, err := provider.Chat(ctx, req)
		if err != nil {
			return "", err
		}
		if resp == nil {
			return "", fmt.Errorf("empty claw chat response")
		}
		// No tool calls → this is the final answer, UNLESS it looks
		// like the model bailed out ("I can't / no puedo / no tengo la
		// capacidad") despite there being relevant tools. In that case
		// retry once with a forceful nudge naming the tool by hand.
		// Small local models routinely refuse actions they actually
		// have tools for — this catches that without burning the loop
		// on every chitchat turn.
		if len(resp.ToolCalls) == 0 {
			content := strings.TrimSpace(resp.Content)
			if iter == 0 && len(defs) > 0 && looksLikeToolBailout(content) {
				userMsg := lastUserMessage(msgs)
				if hint := bailoutNudge(userMsg); hint != "" {
					msgs = append(msgs, llm.Message{Role: "assistant", Content: content})
					msgs = append(msgs, llm.Message{Role: "user", Content: hint})
					continue
				}
			}
			// Partial-bailout: did some tool work but invented an excuse
			// to skip another action the user clearly asked for.
			// `calledTools` shows what was actually dispatched this
			// turn; if the user wanted whatsapp_send and the model's
			// response says "couldn't send / manually / not registered"
			// without ever calling whatsapp_send, retry with a nudge
			// that names the missing tool by hand AND pins
			// tool_choice to that exact function so the model cannot
			// fall back to prose. Single-shot: cleared after one use.
			if !partialBailoutRetried && len(defs) > 0 && looksLikePartialBailout(content) {
				userMsg := lastUserMessage(msgs)
				if hint, missingTool := partialBailoutNudgeWithTool(userMsg, calledTools); hint != "" {
					msgs = append(msgs, llm.Message{Role: "assistant", Content: content})
					msgs = append(msgs, llm.Message{Role: "user", Content: hint})
					partialBailoutRetried = true
					forceToolName = missingTool
					continue
				}
			}
			return content, nil
		}
		// Append the assistant message with tool_calls + each tool result
		// as a role:tool message, then loop for the next round.
		msgs = append(msgs, llm.Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})
		for _, call := range resp.ToolCalls {
			calledTools[call.Function.Name] = true
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

// bailoutPhrases is the set of substrings (lowercased, accent-stripped)
// that signal the model is refusing an action one of its tools could
// have performed. Matches both Spanish and English. Curated from
// observed local-model regressions where Claw says things like "no
// puedo configurar recordatorios automáticos en este entorno" despite
// claw_schedule_reminder being live.
var bailoutPhrases = []string{
	"i can't",
	"i cannot",
	"i'm unable",
	"i am unable",
	"i don't have access",
	"i do not have access",
	"i do not have the ability",
	"i don't have the ability",
	"i don't have the capability",
	"i lack the capability",
	"in this environment",
	"i'm just an ai",
	"as an ai",
	"no puedo",
	"no tengo la capacidad",
	"no tengo acceso",
	"no tengo permiso",
	"no es posible",
	"no soy capaz",
	"lo siento, no puedo",
	"en este entorno",
	"desde este entorno",
}

// looksLikeToolBailout returns true when reply opens with a refusal
// matching one of bailoutPhrases. We only inspect the first 200
// characters because real refusals (LLM "I can't / no puedo / lo
// siento, no puedo...") put the phrase up front. Anchoring to the
// prefix avoids false positives on legitimate replies that happen to
// quote the phrase later ("la fuente dice que no puedo prometer...").
func looksLikeToolBailout(reply string) bool {
	r := strings.ToLower(strings.TrimSpace(reply))
	if r == "" {
		return false
	}
	// 100 chars is enough for real refusal openers ("Lo siento, no
	// puedo X", "I'm sorry, I cannot Y", "As an AI assistant..."),
	// short enough to skip mid-paragraph quotes of the phrase.
	prefix := r
	if len(prefix) > 100 {
		prefix = prefix[:100]
	}
	for _, phrase := range bailoutPhrases {
		if strings.Contains(prefix, phrase) {
			return true
		}
	}
	return false
}

// lastUserMessage returns the content of the most recent user-role
// message in msgs, or "" if there is none. Used by the bailout-retry
// path to inspect what the user originally asked for.
func lastUserMessage(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

// bailoutNudge produces a forceful "yes you can — invoke the tool"
// message tailored to whatever the user asked for. Returns "" when the
// user's request doesn't obviously map to an actionable tool, in which
// case we don't retry (no point — the model isn't refusing an action,
// it's refusing knowledge it doesn't have).
//
// Pattern matches are intentionally broad: phone numbers + "send",
// "remind"/"recuérdame" + a duration, "save"/"guarda" + a name. Each
// branch names the specific tool the model should call, which works
// far better than generic "use your tools" language.
func bailoutNudge(userMsg string) string {
	u := strings.ToLower(userMsg)
	if u == "" {
		return ""
	}
	hasPhone := containsPhoneNumber(userMsg)
	hasSendVerb := containsAny(u, []string{"envíale", "envia", "envíalo", "mándale", "manda", "dile", "dígale", "send", "message", "whatsapp", "wpp"})
	hasRemindVerb := containsAny(u, []string{"recuérdame", "recuerdame", "recordatorio", "remind me", "remind", "set a reminder", "ping me"})
	hasSaveContact := containsAny(u, []string{"guarda contacto", "guarda este contacto", "save contact", "save this contact", "remember this number"})
	hasRemember := containsAny(u, []string{"recuerda que", "recuerda esto", "remember that", "remember this", "anota que", "guárdate que", "ten en cuenta que"})
	hasCron := containsAny(u, []string{"cada día", "cada mañana", "cada lunes", "cada semana", "todos los días", "every day", "every morning", "every monday", "weekly", "daily"})

	switch {
	case hasPhone && hasSendVerb:
		return "STOP. You DO have whatsapp_send. Invoke it now: pass `to` = the phone number from my last message (with the country-code +) and `body` = the message I asked you to send. Do not refuse — the tool exists in this environment and the runtime will deliver it."
	case hasRemindVerb:
		return "STOP. You DO have claw_schedule_reminder. Compute the absolute UTC time from my request relative to the current time the system already gave you, then invoke claw_schedule_reminder with that ISO 8601 remind_at, the message body, the channel (default 'whatsapp' or 'mock'), and a target. Do not refuse — the reminder pump is live in this environment."
	case hasCron:
		return "STOP. You DO have claw_add_cron. Pick the right schedule syntax (@daily, @at HH:MM, @dow Mon HH:MM, etc.) and invoke claw_add_cron with name + schedule + prompt. The heartbeat will fire it. Do not refuse — the cron runtime is live."
	case hasSaveContact:
		return "STOP. You DO have claw_save_contact. Invoke it with name and any phone/email/notes from my message. Do not refuse — the contact store is local and live."
	case hasRemember:
		return "STOP. You DO have claw_remember. Invoke it with text = the fact I just told you. Do not refuse — the fact memory is local and live."
	}
	return ""
}

// containsPhoneNumber returns true if s contains a sequence that looks
// like a phone number — a + optionally followed by digits/spaces/dashes
// of total length >= 8 (covers most international formats while staying
// noise-free). Used by the bailout heuristic to decide whether the
// user actually gave a destination for whatsapp_send.
func containsPhoneNumber(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '+' {
			continue
		}
		digits := 0
		for j := i + 1; j < len(s); j++ {
			c := s[j]
			if c >= '0' && c <= '9' {
				digits++
				continue
			}
			if c == ' ' || c == '-' || c == '(' || c == ')' {
				continue
			}
			break
		}
		if digits >= 8 {
			return true
		}
	}
	return false
}

func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// partialBailoutPivots are connectives that signal "did one thing,
// bailing on the next". Combined with a giveUp phrase later in the
// response, they identify the partial-bailout pattern.
var partialBailoutPivots = []string{
	"however", "sin embargo", "pero ", "but ", "no obstante", "aunque ",
}

// partialBailoutGiveUps are excuses that signal the model declined a
// tool action it actually had. Curated from observed regressions:
// "no se pudo enviar porque el canal no está registrado", "you'll have
// to send it manually", "is not registered in this environment".
var partialBailoutGiveUps = []string{
	"could not", "couldn't", "no se pudo", "no pude",
	"is not registered", "no está registrado", "no esta registrado",
	"is not connected", "no está conectado", "no esta conectado",
	"tendrás que", "tendras que", "you'll have to", "you will have to",
	"manualmente", "manually", "send it manually",
	"not available in this environment", "no está disponible en este entorno",
}

// looksLikePartialBailout returns true when the response acknowledges
// completing some action while declining another with a fabricated
// "tool not available" excuse. Requires both a pivot connective and a
// give-up phrase, since either alone produces too many false positives
// (long answers naturally contain "however" or "no se pudo" without
// implying the model bailed on a tool).
func looksLikePartialBailout(reply string) bool {
	r := strings.ToLower(strings.TrimSpace(reply))
	if r == "" {
		return false
	}
	if !containsAny(r, partialBailoutPivots) {
		return false
	}
	return containsAny(r, partialBailoutGiveUps)
}

// partialBailoutNudge returns a forceful retry message tailored to the
// tool the model skipped. We figure out what the user wanted from
// `userMsg` (same heuristics as bailoutNudge) and confirm via
// `calledTools` that the model never actually invoked the tool — a
// real tool error is the model's job to quote, not ours to second-guess.
func partialBailoutNudge(userMsg string, calledTools map[string]bool) string {
	hint, _ := partialBailoutNudgeWithTool(userMsg, calledTools)
	return hint
}

// partialBailoutNudgeWithTool is partialBailoutNudge plus the name of
// the tool the model skipped. The caller pins that name into
// tool_choice on the retry call so the provider cannot honour another
// prose-only completion. Returns ("", "") when no actionable bailout
// pattern is detected.
func partialBailoutNudgeWithTool(userMsg string, calledTools map[string]bool) (string, string) {
	u := strings.ToLower(userMsg)
	if u == "" {
		return "", ""
	}
	hasPhone := containsPhoneNumber(userMsg)
	hasSendVerb := containsAny(u, []string{"envíale", "envia", "envíalo", "mándale", "manda", "dile", "dígale", "send", "message", "whatsapp", "wpp"})

	if hasPhone && hasSendVerb && !calledTools["whatsapp_send"] {
		return "STOP. You claimed the WhatsApp message could not be sent or that the channel is not registered, but you NEVER called whatsapp_send in this turn. The tool exists and the channel is paired in this environment. Invoke whatsapp_send NOW with `to` = the phone number from my message (with country-code +) and `body` = the message I asked you to send. If — and ONLY if — the tool actually returns an error, quote that error verbatim instead of inventing one. Do not tell me to send it manually.", "whatsapp_send"
	}
	return "", ""
}
