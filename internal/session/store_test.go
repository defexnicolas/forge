package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge/internal/agent"
	"forge/internal/tools"
)

func TestStoreLogsAndTailsEvents(t *testing.T) {
	cwd := t.TempDir()
	store, err := New(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LogUser("hello"); err != nil {
		t.Fatal(err)
	}
	if err := store.LogCommand("/test", "ok"); err != nil {
		t.Fatal(err)
	}
	events, err := store.Tail(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != "user" || events[0].Text != "hello" {
		t.Fatalf("unexpected first event %#v", events[0])
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), "meta.json")); err != nil {
		t.Fatal(err)
	}
}

func TestListAndOpenLatest(t *testing.T) {
	cwd := t.TempDir()
	first, err := New(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.LogUser("first"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	second, err := New(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.LogUser("second"); err != nil {
		t.Fatal(err)
	}

	sessions, err := List(cwd, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	latest, err := OpenLatest(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID() != second.ID() {
		t.Fatalf("expected latest %s, got %s", second.ID(), latest.ID())
	}
	reopened, err := Open(cwd, first.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reopened.ContextText(4), "first") {
		t.Fatalf("expected reopened context text, got %q", reopened.ContextText(4))
	}
	if !strings.Contains(reopened.ContextText(4), "Session summary:") {
		t.Fatalf("expected summary in context text, got %q", reopened.ContextText(4))
	}
}

func TestStoreWritesLiveLog(t *testing.T) {
	cwd := t.TempDir()
	store, err := New(cwd)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.LogUser("debug this"); err != nil {
		t.Fatal(err)
	}
	if err := store.LogCommand("/test", "\x1b[31mok\x1b[0m"); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{
		Type:     agent.EventToolResult,
		ToolName: "run_command",
		Text:     "go test ./...",
		Result: &tools.Result{
			Summary: "go test ./...",
			Content: []tools.ContentBlock{
				{Type: "text", Text: "package output\nFAIL example"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendChatTurn("final answer"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(store.LiveLogPath())
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	for _, want := range []string{"## You", "debug this", "## Command /test", "ok", "## Tool result run_command", "package output", "FAIL example", "## Forge", "final answer"} {
		if !strings.Contains(log, want) {
			t.Fatalf("live log missing %q in:\n%s", want, log)
		}
	}
	if strings.Contains(log, "\x1b[") {
		t.Fatalf("live log contains ANSI escapes:\n%q", log)
	}
}

func TestContextTextCompactsPlanningArtifacts(t *testing.T) {
	cwd := t.TempDir()
	store, err := New(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LogUser("start planning"); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{
		Type:     agent.EventToolCall,
		ToolName: "todo_write",
		Input:    []byte(`{"items":["Create stale file","Update stale imports"]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{
		Type:     agent.EventToolResult,
		ToolName: "todo_write",
		Result: &tools.Result{
			Summary: "Updated checklist:\n  [ ] Create stale file\n  [ ] Update stale imports",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{
		Type:     agent.EventToolResult,
		ToolName: "run_command",
		Result:   &tools.Result{Summary: "go test ./... passed"},
	}); err != nil {
		t.Fatal(err)
	}

	text := store.ContextText(10)
	if strings.Contains(text, "Create stale file") || strings.Contains(text, "Update stale imports") {
		t.Fatalf("planning payload leaked into context:\n%s", text)
	}
	if !strings.Contains(text, "planning artifact updated") {
		t.Fatalf("expected compact planning marker, got:\n%s", text)
	}
	if !strings.Contains(text, "go test ./... passed") {
		t.Fatalf("expected non-planning event preserved, got:\n%s", text)
	}
}

// TestContextDropsAbortedTurns pins the core promise of Fix 3 + Fix 1:
// when a turn ends with TurnOutcome=aborted (narration loop, parse retry
// exhausted, etc.) NO event from that turn is rendered to the next
// prompt. The model should not see its own cancelled monologue and start
// apologizing about being stuck.
func TestContextDropsAbortedTurns(t *testing.T) {
	cwd := t.TempDir()
	store, err := New(cwd)
	if err != nil {
		t.Fatal(err)
	}

	// Turn 1: aborted. Has user input + a cancelled assistant reply.
	turn1 := "t-1"
	if err := store.LogAgentEvent(agent.Event{Type: agent.EventTurnStart, TurnID: turn1, Text: "investigá la combat log"}); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{Type: agent.EventAssistantText, TurnID: turn1, Text: "I keep saying the same thing over and over"}); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{Type: agent.EventError, TurnID: turn1, Error: errors.New(`narration loop detected (line "let me think" repeated 3 times); cancelled stream`)}); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{Type: agent.EventTurnEnd, TurnID: turn1}); err != nil {
		t.Fatal(err)
	}
	// Turn 2: completed cleanly. Has a real assistant answer.
	turn2 := "t-2"
	if err := store.LogAgentEvent(agent.Event{Type: agent.EventTurnStart, TurnID: turn2, Text: "ok now answer me"}); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{Type: agent.EventAssistantText, TurnID: turn2, Text: "Combat log lives in src/Game.tsx:142"}); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{Type: agent.EventTurnEnd, TurnID: turn2}); err != nil {
		t.Fatal(err)
	}

	text := store.ContextText(20)
	if strings.Contains(text, "I keep saying the same thing") {
		t.Errorf("aborted-turn assistant text leaked into context:\n%s", text)
	}
	if strings.Contains(text, "narration loop detected") {
		t.Errorf("aborted-turn error leaked into context:\n%s", text)
	}
	if !strings.Contains(text, "Combat log lives in src/Game.tsx") {
		t.Errorf("completed-turn answer was filtered (should be preserved):\n%s", text)
	}
}

// TestContextCarriesForwardBudgetAbortedTurns pins the carry-forward
// behavior: when a debug turn aborts because the read budget or thinking
// budget was exhausted, the next prompt sees a synthesized summary of
// what was already read so it doesn't restart cold and re-explore the
// same files. Narration-loop / parse-retry aborts still get filtered
// cleanly (covered by TestContextDropsAbortedTurns).
func TestContextCarriesForwardBudgetAbortedTurns(t *testing.T) {
	cwd := t.TempDir()
	store, err := New(cwd)
	if err != nil {
		t.Fatal(err)
	}

	turnID := "t-budget"
	if err := store.LogAgentEvent(agent.Event{Type: agent.EventTurnStart, TurnID: turnID}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"internal/agent/runtime.go", "internal/agent/modes.go", "internal/session/store.go"} {
		if err := store.LogAgentEvent(agent.Event{
			Type:     agent.EventToolCall,
			ToolName: "read_file",
			TurnID:   turnID,
			Input:    fmt.Appendf(nil, `{"path":%q}`, path),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.LogAgentEvent(agent.Event{
		Type:     agent.EventToolCall,
		ToolName: "search_text",
		TurnID:   turnID,
		Input:    []byte(`{"query":"NewDebugPolicy"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{
		Type:    agent.EventAssistantText,
		TurnID:  turnID,
		Text:    "I think the bug is in how the policy decides ToolAsk vs ToolAllow",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{
		Type:   agent.EventError,
		TurnID: turnID,
		Error:  errors.New("stopped: 28 consecutive read-only tool calls — switch to instrumentation or escalate"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{Type: agent.EventTurnEnd, TurnID: turnID, TurnOutcome: OutcomeAborted}); err != nil {
		t.Fatal(err)
	}

	text := store.ContextText(20)

	// Carry-forward block must appear.
	if !strings.Contains(text, "PRIOR DEBUG ATTEMPT") {
		t.Fatalf("expected carry-forward header in context, got:\n%s", text)
	}
	for _, want := range []string{
		"internal/agent/runtime.go",
		"internal/agent/modes.go",
		"internal/session/store.go",
		"NewDebugPolicy",
		"I think the bug is in how the policy",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("carry-forward missing %q in:\n%s", want, text)
		}
	}
	// The original tool_call records and assistant_text must NOT leak in
	// addition — only the synthesized summary survives.
	if strings.Count(text, "internal/agent/runtime.go") > 1 {
		t.Errorf("file path appears more than once — original tool_call leaked alongside carry-forward:\n%s", text)
	}
}

// TestContextCarriesForwardReasoningTailOnThinkingBudgetAbort verifies
// that when the thinking-budget guard cancels mid-stream BEFORE any
// tool_call, the captured reasoning tail (emitted as
// EventReasoningTail) survives into the carry-forward block. Without
// this, an early thinking-budget abort would leave the next turn no
// breadcrumbs at all and the model would re-derive the same chain.
func TestContextCarriesForwardReasoningTailOnThinkingBudgetAbort(t *testing.T) {
	cwd := t.TempDir()
	store, err := New(cwd)
	if err != nil {
		t.Fatal(err)
	}

	turnID := "t-think"
	if err := store.LogAgentEvent(agent.Event{Type: agent.EventTurnStart, TurnID: turnID}); err != nil {
		t.Fatal(err)
	}
	// No tool_calls happened — model only reasoned. Capture the tail.
	if err := store.LogAgentEvent(agent.Event{
		Type:      agent.EventReasoningTail,
		TurnID:    turnID,
		Text:      "The bug seems to be in how the snake position updates after eating food. Let me read Game.tsx to confirm.",
		Transient: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{
		Type:      agent.EventError,
		TurnID:    turnID,
		Error:     errors.New("thinking budget exhausted: model emitted 14000 chars of reasoning without taking an action"),
		Transient: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{Type: agent.EventTurnEnd, TurnID: turnID, TurnOutcome: OutcomeAborted}); err != nil {
		t.Fatal(err)
	}

	text := store.ContextText(20)
	if !strings.Contains(text, "PRIOR DEBUG ATTEMPT") {
		t.Fatalf("expected carry-forward header, got:\n%s", text)
	}
	if !strings.Contains(text, "snake position updates after eating food") {
		t.Errorf("reasoning tail did not survive into carry-forward, got:\n%s", text)
	}
	if !strings.Contains(text, "Reasoning chunk before cancel") {
		t.Errorf("expected explicit reasoning-chunk label in carry-forward, got:\n%s", text)
	}
}

// TestContextDoesNotCarryForwardNarrationLoopAborts confirms that
// non-budget aborts (narration loops, parse retries, max-step caps) are
// still filtered cleanly without a carry-forward block — that pollution
// is what TestContextDropsAbortedTurns guards against, and the new
// carry-forward path must NOT regress it.
func TestContextDoesNotCarryForwardNarrationLoopAborts(t *testing.T) {
	cwd := t.TempDir()
	store, err := New(cwd)
	if err != nil {
		t.Fatal(err)
	}

	turnID := "t-narration"
	if err := store.LogAgentEvent(agent.Event{Type: agent.EventTurnStart, TurnID: turnID}); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{
		Type:     agent.EventToolCall,
		ToolName: "read_file",
		TurnID:   turnID,
		Input:    []byte(`{"path":"foo.go"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{
		Type:   agent.EventError,
		TurnID: turnID,
		Error:  errors.New(`narration loop detected (line "let me think" repeated 3 times)`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.LogAgentEvent(agent.Event{Type: agent.EventTurnEnd, TurnID: turnID, TurnOutcome: OutcomeAborted}); err != nil {
		t.Fatal(err)
	}

	text := store.ContextText(20)
	if strings.Contains(text, "PRIOR DEBUG ATTEMPT") {
		t.Errorf("narration-loop abort should NOT produce a carry-forward block, got:\n%s", text)
	}
	if strings.Contains(text, "foo.go") {
		t.Errorf("narration-loop abort leaked file path into context:\n%s", text)
	}
}

// TestClassifyTurnOutcomeRecognizesPatterns pins the heuristics that
// decide whether a turn was aborted vs failed vs completed. If any of
// these patterns drift in the runtime's error messages, this test fails
// loudly so we update the classifier in lockstep.
func TestClassifyTurnOutcomeRecognizesPatterns(t *testing.T) {
	cases := []struct {
		name string
		err  string
		want string
	}{
		{"narration loop", `narration loop detected (line "x" repeated 3 times)`, OutcomeAborted},
		{"parse exhausted", `parse error (attempt 3/3) [parser=qwen model=gpt-oss-20b]: invalid JSON`, OutcomeAborted},
		{"max steps", `agent stopped after 40 steps in build mode`, OutcomeAborted},
		{"soft-nudge stop", `stopped: 15 consecutive read-only tool calls — you ignored the soft nudge`, OutcomeAborted},
		{"build prose stuck", `stopped: 3 build response(s) in prose with checklist tasks still active`, OutcomeAborted},
		{"plan no progress", `stopped: 2 planner step(s) with no actionable progress`, OutcomeAborted},
		{"tool failed", `tool edit_file failed 3 times in a row — stopping to avoid infinite loop`, OutcomeFailed},
		{"policy denial", `denied by command policy: rm -rf /`, OutcomeFailed},
		{"task already failed", `task plan-3 already failed in this turn; refusing repeated execute_task retry`, OutcomeFailed},
		{"unknown error", `something weird happened`, OutcomeCompleted},
		{"no error", "", OutcomeCompleted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := []agent.Event{}
			if tc.err != "" {
				buf = append(buf, agent.Event{Type: agent.EventError, Error: errors.New(tc.err)})
			}
			got := classifyTurnOutcome(buf)
			if got != tc.want {
				t.Errorf("classifyTurnOutcome(%q) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestAutoCompactSkipsBelowThreshold confirms the compaction is a no-op
// when neither token usage nor event count crosses the trigger.
func TestAutoCompactSkipsBelowThreshold(t *testing.T) {
	cwd := t.TempDir()
	store, err := New(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LogUser("first"); err != nil {
		t.Fatal(err)
	}
	called := false
	runner := func(ctx context.Context, req agent.SubagentRequest) (tools.Result, error) {
		called = true
		return tools.Result{}, nil
	}
	err = store.AutoCompact(context.Background(), agent.AutoCompactOptions{
		Runner:       runner,
		TokensUsed:   100,
		TokensBudget: 10000, // 1% — way under any reasonable threshold
		Threshold:    0.7,
		MaxEvents:    100,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Error("summarizer should not have been invoked when below threshold")
	}
}

// TestAutoCompactInvokesSummarizerAndRecordsMarker drives the happy path:
// threshold crossed → summarizer called → marker appended → next
// ContextText renders the summary in place of the absorbed events.
func TestAutoCompactInvokesSummarizerAndRecordsMarker(t *testing.T) {
	cwd := t.TempDir()
	store, err := New(cwd)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := store.LogUser(fmt.Sprintf("turn %d input", i)); err != nil {
			t.Fatal(err)
		}
	}
	gotPrompt := ""
	runner := func(ctx context.Context, req agent.SubagentRequest) (tools.Result, error) {
		gotPrompt = req.Prompt
		return tools.Result{
			Title:   "summarizer",
			Summary: "User explored the combat log; touched src/Game.tsx.",
		}, nil
	}
	err = store.AutoCompact(context.Background(), agent.AutoCompactOptions{
		Runner:       runner,
		TokensUsed:   8000,
		TokensBudget: 10000,
		Threshold:    0.7,
		MaxEvents:    1000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(gotPrompt, "TRANSCRIPT") {
		t.Errorf("summarizer prompt missing transcript section, got: %s", gotPrompt)
	}
	// The next context render must include the summary, not the
	// individual user events that were absorbed.
	text := store.ContextText(20)
	if !strings.Contains(text, "combat log") {
		t.Errorf("ContextText should render the summary, got:\n%s", text)
	}
}
