package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ContactRecord is the shape returned by the lookup closure. Mirrors
// claw.Contact but lives here to avoid an import cycle (tools is
// imported by claw, not the other way).
type ContactRecord struct {
	Name      string `json:"name"`
	Phone     string `json:"phone,omitempty"`
	Email     string `json:"email,omitempty"`
	Notes     string `json:"notes,omitempty"`
	Source    string `json:"source,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// claw_save_contact

type clawSaveContactTool struct {
	save func(ctx context.Context, name, phone, email, notes string) (ContactRecord, error)
}

func (clawSaveContactTool) Name() string { return "claw_save_contact" }
func (clawSaveContactTool) Description() string {
	return "Save or update a contact (a person Claw should remember). Use when the user asks Claw to 'agendar', 'guardar', 'remember' or 'save' someone. Name is required; phone, email, and notes are optional. Re-saving the same name updates only the fields you provide."
}
func (clawSaveContactTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string","description":"Display name of the contact (e.g. 'Sebastian', 'Dra. López')."},"phone":{"type":"string","description":"Phone number in any reasonable format (will be stored as given)."},"email":{"type":"string","description":"Email address."},"notes":{"type":"string","description":"Free-form notes — relationship, allergies, preferences, etc."}}}`)
}
func (clawSaveContactTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow, Reason: "Saves a contact in Claw's local state — no external side effects"}
}
func (t clawSaveContactTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	if t.save == nil {
		return Result{Title: "claw_save_contact", Summary: "contact store not wired"}, errors.New("claw_save_contact: no save closure registered")
	}
	var req struct {
		Name  string `json:"name"`
		Phone string `json:"phone"`
		Email string `json:"email"`
		Notes string `json:"notes"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Result{Title: "claw_save_contact", Summary: "name is required"}, errors.New("name is required")
	}
	rec, err := t.save(ctx.Context, name, req.Phone, req.Email, req.Notes)
	if err != nil {
		return Result{
			Title:   "claw_save_contact",
			Summary: "save failed: " + err.Error(),
			Content: []ContentBlock{{Type: "text", Text: err.Error()}},
		}, nil
	}
	summary := fmt.Sprintf("saved %s", rec.Name)
	if rec.Phone != "" {
		summary += " (" + rec.Phone + ")"
	}
	return Result{
		Title:   "claw_save_contact",
		Summary: summary,
		Content: []ContentBlock{{Type: "text", Text: summary}},
	}, nil
}

// claw_lookup_contact

type clawLookupContactTool struct {
	lookup func(ctx context.Context, name string) (ContactRecord, bool)
}

func (clawLookupContactTool) Name() string { return "claw_lookup_contact" }
func (clawLookupContactTool) Description() string {
	return "Look up a saved contact by name. Match is case-insensitive and supports substring matching, so 'sebas' will find 'Sebastián'. Returns the contact's known fields, or a not-found message."
}
func (clawLookupContactTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string","description":"Name (or partial name) of the contact to look up."}}}`)
}
func (clawLookupContactTool) Permission(_ Context, _ json.RawMessage) PermissionRequest {
	return PermissionRequest{Decision: PermissionAllow, Reason: "Read-only lookup of Claw's local contact store"}
}
func (t clawLookupContactTool) Run(ctx Context, input json.RawMessage) (Result, error) {
	if t.lookup == nil {
		return Result{Title: "claw_lookup_contact", Summary: "contact store not wired"}, errors.New("claw_lookup_contact: no lookup closure registered")
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return Result{}, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Result{Title: "claw_lookup_contact", Summary: "name is required"}, errors.New("name is required")
	}
	rec, ok := t.lookup(ctx.Context, name)
	if !ok {
		return Result{
			Title:   "claw_lookup_contact",
			Summary: "no contact matches " + name,
			Content: []ContentBlock{{Type: "text", Text: "no contact matches " + name}},
		}, nil
	}
	body, _ := json.Marshal(rec)
	return Result{
		Title:   "claw_lookup_contact",
		Summary: "found " + rec.Name,
		Content: []ContentBlock{{Type: "text", Text: string(body)}},
	}, nil
}

// RegisterClawContactTools wires the save+lookup closures. Pass nil to
// register placeholder tools that surface a clear error message to the
// model — useful when the Claw service hasn't booted yet so the tools
// stay advertised but explain themselves instead of silently no-op'ing.
func RegisterClawContactTools(
	reg *Registry,
	save func(ctx context.Context, name, phone, email, notes string) (ContactRecord, error),
	lookup func(ctx context.Context, name string) (ContactRecord, bool),
) {
	reg.Register(clawSaveContactTool{save: save}, "ClawSaveContact")
	reg.Register(clawLookupContactTool{lookup: lookup}, "ClawLookupContact")
}
