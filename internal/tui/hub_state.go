package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type RecentWorkspace struct {
	Path     string    `json:"path"`
	OpenedAt time.Time `json:"opened_at"`
}

type HubState struct {
	LastHubDir       string            `json:"last_hub_dir"`
	RecentWorkspaces []RecentWorkspace `json:"recent_workspaces"`
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
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, ".codex", "memories", "forge_hub_state.json")
		} else {
			path = filepath.Join(".", ".forge_hub_state.json")
		}
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
