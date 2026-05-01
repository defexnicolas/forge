package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestShellStartsInHubWithoutWorkspace(t *testing.T) {
	store := &memoryHubStateStore{}
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    store,
	})
	if m.mode != modeHub {
		t.Fatalf("mode = %s, want %s", m.mode, modeHub)
	}
	if m.workspace != nil {
		t.Fatal("expected no workspace at startup")
	}
	if m.activeView != viewExplorer {
		t.Fatalf("activeView = %s, want %s", m.activeView, viewExplorer)
	}
}

func TestShellStartsInWorkspaceWhenProvided(t *testing.T) {
	cwd := t.TempDir()
	m := newShellModel(ShellOptions{
		InitialHubDir:    cwd,
		InitialWorkspace: &WorkspaceSession{Options: Options{CWD: cwd}},
		StateStore:       &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	if m.mode != modeWorkspace {
		t.Fatalf("mode = %s, want %s", m.mode, modeWorkspace)
	}
	if m.workspace == nil {
		t.Fatal("expected workspace model")
	}
	if m.workspace.options.CWD != cwd {
		t.Fatalf("workspace cwd = %q, want %q", m.workspace.options.CWD, cwd)
	}
	if m.activePane != paneInput {
		t.Fatalf("activePane = %s, want %s", m.activePane, paneInput)
	}
}

func TestShellOpenWorkspaceClosesPreviousSession(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	closeCount := 0
	store := &memoryHubStateStore{}
	m := newShellModel(ShellOptions{
		InitialHubDir: dirA,
		StateStore:    store,
		OpenWorkspace: func(cwd, resume string) (*WorkspaceSession, error) {
			return &WorkspaceSession{
				Options: Options{CWD: cwd},
				CloseFunc: func() error {
					closeCount++
					return nil
				},
			}, nil
		},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})

	if err := m.OpenWorkspace(dirA); err != nil {
		t.Fatal(err)
	}
	if err := m.OpenWorkspace(dirB); err != nil {
		t.Fatal(err)
	}
	if closeCount != 1 {
		t.Fatalf("closeCount = %d, want 1", closeCount)
	}
	if got := m.workspace.options.CWD; got != dirB {
		t.Fatalf("workspace cwd = %q, want %q", got, dirB)
	}
	if len(store.state.RecentWorkspaces) == 0 || store.state.RecentWorkspaces[0].Path != dirB {
		t.Fatalf("recent workspaces not updated: %+v", store.state.RecentWorkspaces)
	}
}

func TestShellSwitchToHubClosesWorkspace(t *testing.T) {
	cwd := t.TempDir()
	closeCount := 0
	m := newShellModel(ShellOptions{
		InitialHubDir: cwd,
		InitialWorkspace: &WorkspaceSession{
			Options: Options{CWD: cwd},
			CloseFunc: func() error {
				closeCount++
				return nil
			},
		},
		StateStore: &memoryHubStateStore{},
	})

	if err := m.SwitchToHub(); err != nil {
		t.Fatal(err)
	}
	if closeCount != 1 {
		t.Fatalf("closeCount = %d, want 1", closeCount)
	}
	if m.mode != modeHub {
		t.Fatalf("mode = %s, want %s", m.mode, modeHub)
	}
	if m.workspace != nil {
		t.Fatal("expected workspace closed")
	}
}

func TestShellExplorerNavigatesDirectories(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "repo")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	m := newShellModel(ShellOptions{
		InitialHubDir: root,
		StateStore:    &memoryHubStateStore{},
	})
	if len(m.explorerEntries) == 0 {
		t.Fatal("expected explorer entries")
	}
	m.enterExplorerSelection()
	if m.explorerDir != child {
		t.Fatalf("explorerDir = %q, want %q", m.explorerDir, child)
	}
	m.moveExplorerParent()
	if m.explorerDir != root {
		t.Fatalf("explorerDir after parent = %q, want %q", m.explorerDir, root)
	}
}

func TestShellTabDoesNotLeaveWorkspaceInputPane(t *testing.T) {
	cwd := t.TempDir()
	m := newShellModel(ShellOptions{
		InitialHubDir:    cwd,
		InitialWorkspace: &WorkspaceSession{Options: Options{CWD: cwd}},
		StateStore:       &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	m.activePane = paneInput

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	next := updated.(shellModel)
	if next.activePane != paneInput {
		t.Fatalf("activePane = %s, want %s", next.activePane, paneInput)
	}
}

func TestShellCtrlWSwitchesWorkspacePane(t *testing.T) {
	cwd := t.TempDir()
	m := newShellModel(ShellOptions{
		InitialHubDir:    cwd,
		InitialWorkspace: &WorkspaceSession{Options: Options{CWD: cwd}},
		StateStore:       &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	m.activePane = paneInput

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
	next := updated.(shellModel)
	if next.activePane == paneInput {
		t.Fatalf("expected ctrl+w to rotate away from input pane")
	}
}

func TestShellSelectingChatFromSidebarFocusesInput(t *testing.T) {
	cwd := t.TempDir()
	m := newShellModel(ShellOptions{
		InitialHubDir:    cwd,
		InitialWorkspace: &WorkspaceSession{Options: Options{CWD: cwd}},
		StateStore:       &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	m.activePane = paneSidebar
	m.activeView = viewPlan
	m.sidebarIndex = 0

	updated, _ := m.handleEnter()
	next := updated.(shellModel)
	if next.activeView != viewChat {
		t.Fatalf("activeView = %s, want %s", next.activeView, viewChat)
	}
	if next.activePane != paneInput {
		t.Fatalf("activePane = %s, want %s", next.activePane, paneInput)
	}
}

func TestShellEnterInWorkspaceInputForwardsToWorkspace(t *testing.T) {
	cwd := t.TempDir()
	m := newShellModel(ShellOptions{
		InitialHubDir:    cwd,
		InitialWorkspace: &WorkspaceSession{Options: Options{CWD: cwd}},
		StateStore:       &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	m.activePane = paneInput
	m.workspace.input.SetValue("/help")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	next := updated.(shellModel)
	if got := next.workspace.input.Value(); got != "" {
		t.Fatalf("workspace input = %q, want empty after enter", got)
	}
}

type memoryHubStateStore struct {
	state HubState
}

func (s *memoryHubStateStore) Load() (HubState, error) {
	return s.state, nil
}

func (s *memoryHubStateStore) Save(state HubState) error {
	s.state = state
	return nil
}
