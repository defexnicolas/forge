package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// MemorySnapshot is a stable read-only view of Claw's recent memory the LLM
// can pull through the introspection tool. Mirrors a subset of claw.State.
type MemorySnapshot struct {
	Summaries []string         `json:"summaries"`
	Events    []MemoryEventRec `json:"events"`
	Facts     []FactRecord     `json:"facts"`
	Crons     int              `json:"crons"`
	Reminders int              `json:"reminders_pending"`
}

// MemoryEventRec is a flattened claw.MemoryEvent for the wire.
type MemoryEventRec struct {
	Kind      string `json:"kind"`
	Channel   string `json:"channel,omitempty"`
	Author    string `json:"author,omitempty"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}

// claw_recent_memory

type clawRecentMemoryTool struct {
	snapshot func(ctx context.Context, limitEvents, limitFacts int) MemorySnapshot
}

func (clawRecentMemoryTool) Name() string { return "claw_recent_memory" }
func (clawRecentMemoryTool) Description() string {
	return "Fetch a snapshot of Claw's recent memory: the last summaries, recent events (chat/inbound/cron), top facts, plus counts of active crons and pending reminders. Use this when you need to recall what Claw was last working on, or before answering 'what do you remember about X?' so your reply is grounded in stored memory rather than a guess."
}
func (clawRecentMemoryTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"events":{"type":"integer","description":"Max events to return (default 10, max 30)."},"facts":{"type":"integer","description":"Max facts to return (default 10, max 30)."}}}`)
}
func (clawRecentMemoryTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow, Reason: "Read-only view of Claw's local memory"}
}
func (t clawRecentMemoryTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	if t.snapshot == nil {
		return Result{Title: "claw_recent_memory", Summary: "memory not wired"}, errors.New("claw_recent_memory: no snapshot closure registered")
	}
	var req struct {
		Events int `json:"events"`
		Facts  int `json:"facts"`
	}
	_ = json.Unmarshal(input, &req)
	if req.Events <= 0 {
		req.Events = 10
	}
	if req.Events > 30 {
		req.Events = 30
	}
	if req.Facts <= 0 {
		req.Facts = 10
	}
	if req.Facts > 30 {
		req.Facts = 30
	}
	snap := t.snapshot(ctx.Context, req.Events, req.Facts)
	body, _ := json.Marshal(snap)
	summary := fmt.Sprintf("%d summary, %d event, %d fact, %d cron, %d reminder",
		len(snap.Summaries), len(snap.Events), len(snap.Facts), snap.Crons, snap.Reminders)
	return Result{
		Title:   "claw_recent_memory",
		Summary: summary,
		Content: []ContentBlock{{Type: "text", Text: string(body)}},
	}, nil
}

// claw_dream_now

type clawDreamNowTool struct {
	dream func(ctx context.Context, reason string) (string, error)
}

func (clawDreamNowTool) Name() string { return "claw_dream_now" }
func (clawDreamNowTool) Description() string {
	return "Trigger an immediate dream pass — Claw consolidates recent memory into a summary and may surface action suggestions. Use this when the user explicitly asks Claw to reflect, plan, or 'think about what we've done lately'. Otherwise dreaming runs automatically on the heartbeat schedule."
}
func (clawDreamNowTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"reason":{"type":"string","description":"Optional short label tagged onto the resulting summary (e.g. 'user asked')."}}}`)
}
func (clawDreamNowTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow, Reason: "Local memory consolidation — no external side effects"}
}
func (t clawDreamNowTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	if t.dream == nil {
		return Result{Title: "claw_dream_now", Summary: "dream not wired"}, errors.New("claw_dream_now: no dream closure registered")
	}
	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(input, &req)
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "tool_request"
	}
	summary, err := t.dream(ctx.Context, reason)
	if err != nil {
		return Result{
			Title:   "claw_dream_now",
			Summary: "dream failed: " + err.Error(),
			Content: []ContentBlock{{Type: "text", Text: err.Error()}},
		}, nil
	}
	return Result{
		Title:   "claw_dream_now",
		Summary: summary,
		Content: []ContentBlock{{Type: "text", Text: summary}},
	}, nil
}

// RegisterClawIntrospectionTools wires the memory snapshot + dream closures.
func RegisterClawIntrospectionTools(
	reg *Registry,
	snapshot func(ctx context.Context, limitEvents, limitFacts int) MemorySnapshot,
	dream func(ctx context.Context, reason string) (string, error),
) {
	reg.Register(clawRecentMemoryTool{snapshot: snapshot}, "ClawRecentMemory")
	reg.Register(clawDreamNowTool{dream: dream}, "ClawDreamNow")
}
