package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// FactRecord mirrors claw.Fact across the package boundary so we don't
// pull claw into tools (which would create an import cycle).
type FactRecord struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Subject   string `json:"subject,omitempty"`
	Source    string `json:"source,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// claw_remember

type clawRememberTool struct {
	remember func(ctx context.Context, text, subject string) (FactRecord, error)
}

func (clawRememberTool) Name() string { return "claw_remember" }
func (clawRememberTool) Description() string {
	return "Remember a fact for later. Use when the user shares preferences, allergies, recurring schedules, contact details that don't fit a Contact record, or anything they'd reasonably expect Claw to recall later. The 'subject' field is optional — pass it when the fact is about a specific person or topic so future recalls can filter."
}
func (clawRememberTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["text"],"properties":{"text":{"type":"string","description":"The fact in free-form prose. Example: 'Nicolás is allergic to peanuts.'"},"subject":{"type":"string","description":"Optional tag: who or what this fact is about (e.g. 'user', 'Sebastián', 'work')."}}}`)
}
func (clawRememberTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow, Reason: "Stores a fact in Claw's local memory — no external side effects"}
}
func (t clawRememberTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	if t.remember == nil {
		return Result{Title: "claw_remember", Summary: "fact store not wired"}, errors.New("claw_remember: no remember closure registered")
	}
	var req struct {
		Text    string `json:"text"`
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return Result{Title: "claw_remember", Summary: "text is required"}, errors.New("text is required")
	}
	rec, err := t.remember(ctx.Context, text, req.Subject)
	if err != nil {
		return Result{
			Title:   "claw_remember",
			Summary: "remember failed: " + err.Error(),
			Content: []ContentBlock{{Type: "text", Text: err.Error()}},
		}, nil
	}
	summary := "remembered: " + rec.Text
	return Result{
		Title:   "claw_remember",
		Summary: summary,
		Content: []ContentBlock{{Type: "text", Text: summary}},
	}, nil
}

// claw_recall

type clawRecallTool struct {
	recall func(ctx context.Context, query string, maxResults int) []FactRecord
}

func (clawRecallTool) Name() string { return "claw_recall" }
func (clawRecallTool) Description() string {
	return "Search Claw's stored facts. Pass a query (a word or phrase) and Claw returns matching facts ranked newest-first. Empty query returns the most recent facts overall. Use this whenever the user asks about something they previously told you."
}
func (clawRecallTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Word or phrase to search for. Empty string returns most recent facts."},"max_results":{"type":"integer","description":"Maximum number of facts to return. Defaults to 10."}}}`)
}
func (clawRecallTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow, Reason: "Read-only search over Claw's local fact store"}
}
func (t clawRecallTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	if t.recall == nil {
		return Result{Title: "claw_recall", Summary: "fact store not wired"}, errors.New("claw_recall: no recall closure registered")
	}
	var req struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	facts := t.recall(ctx.Context, req.Query, req.MaxResults)
	if len(facts) == 0 {
		return Result{
			Title:   "claw_recall",
			Summary: "no facts match",
			Content: []ContentBlock{{Type: "text", Text: "no facts match"}},
		}, nil
	}
	body, _ := json.Marshal(facts)
	return Result{
		Title:   "claw_recall",
		Summary: fmt.Sprintf("found %d fact(s)", len(facts)),
		Content: []ContentBlock{{Type: "text", Text: string(body)}},
	}, nil
}

// RegisterClawFactTools wires the remember+recall closures.
func RegisterClawFactTools(
	reg *Registry,
	remember func(ctx context.Context, text, subject string) (FactRecord, error),
	recall func(ctx context.Context, query string, maxResults int) []FactRecord,
) {
	reg.Register(clawRememberTool{remember: remember}, "ClawRemember")
	reg.Register(clawRecallTool{recall: recall}, "ClawRecall")
}
