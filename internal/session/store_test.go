package session

import (
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
