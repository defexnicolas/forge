package tui

import (
	"strings"
	"testing"

	"forge/internal/agent"
)

// TestStreamFlushCoalescesDeltas feeds many EventAssistantDelta events into the
// model's streaming builder path and verifies the materialized line matches
// the concatenation of deltas with proper indenting. This is the regression
// test for the O(n²) `m.history[last] += indented` concat that was replaced
// by strings.Builder — the builder must produce an identical final string.
func TestStreamFlushCoalescesDeltas(t *testing.T) {
	m := newSizedLayoutModel(t, 96, 24)
	deltas := []string{"Hello", " ", "Forge", "!\n", "second line", " continues"}
	for _, d := range deltas {
		m.appendAgentEvent(agent.Event{Type: agent.EventAssistantDelta, Text: d})
	}
	if !m.streaming {
		t.Fatal("expected streaming=true after deltas")
	}
	if m.streamingStartIdx < 0 || m.streamingStartIdx >= len(m.history) {
		t.Fatalf("streamingStartIdx out of range: %d (history len %d)", m.streamingStartIdx, len(m.history))
	}
	// Before flush, the placeholder line should still be empty — the builder
	// holds the real content.
	if m.history[m.streamingStartIdx] != "" {
		t.Fatalf("history[streamingStartIdx] = %q, want empty before flush", m.history[m.streamingStartIdx])
	}
	m.flushStreaming()
	got := m.history[m.streamingStartIdx]
	// The expected indented form: four-space prefix, with every newline
	// followed by a four-space continuation indent.
	raw := strings.Join(deltas, "")
	want := "    " + strings.ReplaceAll(raw, "\n", "\n    ")
	if got != want {
		t.Fatalf("flushed line mismatch\n got: %q\nwant: %q", got, want)
	}
	if lastAgentResponse != raw {
		t.Fatalf("lastAgentResponse = %q, want %q", lastAgentResponse, raw)
	}
}

// TestClearStreamingDropsPendingBuilder verifies EventClearStreaming wipes the
// accumulated builder without materializing it. This preserves the contract:
// when a <tool_call> tag is detected mid-stream the runtime sends
// EventClearStreaming so the partial streamed text — which contains the raw
// tag — does not leak into the viewport.
func TestClearStreamingDropsPendingBuilder(t *testing.T) {
	m := newSizedLayoutModel(t, 96, 24)
	m.appendAgentEvent(agent.Event{Type: agent.EventAssistantDelta, Text: "I'll use <tool_"})
	m.appendAgentEvent(agent.Event{Type: agent.EventAssistantDelta, Text: "call>{..."})
	historyLenBefore := len(m.history)
	startIdx := m.streamingStartIdx
	m.appendAgentEvent(agent.Event{Type: agent.EventClearStreaming})
	if m.streaming {
		t.Fatal("expected streaming=false after EventClearStreaming")
	}
	if m.streamingStartIdx != -1 {
		t.Fatalf("streamingStartIdx = %d, want -1", m.streamingStartIdx)
	}
	if m.streamingBuilder.Len() != 0 {
		t.Fatalf("streamingBuilder should be empty, got %d bytes", m.streamingBuilder.Len())
	}
	if m.streamingRaw.Len() != 0 {
		t.Fatalf("streamingRaw should be empty, got %d bytes", m.streamingRaw.Len())
	}
	if lastAgentResponse != "" {
		t.Fatalf("lastAgentResponse = %q, want empty", lastAgentResponse)
	}
	if len(m.history) != historyLenBefore-1 {
		t.Fatalf("history not trimmed back: was %d, now %d (startIdx=%d)", historyLenBefore, len(m.history), startIdx)
	}
	// A fresh streaming turn must start cleanly — no leftover bytes from the
	// discarded block.
	m.appendAgentEvent(agent.Event{Type: agent.EventAssistantDelta, Text: "clean answer"})
	m.flushStreaming()
	got := m.history[m.streamingStartIdx]
	if got != "    clean answer" {
		t.Fatalf("fresh stream contaminated by discarded builder: %q", got)
	}
}

// TestRefreshStreamingFallsBackWhenSearching verifies that refreshStreaming
// uses the full refresh path (with filter) when search mode is active. The
// prefix cache is not valid under a filtered view.
func TestRefreshStreamingFallsBackWhenSearching(t *testing.T) {
	m := newSizedLayoutModel(t, 96, 24)
	m.history = append(m.history, "    foo line one", "    bar line two", "    foo line three")
	m.appendAgentEvent(agent.Event{Type: agent.EventAssistantDelta, Text: "streaming text"})
	m.flushStreaming()
	// Activate search for "foo" — refreshStreaming should delegate to refresh,
	// which populates search positions.
	m.searchMode = newSearchMode(m.theme)
	m.searchMode.query = "foo"
	m.refreshStreaming()
	if len(m.searchMode.positions) == 0 {
		t.Fatal("expected refresh() fallback to populate search positions for 'foo'")
	}
}

// TestPrefixCacheReusedAcrossFlushes verifies the cached prefix string is only
// rebuilt when marked dirty. On the second flush (no history mutation above
// streamingStartIdx) the cache must survive — that is the whole point of the
// optimization.
func TestPrefixCacheReusedAcrossFlushes(t *testing.T) {
	m := newSizedLayoutModel(t, 96, 24)
	m.history = append(m.history, "    preamble one", "    preamble two")
	m.appendAgentEvent(agent.Event{Type: agent.EventAssistantDelta, Text: "hello"})
	m.flushStreaming()
	m.refreshStreaming()
	if m.prefixDirty {
		t.Fatal("prefixDirty should be false after refreshStreaming built the cache")
	}
	cached := m.prefixRendered
	if cached == "" {
		t.Fatal("expected non-empty prefixRendered after first refreshStreaming")
	}
	// A second delta + flush should NOT invalidate the prefix — nothing above
	// streamingStartIdx changed.
	m.appendAgentEvent(agent.Event{Type: agent.EventAssistantDelta, Text: " world"})
	m.flushStreaming()
	m.refreshStreaming()
	if m.prefixRendered != cached {
		t.Fatal("prefixRendered should be reused across flushes within a turn")
	}
}
