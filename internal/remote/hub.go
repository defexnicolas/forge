// Package remote serves the current forge session over HTTP so the user can
// view and drive it from another device on the LAN (phone, tablet).
package remote

import (
	"encoding/json"
	"sync"

	"forge/internal/agent"
)

// Hub fans out agent events to any number of subscribers. Safe for concurrent
// publish and subscribe.
type Hub struct {
	mu     sync.RWMutex
	subs   map[int]chan EventWire
	nextID int
}

// EventWire is the JSON-serializable form of agent.Event used over SSE.
type EventWire struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ToolName string          `json:"tool_name,omitempty"`
	Summary  string          `json:"summary,omitempty"`
	Error    string          `json:"error,omitempty"`
	Side     bool            `json:"side,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
}

func NewHub() *Hub {
	return &Hub{subs: map[int]chan EventWire{}}
}

// Publish implements agent.EventTee.
func (h *Hub) Publish(ev agent.Event) {
	wire := toWire(ev)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.subs {
		select {
		case ch <- wire:
		default:
			// Drop if subscriber is slow — they'll resync from /api/session.
		}
	}
}

// Subscribe returns a channel receiving future events, plus an unsubscribe fn.
func (h *Hub) Subscribe() (<-chan EventWire, func()) {
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	ch := make(chan EventWire, 64)
	h.subs[id] = ch
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if existing, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(existing)
		}
		h.mu.Unlock()
	}
}

func (h *Hub) SubscriberCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

func toWire(ev agent.Event) EventWire {
	w := EventWire{
		Type:     ev.Type,
		Text:     ev.Text,
		ToolName: ev.ToolName,
		Side:     ev.Side,
		Input:    ev.Input,
	}
	if ev.Error != nil {
		w.Error = ev.Error.Error()
	}
	if ev.Result != nil {
		w.Summary = ev.Result.Summary
	}
	if ev.AskUser != nil && w.Summary == "" {
		w.Summary = ev.AskUser.Question
	}
	if ev.Approval != nil && w.Summary == "" {
		w.Summary = ev.Approval.Summary
	}
	if ev.SubagentProgress != nil && w.Summary == "" {
		w.Summary = ev.SubagentProgress.Agent + " " + ev.SubagentProgress.Status
	}
	return w
}
