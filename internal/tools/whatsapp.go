package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// whatsAppSendTool dispatches outbound WhatsApp messages through the
// Claw service. The service holds the live whatsmeow session; this tool
// only carries the closure that knows how to reach it. Closure-on-
// register matches the pattern used by RegisterRunSkillTool.
//
// Permission is PermissionAsk on purpose — outbound messages cannot be
// undone from forge once sent, and a runaway model spamming a real
// contact would be embarrassing at best, account-banning at worst. The
// approval_profile = 'auto' setting bypasses the prompt for users who
// have decided the trade-off is worth it (anti-ban guardrails inside
// the WhatsApp channel — typing simulation, rate limit, link guard —
// stay active regardless).
type whatsAppSendTool struct {
	sender func(ctx context.Context, to, body string) error
}

func (whatsAppSendTool) Name() string { return "whatsapp_send" }
func (whatsAppSendTool) Description() string {
	return "Send a text message via the user's paired WhatsApp account. The recipient must be a JID like 5215555555555@s.whatsapp.net (individual) or 120363042-12345@g.us (group). Anti-ban guardrails (typing simulation, rate limit, first-contact link guard) are enforced inside the channel; this tool just hands the message to that pipeline."
}
func (whatsAppSendTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["to","body"],"properties":{"to":{"type":"string","description":"WhatsApp JID — e.g. 5215555555555@s.whatsapp.net for an individual or 120363042-12345@g.us for a group."},"body":{"type":"string","description":"Plain-text message body. WhatsApp markdown (* _ ~) is supported."}}}`)
}
func (whatsAppSendTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAsk, Reason: "WhatsApp send reaches a real contact and cannot be undone from forge"}
}
func (t whatsAppSendTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	if t.sender == nil {
		return Result{Title: "whatsapp_send", Summary: "WhatsApp channel not registered"}, errors.New("whatsapp_send invoked but no sender closure was registered — pair WhatsApp via HUB → Claw → Channels first")
	}
	var req struct {
		To   string `json:"to"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	to := strings.TrimSpace(req.To)
	body := strings.TrimSpace(req.Body)
	if to == "" {
		return Result{Title: "whatsapp_send", Summary: "to is required"}, errors.New("to is required")
	}
	if body == "" {
		return Result{Title: "whatsapp_send", Summary: "body is required"}, errors.New("body is required")
	}
	if err := t.sender(ctx.Context, to, body); err != nil {
		// Surface the error as tool result text so the model can react
		// (e.g. a rate-limit refusal becomes "wait then retry").
		return Result{
			Title:   "whatsapp_send",
			Summary: "send failed: " + err.Error(),
			Content: []ContentBlock{{Type: "text", Text: err.Error()}},
		}, nil
	}
	return Result{
		Title:   "whatsapp_send",
		Summary: fmt.Sprintf("delivered to %s (%d chars)", to, len(body)),
	}, nil
}

// RegisterWhatsAppSendTool wires a sender closure into the registry.
// Pass nil to register a placeholder that returns a clear error to the
// model — useful when WhatsApp has not been paired yet so the tool
// stays advertised but explains itself instead of silently no-op'ing.
func RegisterWhatsAppSendTool(reg *Registry, sender func(ctx context.Context, to, body string) error) {
	reg.Register(whatsAppSendTool{sender: sender}, "WhatsAppSend")
}
