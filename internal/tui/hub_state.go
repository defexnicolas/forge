package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"forge/internal/globalconfig"
)

type RecentWorkspace struct {
	Path     string    `json:"path"`
	OpenedAt time.Time `json:"opened_at"`
}

type HubState struct {
	LastHubDir       string            `json:"last_hub_dir"`
	RecentWorkspaces []RecentWorkspace `json:"recent_workspaces"`
	// Pinned holds workspaces the user has explicitly favorited. Order is
	// preserved so the user can curate a list manually; it is not derived
	// from RecentWorkspaces. Pinned paths can also appear in
	// RecentWorkspaces -- the sidebar dedupes them out of the Recent view.
	Pinned []string `json:"pinned,omitempty"`
	// MigrationDone is set to true the first time the Hub finishes (or
	// dismisses) the wizard that proposes moving theme/models/yarn from
	// existing per-workspace .forge/config.toml files into the new global
	// config. It is intentionally a one-shot flag -- the wizard is a
	// migration aid, not a recurring chore.
	MigrationDone bool `json:"migration_done,omitempty"`
}

// Pin adds path to the pinned list if it is not already there. Returns true
// if it was added, false if it was already pinned. Idempotent.
func (s *HubState) Pin(path string) bool {
	for _, p := range s.Pinned {
		if p == path {
			return false
		}
	}
	s.Pinned = append(s.Pinned, path)
	return true
}

// Unpin removes path from the pinned list. Returns true if it was present.
func (s *HubState) Unpin(path string) bool {
	for i, p := range s.Pinned {
		if p == path {
			s.Pinned = append(s.Pinned[:i], s.Pinned[i+1:]...)
			return true
		}
	}
	return false
}

// IsPinned reports whether path is in the pinned list.
func (s HubState) IsPinned(path string) bool {
	for _, p := range s.Pinned {
		if p == path {
			return true
		}
	}
	return false
}

type HubStateStore interface {
	Load() (HubState, error)
	Save(HubState) error
}

type fileHubStateStore struct {
	path string
}

func NewFileHubStateStore(path string) HubStateStore {
	if path == "" {
		// Lives alongside global.toml under the forge home dir
		// (~/.forge/hub_state.json by default). globalconfig.Migrate()
		// copies the legacy ~/.codex/memories/forge_hub_state.json on
		// first launch so existing pinned/recent state survives.
		path = filepath.Join(globalconfig.HomeDir(), "hub_state.json")
	}
	return fileHubStateStore{path: path}
}

func (s fileHubStateStore) Load() (HubState, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return HubState{}, nil
		}
		return HubState{}, err
	}
	var state HubState
	if err := json.Unmarshal(data, &state); err != nil {
		return HubState{}, err
	}
	sort.Slice(state.RecentWorkspaces, func(i, j int) bool {
		return state.RecentWorkspaces[i].OpenedAt.After(state.RecentWorkspaces[j].OpenedAt)
	})
	return state, nil
}

func (s fileHubStateStore) Save(state HubState) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}
