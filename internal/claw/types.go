package claw

import "time"

type Identity struct {
	Name        string    `json:"name"`
	Tone        string    `json:"tone"`
	Style       string    `json:"style"`
	Seed        string    `json:"seed"`
	UpdatedAt   time.Time `json:"updated_at"`
	Revision    int       `json:"revision"`
	Description string    `json:"description"`
}

type Soul struct {
	Values       []string  `json:"values"`
	Goals        []string  `json:"goals"`
	Traits       []string  `json:"traits"`
	LearnedNotes []string  `json:"learned_notes"`
	UpdatedAt    time.Time `json:"updated_at"`
	Revision     int       `json:"revision"`
}

type UserProfile struct {
	DisplayName string            `json:"display_name"`
	Timezone    string            `json:"timezone"`
	Preferences map[string]string `json:"preferences"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

type MemoryEvent struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Channel   string    `json:"channel"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

type MemorySummary struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"created_at"`
}

type ActionSuggestion struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Summary   string    `json:"summary"`
	Approved  bool      `json:"approved"`
	CreatedAt time.Time `json:"created_at"`
}

type InterviewTurn struct {
	Speaker   string    `json:"speaker"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

type Interview struct {
	Active       bool            `json:"active"`
	Current      int             `json:"current"`
	StartedAt    time.Time       `json:"started_at"`
	CompletedAt  time.Time       `json:"completed_at"`
	LastAnswered time.Time       `json:"last_answered"`
	Transcript   []InterviewTurn `json:"transcript"`
}

type Chat struct {
	SessionID  string          `json:"session_id"`
	Transcript []InterviewTurn `json:"transcript"`
}

type Memory struct {
	Events      []MemoryEvent      `json:"events"`
	Summaries   []MemorySummary    `json:"summaries"`
	Suggestions []ActionSuggestion `json:"suggestions"`
	Facts       []Fact             `json:"facts,omitempty"`
	LastDreamAt time.Time          `json:"last_dream_at"`
}

// Fact is a free-form piece of knowledge Claw should remember about
// the user, the user's contacts, or the world ("Nicolás is allergic to
// peanuts"; "the dentist's office closes at 18:00 on Fridays"). Stored
// flat — search is substring-based for now, semantic search can be
// added later by hooking yarn.Embed.
type Fact struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`           // the fact itself, free-form prose
	Subject   string    `json:"subject,omitempty"` // optional tag: "user", "Sebastián", "office"
	Source    string    `json:"source,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

type AgentRole struct {
	Name      string    `json:"name"`
	Purpose   string    `json:"purpose"`
	LastRunAt time.Time `json:"last_run_at"`
	LastTask  string    `json:"last_task"`
}

type Agents struct {
	Roles []AgentRole `json:"roles"`
}

type ToolDescriptor struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type Tools struct {
	Registered []ToolDescriptor `json:"registered"`
	UpdatedAt  time.Time        `json:"updated_at"`
}

type Channel struct {
	Name          string    `json:"name"`
	Provider      string    `json:"provider"`
	Enabled       bool      `json:"enabled"`
	LastInboundAt time.Time `json:"last_inbound_at"`
	LastError     string    `json:"last_error"`

	// AccountID, AccountName, PairedAt mirror the same fields on the
	// live channels.Status so the Hub UI can display link details
	// without holding a pointer to the live channel. Empty on backends
	// that don't have the concept of a paired account.
	AccountID   string    `json:"account_id,omitempty"`
	AccountName string    `json:"account_name,omitempty"`
	PairedAt    time.Time `json:"paired_at,omitzero"`

	// Threads stores per-contact conversation history so each contact
	// has their own session with Claw. Key is the contact's JID
	// (post-ToNonAD, e.g. "119567594582255@lid"). Trimmed to a fixed
	// window so growth stays bounded; older context is implicitly
	// summarized by Memory.Summaries when relevant.
	Threads map[string][]InterviewTurn `json:"threads,omitempty"`

	// AllowlistEnabled gates inbound auto-reply on Allowlist
	// membership. When false (default), every inbound message gets a
	// reply — current permissive behaviour. When true, only senders in
	// Allowlist get past the gate; everyone else receives a static
	// rejection message and the LLM is never invoked. This is the
	// "deploy Claw to friends/family without random numbers waking it
	// up" mode.
	AllowlistEnabled bool     `json:"allowlist_enabled,omitempty"`
	Allowlist        []string `json:"allowlist,omitempty"`
}

type Channels struct {
	Default string             `json:"default"`
	Items   map[string]Channel `json:"items"`
}

type Heartbeat struct {
	Running       bool      `json:"running"`
	Status        string    `json:"status"`
	OwnerID       string    `json:"owner_id"`
	LastStartedAt time.Time `json:"last_started_at"`
	LastBeatAt    time.Time `json:"last_beat_at"`
	LastStoppedAt time.Time `json:"last_stopped_at"`
	LastError     string    `json:"last_error"`
}

type CronJob struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Schedule   string    `json:"schedule"`
	Prompt     string    `json:"prompt"`
	Enabled    bool      `json:"enabled"`
	NextRunAt  time.Time `json:"next_run_at"`
	LastRunAt  time.Time `json:"last_run_at"`
	LastResult string    `json:"last_result"`
	LastError  string    `json:"last_error"`
}

// Contact is a person Claw has been told to remember. Stored in
// State.Contacts keyed by canonical name (lowercase, trimmed) so the
// LLM can look them up regardless of input casing. Fields beyond Name
// are optional — a quick "save Sebastian +57..." stores just name +
// phone, while a fuller "save my dentist Dr. López, phone X, email Y,
// allergic to penicillin" populates everything.
type Contact struct {
	Name      string    `json:"name"`
	Phone     string    `json:"phone,omitempty"`
	Email     string    `json:"email,omitempty"`
	Notes     string    `json:"notes,omitempty"`
	Source    string    `json:"source,omitempty"` // "claw_save_contact", "interview", etc.
	CreatedAt time.Time `json:"created_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

type State struct {
	Enabled    bool               `json:"enabled"`
	Identity   Identity           `json:"identity"`
	Soul       Soul               `json:"soul"`
	User       UserProfile        `json:"user"`
	Memory     Memory             `json:"memory"`
	Interview  Interview          `json:"interview"`
	Chat       Chat               `json:"chat"`
	Agents     Agents             `json:"agents"`
	Tools      Tools              `json:"tools"`
	Channels   Channels           `json:"channels"`
	Heartbeat  Heartbeat          `json:"heartbeat"`
	Crons      []CronJob          `json:"crons"`
	Contacts   map[string]Contact `json:"contacts,omitempty"`
	Reminders  []Reminder         `json:"reminders,omitempty"`
	LastUpdate time.Time          `json:"last_update"`
}

// Reminder is a one-shot scheduled message Claw sends through a
// channel when RemindAt is reached. Status transitions
// pending → sent or pending → canceled. Past-due pending reminders are
// fired the moment the pump catches up, so a forge restart doesn't
// silently drop them.
type Reminder struct {
	ID        string    `json:"id"`
	RemindAt  time.Time `json:"remind_at"`
	Body      string    `json:"body"`
	Channel   string    `json:"channel"` // e.g. "whatsapp", "mock"
	Target    string    `json:"target"`  // recipient JID/handle on that channel
	Status    string    `json:"status"`  // "pending" | "sent" | "canceled"
	CreatedAt time.Time `json:"created_at,omitzero"`
	SentAt    time.Time `json:"sent_at,omitzero"`
	LastError string    `json:"last_error,omitempty"`
}

type ActiveModel struct {
	ProviderName        string `json:"provider_name"`
	ModelID             string `json:"model_id"`
	SupportsTools       bool   `json:"supports_tools"`
	LoadedContextLength int    `json:"loaded_context_length"`
	MaxContextLength    int    `json:"max_context_length"`
}

type Status struct {
	State       State       `json:"state"`
	StorePath   string      `json:"store_path"`
	ActiveModel ActiveModel `json:"active_model"`
	InboxCount  int         `json:"inbox_count"`

	// Last-turn telemetry surfaced by the Hub status bar. LastModelUsed
	// names the model that produced the most recent reply. LastTokensUsed
	// is an estimate of the prompt size (input+history) we sent to that
	// model. LastTokensTotal mirrors the loaded context window when known,
	// so the bar can show pct-used. LastMode is "interview" or "chat".
	LastModelUsed   string `json:"last_model_used,omitempty"`
	LastTokensUsed  int    `json:"last_tokens_used,omitempty"`
	LastTokensTotal int    `json:"last_tokens_total,omitempty"`
	LastMode        string `json:"last_mode,omitempty"`
}

type DreamResult struct {
	Summary     string `json:"summary"`
	Suggestions int    `json:"suggestions"`
	Summaries   int    `json:"summaries"`
}

type InterviewReply struct {
	AssistantMessage string           `json:"assistant_message"`
	Done             bool             `json:"done"`
	Updates          InterviewUpdates `json:"updates"`
}

type InterviewUpdates struct {
	Identity      *InterviewIdentityUpdate `json:"identity,omitempty"`
	Soul          *InterviewSoulUpdate     `json:"soul,omitempty"`
	User          *InterviewUserUpdate     `json:"user,omitempty"`
	MemorySummary string                   `json:"memory_summary,omitempty"`
}

type InterviewIdentityUpdate struct {
	Name        string `json:"name,omitempty"`
	Tone        string `json:"tone,omitempty"`
	Style       string `json:"style,omitempty"`
	Seed        string `json:"seed,omitempty"`
	Description string `json:"description,omitempty"`
}

type InterviewSoulUpdate struct {
	Values       []string `json:"values,omitempty"`
	Goals        []string `json:"goals,omitempty"`
	Traits       []string `json:"traits,omitempty"`
	LearnedNotes []string `json:"learned_notes,omitempty"`
}

type InterviewUserUpdate struct {
	DisplayName string            `json:"display_name,omitempty"`
	Timezone    string            `json:"timezone,omitempty"`
	Preferences map[string]string `json:"preferences,omitempty"`
}
