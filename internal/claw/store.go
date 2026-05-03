package claw

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Store struct {
	path  string
	mu    sync.Mutex
	state State
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	store := &Store{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			store.state = defaultState()
			return store, store.saveLocked()
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &store.state); err != nil {
		return nil, err
	}
	ensureStateDefaults(&store.state)
	return store, nil
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fn(&s.state); err != nil {
		return err
	}
	ensureStateDefaults(&s.state)
	s.state.LastUpdate = time.Now().UTC()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func defaultState() State {
	now := time.Now().UTC()
	return State{
		Identity: Identity{
			Name:        "Claw",
			Tone:        "warm",
			Style:       "direct",
			Seed:        "A resident Forge companion with memory, initiative, and restraint.",
			UpdatedAt:   now,
			Revision:    1,
			Description: "Global Forge companion",
		},
		Soul: Soul{
			Values:       []string{"useful", "truthful", "steady"},
			Goals:        []string{"help the user follow through", "remember commitments"},
			Traits:       []string{"observant", "calm"},
			LearnedNotes: []string{},
			UpdatedAt:    now,
			Revision:     1,
		},
		User: UserProfile{
			DisplayName: "User",
			Timezone:    "",
			Preferences: map[string]string{},
			UpdatedAt:   now,
		},
		Memory: Memory{
			Summaries: []MemorySummary{
				{
					ID:        "seed-summary",
					Source:    "seed",
					Summary:   "Claw starts with generic memory and learns the user's routines, tone, priorities, and relationships through conversation.",
					CreatedAt: now,
				},
			},
			Suggestions: []ActionSuggestion{
				{
					ID:        "seed-suggestion",
					Source:    "seed",
					Summary:   "Run the initial Claw interview from the Hub to personalize identity, soul, user profile, and memory.",
					CreatedAt: now,
				},
			},
		},
		Agents: Agents{
			Roles: []AgentRole{
				{Name: "concierge", Purpose: "Handle inbound messages"},
				{Name: "planner", Purpose: "Turn intent into tasks and crons"},
				{Name: "dreamer", Purpose: "Consolidate memory and propose actions"},
				{Name: "operator", Purpose: "Run approved actions via Forge"},
			},
		},
		Channels: Channels{
			Default: "mock",
			Items: map[string]Channel{
				"mock": {
					Name:     "mock",
					Provider: "inbox",
					Enabled:  true,
				},
			},
		},
		Heartbeat: Heartbeat{
			Status: "stopped",
		},
		Crons:      []CronJob{},
		LastUpdate: now,
	}
}

func ensureStateDefaults(state *State) {
	if state == nil {
		return
	}
	if state.Identity.Name == "" {
		state.Identity.Name = "Claw"
	}
	if state.Identity.Tone == "" {
		state.Identity.Tone = "warm"
	}
	if state.Identity.Style == "" {
		state.Identity.Style = "direct"
	}
	if state.Identity.Seed == "" {
		state.Identity.Seed = "A resident Forge companion with memory, initiative, and restraint."
	}
	if state.Identity.Revision <= 0 {
		state.Identity.Revision = 1
	}
	if state.Channels.Default == "" {
		state.Channels.Default = "mock"
	}
	if state.Channels.Items == nil {
		state.Channels.Items = map[string]Channel{}
	}
	if _, ok := state.Channels.Items["mock"]; !ok {
		state.Channels.Items["mock"] = Channel{Name: "mock", Provider: "inbox", Enabled: true}
	}
	if state.User.Preferences == nil {
		state.User.Preferences = map[string]string{}
	}
	if len(state.Memory.Summaries) == 0 {
		state.Memory.Summaries = []MemorySummary{{
			ID:        "seed-summary",
			Source:    "seed",
			Summary:   "Claw starts with generic memory and learns the user's routines, tone, priorities, and relationships through conversation.",
			CreatedAt: time.Now().UTC(),
		}}
	}
	if len(state.Memory.Suggestions) == 0 {
		state.Memory.Suggestions = []ActionSuggestion{{
			ID:        "seed-suggestion",
			Source:    "seed",
			Summary:   "Run the initial Claw interview from the Hub to personalize identity, soul, user profile, and memory.",
			CreatedAt: time.Now().UTC(),
		}}
	}
	if state.Heartbeat.Status == "" {
		state.Heartbeat.Status = "stopped"
	}
}
