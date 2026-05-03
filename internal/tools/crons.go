package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// CronRecord mirrors claw.CronJob without importing claw — keeps the tools
// layer free of cycles and gives the LLM a stable schema regardless of
// internal type changes.
type CronRecord struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Schedule   string `json:"schedule"`
	Prompt     string `json:"prompt"`
	Enabled    bool   `json:"enabled"`
	NextRunAt  string `json:"next_run_at"`
	LastRunAt  string `json:"last_run_at,omitempty"`
	LastResult string `json:"last_result,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

// claw_add_cron

type clawAddCronTool struct {
	add func(ctx context.Context, name, schedule, prompt string) (CronRecord, error)
}

func (clawAddCronTool) Name() string { return "claw_add_cron" }
func (clawAddCronTool) Description() string {
	return "Schedule a recurring task Claw will run on its own heartbeat. The schedule supports several human-friendly forms: '@every 30m', '@every 1h', '@hourly', '@daily', '@weekly', '@at 09:00' (every day at 09:00 in the user's timezone), '@dow Mon 09:00' (every Monday at 09:00), or a 5-field cron expression ('0 9 * * *'). The prompt is what Claw will think about when the cron fires — it can call other tools (web_fetch, whatsapp_send, claw_remember) to act on it. Use this when the user asks for anything recurring: 'every morning send me…', 'check the news at 6pm', 'remind me on Mondays to…'."
}
func (clawAddCronTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["name","schedule","prompt"],"properties":{"name":{"type":"string","description":"Short human-readable label for the cron (e.g. 'morning briefing')."},"schedule":{"type":"string","description":"@every <duration> | @hourly | @daily | @weekly | @at HH:MM | @dow Mon HH:MM | 'M H DOM MON DOW'."},"prompt":{"type":"string","description":"Natural-language instruction Claw will execute when the cron fires."}}}`)
}
func (clawAddCronTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow, Reason: "Stores a recurring task locally; each firing reuses the user's existing per-tool permissions"}
}
func (t clawAddCronTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	if t.add == nil {
		return Result{Title: "claw_add_cron", Summary: "scheduler not wired"}, errors.New("claw_add_cron: no add closure registered")
	}
	var req struct {
		Name     string `json:"name"`
		Schedule string `json:"schedule"`
		Prompt   string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	name := strings.TrimSpace(req.Name)
	schedule := strings.TrimSpace(req.Schedule)
	prompt := strings.TrimSpace(req.Prompt)
	if name == "" || schedule == "" || prompt == "" {
		return Result{Title: "claw_add_cron", Summary: "name, schedule and prompt are required"}, errors.New("name, schedule and prompt are required")
	}
	rec, err := t.add(ctx.Context, name, schedule, prompt)
	if err != nil {
		return Result{
			Title:   "claw_add_cron",
			Summary: "schedule failed: " + err.Error(),
			Content: []ContentBlock{{Type: "text", Text: err.Error()}},
		}, nil
	}
	summary := fmt.Sprintf("scheduled cron %s (%s) next run %s", rec.ID, rec.Schedule, rec.NextRunAt)
	return Result{
		Title:   "claw_add_cron",
		Summary: summary,
		Content: []ContentBlock{{Type: "text", Text: summary}},
	}, nil
}

// claw_list_crons

type clawListCronsTool struct {
	list func(ctx context.Context) []CronRecord
}

func (clawListCronsTool) Name() string { return "claw_list_crons" }
func (clawListCronsTool) Description() string {
	return "List Claw's scheduled crons with their next run time and last result. Use this when the user asks 'what do you have scheduled?' or before adding a cron to avoid duplicates."
}
func (clawListCronsTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (clawListCronsTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow, Reason: "Read-only listing of stored crons"}
}
func (t clawListCronsTool) Run(ctx Context, _ json.RawMessage) (Result, error) {
	if t.list == nil {
		return Result{Title: "claw_list_crons", Summary: "scheduler not wired"}, errors.New("claw_list_crons: no list closure registered")
	}
	hits := t.list(ctx.Context)
	if len(hits) == 0 {
		return Result{Title: "claw_list_crons", Summary: "no crons scheduled", Content: []ContentBlock{{Type: "text", Text: "no crons scheduled"}}}, nil
	}
	body, _ := json.Marshal(hits)
	return Result{
		Title:   "claw_list_crons",
		Summary: fmt.Sprintf("%d cron(s)", len(hits)),
		Content: []ContentBlock{{Type: "text", Text: string(body)}},
	}, nil
}

// claw_remove_cron

type clawRemoveCronTool struct {
	remove func(ctx context.Context, id string) error
}

func (clawRemoveCronTool) Name() string { return "claw_remove_cron" }
func (clawRemoveCronTool) Description() string {
	return "Remove a previously-scheduled cron by ID. Idempotent: returns success even if the ID was already gone, so the LLM can call it without checking first."
}
func (clawRemoveCronTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"string","description":"Cron ID returned by claw_add_cron or claw_list_crons."}}}`)
}
func (clawRemoveCronTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow, Reason: "Removes a local cron — no external side effects"}
}
func (t clawRemoveCronTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	if t.remove == nil {
		return Result{Title: "claw_remove_cron", Summary: "scheduler not wired"}, errors.New("claw_remove_cron: no remove closure registered")
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return Result{Title: "claw_remove_cron", Summary: "id is required"}, errors.New("id is required")
	}
	if err := t.remove(ctx.Context, id); err != nil {
		return Result{
			Title:   "claw_remove_cron",
			Summary: "remove failed: " + err.Error(),
			Content: []ContentBlock{{Type: "text", Text: err.Error()}},
		}, nil
	}
	return Result{
		Title:   "claw_remove_cron",
		Summary: "removed " + id,
		Content: []ContentBlock{{Type: "text", Text: "removed " + id}},
	}, nil
}

// RegisterClawCronTools wires the cron management closures.
func RegisterClawCronTools(
	reg *Registry,
	add func(ctx context.Context, name, schedule, prompt string) (CronRecord, error),
	list func(ctx context.Context) []CronRecord,
	remove func(ctx context.Context, id string) error,
) {
	reg.Register(clawAddCronTool{add: add}, "ClawAddCron")
	reg.Register(clawListCronsTool{list: list}, "ClawListCrons")
	reg.Register(clawRemoveCronTool{remove: remove}, "ClawRemoveCron")
}
