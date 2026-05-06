package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"forge/internal/llm"
)

// User intent categories returned by classifyUserIntent. The classifier is
// deliberately coarse — these labels exist to drive routing suggestions
// (e.g. "this looks like a bug hunt; recommend /mode debug"), not to
// dispatch work. Adding more categories without a downstream consumer
// just bloats the prompt.
const (
	IntentBugHunt  = "bug_hunt"
	IntentFeature  = "feature"
	IntentQuestion = "question"
	IntentOther    = "other"
)

// classifyUserIntent runs a small LLM call against the chat-tier model to
// label the user's request. Result is cached by sha256(message) so a turn
// that re-fires (parse retry, etc.) doesn't re-classify; cache lives on
// the runtime for the session.
//
// On any failure (provider error, timeout, unparseable response) it falls
// back to a tiny string-pattern heuristic so the routing hint still
// appears for the obvious cases. Failure is non-fatal — callers always
// get a category back, possibly IntentOther if nothing matched.
//
// Timeout: 10 seconds. Longer than that, the answer isn't worth waiting
// for — the cost of holding up the main turn beats the benefit of a
// routing hint.
func (r *Runtime) classifyUserIntent(ctx context.Context, userMessage string) string {
	msg := strings.TrimSpace(userMessage)
	if msg == "" {
		return IntentOther
	}
	key := intentCacheKey(msg)
	r.mu.Lock()
	if r.intentCache == nil {
		r.intentCache = map[string]string{}
	}
	if cached, ok := r.intentCache[key]; ok {
		r.mu.Unlock()
		return cached
	}
	r.mu.Unlock()

	classified := r.classifyViaLLM(ctx, msg)
	if classified == "" {
		classified = classifyViaHeuristic(msg)
	}
	r.mu.Lock()
	r.intentCache[key] = classified
	r.mu.Unlock()
	return classified
}

// classifyViaLLM is the primary classifier path. Returns "" on any
// failure so the caller can fall through to the heuristic. Uses the
// "chat" role model (typically the smallest / fastest tier) so we don't
// hold up the main turn waiting for a heavyweight reasoning model.
func (r *Runtime) classifyViaLLM(ctx context.Context, userMessage string) string {
	if r == nil || r.Providers == nil {
		return ""
	}
	providerName := r.Config.Providers.Default.Name
	provider, ok := r.Providers.Get(providerName)
	if !ok {
		return ""
	}
	model := r.roleModel("chat")
	if model == "" {
		model = r.Config.Models["chat"]
	}
	if model == "" {
		return ""
	}

	classifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	system := "You are a one-word classifier. Categorize the user's message into EXACTLY ONE label:\n" +
		"- bug_hunt: user reports unexpected behavior they want diagnosed (something fails/crashes/freezes/teleports/doesn't work)\n" +
		"- feature: user wants new functionality built\n" +
		"- question: user asks how something works (no action requested)\n" +
		"- other: anything else\n" +
		"Respond with ONLY the label, no punctuation, no explanation."

	temp := float64(0.0)
	req := llm.ChatRequest{
		Model:       model,
		Temperature: &temp,
		Messages: []llm.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: userMessage},
		},
	}
	r.applySamplingDefaults(&req)

	resp, err := provider.Chat(classifyCtx, req)
	if err != nil {
		return ""
	}
	out := strings.ToLower(strings.TrimSpace(resp.Content))
	// Strip common framings the model adds despite the instruction. The
	// match is tight: only the leading word is read, so a verbose
	// response like "bug_hunt — the snake teleports" still classifies
	// correctly.
	out = strings.TrimPrefix(out, "category:")
	out = strings.TrimPrefix(out, "label:")
	out = strings.TrimSpace(out)
	if i := strings.IndexAny(out, " \n\t.,—-"); i > 0 {
		out = out[:i]
	}
	switch out {
	case IntentBugHunt, IntentFeature, IntentQuestion, IntentOther:
		return out
	}
	return ""
}

// classifyViaHeuristic is the safety net when the LLM call fails. Tiny
// pattern list covering the obvious bug-hunt phrases in English and
// Spanish — the languages this codebase's user actually types. Anything
// outside the list returns IntentOther rather than guessing.
func classifyViaHeuristic(userMessage string) string {
	low := strings.ToLower(userMessage)
	bugSignals := []string{
		// English
		"doesn't work", "does not work", "doesn't run", "doesn't load",
		"fails", "failing", "failed",
		"crashes", "crashing", "crashed",
		"freezes", "frozen", "hangs", "hanging",
		"teleports", "broken", "bug",
		"throws", "exception", "error",
		"nothing happens", "unexpected",
		"why is", "why does",
		"can't figure out", "stuck on",
		// Spanish
		"no funciona", "no anda", "no corre", "no carga",
		"falla", "fallando", "fallido",
		"se rompe", "se cuelga", "se congela", "se queda",
		"se traba", "se cierra",
		"no entiendo por qué", "no entiendo por que",
		"debería", "debería",
	}
	for _, sig := range bugSignals {
		if strings.Contains(low, sig) {
			return IntentBugHunt
		}
	}
	return IntentOther
}

// intentCacheKey hashes the message so the in-memory cache stays small
// even when users paste large prompts. SHA256 hex truncated to 16 chars
// is plenty for cache uniqueness within a single session.
func intentCacheKey(message string) string {
	sum := sha256.Sum256([]byte(message))
	return hex.EncodeToString(sum[:8])
}

// classifyUserIntentAsync fires the classifier on a goroutine and
// returns a channel that will receive exactly one category string. The
// caller can race it against a short deadline — if the classifier
// hasn't returned by the time the main turn needs to send the prompt,
// drop the hint and proceed (the routing suggestion is nice-to-have,
// not critical path).
//
// The channel is closed after sending so receivers using a select with
// a default branch can detect "not ready yet" via the zero-value path.
func (r *Runtime) classifyUserIntentAsync(ctx context.Context, userMessage string) <-chan string {
	out := make(chan string, 1)
	go func() {
		defer close(out)
		out <- r.classifyUserIntent(ctx, userMessage)
	}()
	return out
}

// suggestModeForIntent returns the mode-routing suggestion text to inject
// into the user prompt when the user's intent doesn't match the current
// mode's strengths. Empty string means "no suggestion needed" — current
// mode is appropriate for the request.
func suggestModeForIntent(currentMode, intent string) string {
	if intent != IntentBugHunt {
		return ""
	}
	switch currentMode {
	case "debug":
		return "" // already in the right place
	case "explore":
		return "ROUTING HINT: This request looks like a bug hunt. Explore mode is read-only — it can't run the program or add console.log to observe runtime behavior. Recommend the user switch to /mode debug for hypothesis-driven debugging. If the user explicitly wants only static investigation, proceed with the regular explore workflow but call this out in your first response."
	case "plan":
		return "ROUTING HINT: This request looks like a bug hunt without a known fix. Plan mode designs solutions to known problems — for an undiagnosed bug, designing tasks before identifying the root cause typically produces vague tasks that build mode then can't execute. Recommend the user switch to /mode debug to find the root cause first; once identified, plan mode can design any structural follow-up if needed."
	case "build":
		return "ROUTING HINT: This request looks like a bug hunt and there's no checklist to execute. Build mode is the executor of an approved plan, not a debugger — without a known fix it ends up speculating and editing without verification. Recommend the user switch to /mode debug for hypothesis-driven investigation."
	default:
		return ""
	}
}

// classifyUserIntentJoinDeadline waits up to deadline for an async
// classification to land. If the deadline expires first, returns
// IntentOther — the classifier is non-blocking by design, callers should
// proceed with the turn even if the hint hasn't materialized.
func classifyUserIntentJoinDeadline(ch <-chan string, deadline time.Duration) string {
	select {
	case v, ok := <-ch:
		if !ok {
			return IntentOther
		}
		return v
	case <-time.After(deadline):
		return IntentOther
	}
}
