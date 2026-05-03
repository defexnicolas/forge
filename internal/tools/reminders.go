package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ReminderRecord mirrors claw.Reminder so this package doesn't import claw.
type ReminderRecord struct {
	ID        string `json:"id"`
	RemindAt  string `json:"remind_at"` // RFC3339
	Body      string `json:"body"`
	Channel   string `json:"channel"`
	Target    string `json:"target"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at,omitempty"`
	SentAt    string `json:"sent_at,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// claw_schedule_reminder

type clawScheduleReminderTool struct {
	schedule func(ctx context.Context, remindAt time.Time, body, channel, target string) (ReminderRecord, error)
}

func (clawScheduleReminderTool) Name() string { return "claw_schedule_reminder" }
func (clawScheduleReminderTool) Description() string {
	return "Schedule a reminder message Claw will send through a channel at a specific UTC time. Pass remind_at as an ISO 8601 timestamp (e.g. '2026-05-02T18:00:00Z' or '2026-05-02T13:00:00-05:00'). Channel defaults to 'whatsapp' when omitted; target is the recipient JID/handle (use the contact JID for WhatsApp). Use this when the user asks to be reminded of something, or to be pinged at a specific time."
}
func (clawScheduleReminderTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["remind_at","body","target"],"properties":{"remind_at":{"type":"string","description":"ISO 8601 timestamp. Convert relative phrasings ('in 1 hour', 'tomorrow at 9am') into an absolute timestamp before calling."},"body":{"type":"string","description":"The message Claw will send when the reminder fires."},"channel":{"type":"string","description":"Channel name (defaults to 'whatsapp')."},"target":{"type":"string","description":"Recipient JID/handle on the channel."}}}`)
}
func (clawScheduleReminderTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow, Reason: "Stores a reminder locally — fires later through the channel's normal Send path which retains its own permissions"}
}
func (t clawScheduleReminderTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	if t.schedule == nil {
		return Result{Title: "claw_schedule_reminder", Summary: "scheduler not wired"}, errors.New("claw_schedule_reminder: no schedule closure registered")
	}
	var req struct {
		RemindAt string `json:"remind_at"`
		Body     string `json:"body"`
		Channel  string `json:"channel"`
		Target   string `json:"target"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	body := strings.TrimSpace(req.Body)
	target := strings.TrimSpace(req.Target)
	channel := strings.TrimSpace(req.Channel)
	if channel == "" {
		channel = "whatsapp"
	}
	if body == "" || target == "" {
		return Result{Title: "claw_schedule_reminder", Summary: "body and target are required"}, errors.New("body and target are required")
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(req.RemindAt))
	if err != nil {
		return Result{
			Title:   "claw_schedule_reminder",
			Summary: "invalid remind_at: " + err.Error(),
			Content: []ContentBlock{{Type: "text", Text: "remind_at must be ISO 8601 (e.g. 2026-05-02T18:00:00Z); got " + req.RemindAt}},
		}, nil
	}
	rec, err := t.schedule(ctx.Context, at, body, channel, target)
	if err != nil {
		return Result{
			Title:   "claw_schedule_reminder",
			Summary: "schedule failed: " + err.Error(),
			Content: []ContentBlock{{Type: "text", Text: err.Error()}},
		}, nil
	}
	summary := fmt.Sprintf("scheduled %s for %s via %s", rec.ID, rec.RemindAt, rec.Channel)
	return Result{
		Title:   "claw_schedule_reminder",
		Summary: summary,
		Content: []ContentBlock{{Type: "text", Text: summary}},
	}, nil
}

// claw_list_reminders

type clawListRemindersTool struct {
	list func(ctx context.Context, status string) []ReminderRecord
}

func (clawListRemindersTool) Name() string { return "claw_list_reminders" }
func (clawListRemindersTool) Description() string {
	return "List Claw's scheduled reminders. Optional status filter: 'pending' (not yet fired), 'sent' (already delivered), 'canceled'. Empty status returns all."
}
func (clawListRemindersTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","description":"Filter: pending | sent | canceled. Empty returns all."}}}`)
}
func (clawListRemindersTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow, Reason: "Read-only listing of stored reminders"}
}
func (t clawListRemindersTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	if t.list == nil {
		return Result{Title: "claw_list_reminders", Summary: "scheduler not wired"}, errors.New("claw_list_reminders: no list closure registered")
	}
	var req struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(input, &req)
	hits := t.list(ctx.Context, req.Status)
	if len(hits) == 0 {
		return Result{Title: "claw_list_reminders", Summary: "no reminders", Content: []ContentBlock{{Type: "text", Text: "no reminders"}}}, nil
	}
	body, _ := json.Marshal(hits)
	return Result{
		Title:   "claw_list_reminders",
		Summary: fmt.Sprintf("%d reminder(s)", len(hits)),
		Content: []ContentBlock{{Type: "text", Text: string(body)}},
	}, nil
}

// claw_cancel_reminder

type clawCancelReminderTool struct {
	cancel func(ctx context.Context, id string) error
}

func (clawCancelReminderTool) Name() string { return "claw_cancel_reminder" }
func (clawCancelReminderTool) Description() string {
	return "Cancel a scheduled reminder by its ID. Already-sent or canceled reminders return an error message."
}
func (clawCancelReminderTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"string","description":"Reminder ID returned by claw_schedule_reminder or claw_list_reminders."}}}`)
}
func (clawCancelReminderTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow, Reason: "Cancels a local reminder before it fires — no external side effects"}
}
func (t clawCancelReminderTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	if t.cancel == nil {
		return Result{Title: "claw_cancel_reminder", Summary: "scheduler not wired"}, errors.New("claw_cancel_reminder: no cancel closure registered")
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return Result{Title: "claw_cancel_reminder", Summary: "id is required"}, errors.New("id is required")
	}
	if err := t.cancel(ctx.Context, id); err != nil {
		return Result{
			Title:   "claw_cancel_reminder",
			Summary: "cancel failed: " + err.Error(),
			Content: []ContentBlock{{Type: "text", Text: err.Error()}},
		}, nil
	}
	return Result{
		Title:   "claw_cancel_reminder",
		Summary: "canceled " + id,
		Content: []ContentBlock{{Type: "text", Text: "canceled " + id}},
	}, nil
}

// RegisterClawReminderTools wires the scheduler closures.
func RegisterClawReminderTools(
	reg *Registry,
	schedule func(ctx context.Context, remindAt time.Time, body, channel, target string) (ReminderRecord, error),
	list func(ctx context.Context, status string) []ReminderRecord,
	cancel func(ctx context.Context, id string) error,
) {
	reg.Register(clawScheduleReminderTool{schedule: schedule}, "ClawScheduleReminder")
	reg.Register(clawListRemindersTool{list: list}, "ClawListReminders")
	reg.Register(clawCancelReminderTool{cancel: cancel}, "ClawCancelReminder")
}
