package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge/internal/agent"
	"forge/internal/claw"
	"forge/internal/config"
	"forge/internal/globalconfig"
	"forge/internal/llm"
	"forge/internal/tools"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type shellClawFakeProvider struct {
	responses []string
	models    []llm.ModelInfo
	calls     int
}

func (f *shellClawFakeProvider) Name() string { return "fake" }
func (f *shellClawFakeProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	content := `{"assistant_message":"What language do you primarily use for conversation?","done":false,"updates":{}}`
	if f.calls < len(f.responses) {
		content = f.responses[f.calls]
	}
	f.calls++
	return &llm.ChatResponse{Content: content}, nil
}
func (f *shellClawFakeProvider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	ch := make(chan llm.ChatEvent)
	close(ch)
	return ch, nil
}
func (f *shellClawFakeProvider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return f.models, nil
}
func (f *shellClawFakeProvider) ProbeModel(ctx context.Context, modelID string) (*llm.ModelInfo, error) {
	for _, model := range f.models {
		if model.ID == modelID {
			copy := model
			return &copy, nil
		}
	}
	return nil, nil
}
func (f *shellClawFakeProvider) LoadModel(ctx context.Context, modelID string, cfg llm.LoadConfig) error {
	return nil
}

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
	if m.activeView != viewHub {
		t.Fatalf("activeView = %s, want %s", m.activeView, viewHub)
	}
	if m.sidebarIndex != 0 {
		t.Fatalf("sidebarIndex = %d, want 0 for Hub", m.sidebarIndex)
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

func TestShellWorkspaceAskUserModalKeepsFocusOffBaseInput(t *testing.T) {
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
	m.workspace.activeForm = formAskUser
	m.workspace.askUserForm = newAskUserForm(&agent.AskUserRequest{
		Question: "Choose one",
		Options:  []string{"A", "B", "C"},
	}, m.workspace.theme, 96, 24)
	m.workspace.input.Focus()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	next := updated.(shellModel)
	if next.workspace.askUserForm.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", next.workspace.askUserForm.cursor)
	}
	if next.workspace.input.Focused() {
		t.Fatal("expected workspace base input blurred while ask_user modal is active")
	}
}

func TestShellEnterInHubSidebarActivatesMainPane(t *testing.T) {
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	m.activePane = paneSidebar
	items := m.currentSidebarItems()
	pinnedIndex := -1
	for i, item := range items {
		if item.View == viewPinned {
			pinnedIndex = i
			break
		}
	}
	if pinnedIndex < 0 {
		t.Fatal("expected pinned item in hub sidebar")
	}
	m.sidebarIndex = pinnedIndex

	updated, _ := m.handleEnter()
	next := updated.(shellModel)
	if next.activeView != viewPinned {
		t.Fatalf("activeView = %s, want %s", next.activeView, viewPinned)
	}
	if next.activePane != paneMain {
		t.Fatalf("activePane = %s, want %s", next.activePane, paneMain)
	}
}

func TestHubSidebarIncludesChatBelowSettings(t *testing.T) {
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	items := m.currentSidebarItems()
	if len(items) < 4 {
		t.Fatal("expected hub sidebar items")
	}
	hub := items[0]
	if hub.View != viewHub {
		t.Fatalf("expected first hub sidebar item to be Hub, got %s", hub.View)
	}
	settings := items[len(items)-3]
	chat := items[len(items)-2]
	if settings.View != viewSettings || chat.View != viewChat {
		t.Fatalf("expected Settings then Chat before Claw, got settings=%s chat=%s", settings.View, chat.View)
	}
}

func TestShellEnterInHubSidebarOpensChat(t *testing.T) {
	globalHome := t.TempDir()
	t.Setenv("FORGE_GLOBAL_HOME", globalHome)
	cwd := t.TempDir()
	m := newShellModel(ShellOptions{
		InitialHubDir: cwd,
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	m.activePane = paneSidebar
	m.sidebarIndex = len(m.currentSidebarItems()) - 2 // Chat

	updated, cmd := m.handleEnter()
	next := updated.(shellModel)
	if next.activeView != viewChat {
		t.Fatalf("activeView = %s, want %s", next.activeView, viewChat)
	}
	if next.activePane != paneInput {
		t.Fatalf("activePane = %s, want %s", next.activePane, paneInput)
	}
	if next.hubChat == nil {
		t.Fatal("expected hub chat model to be created")
	}
	if got, want := next.hubChat.options.CWD, hubChatRootDir(); got != want {
		t.Fatalf("hub chat cwd = %q, want %q", got, want)
	}
	if next.hubChat.options.CWD == cwd {
		t.Fatalf("hub chat should not reuse explorer cwd %q", cwd)
	}
	if got := next.hubChat.agentRuntime.Mode; got != "chat" {
		t.Fatalf("hub chat mode = %q, want chat", got)
	}
	if cmd == nil {
		t.Fatal("expected resize/init command for hub chat")
	}
}

func TestShellHubAskUserModalKeepsFocusOffBaseInput(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	if cmd := m.ensureHubChatSession(); cmd == nil {
		t.Fatal("expected hub chat session command")
	}
	m.mode = modeHub
	m.activeView = viewChat
	m.activePane = paneInput
	m.hubChat.activeForm = formAskUser
	m.hubChat.askUserForm = newAskUserForm(&agent.AskUserRequest{
		Question: "Choose one",
		Options:  []string{"A", "B", "C"},
	}, m.hubChat.theme, 96, 24)
	m.hubChat.input.Focus()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	next := updated.(shellModel)
	if next.hubChat.askUserForm.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", next.hubChat.askUserForm.cursor)
	}
	if next.hubChat.input.Focused() {
		t.Fatal("expected hub base input blurred while ask_user modal is active")
	}
}

func TestHubChatIgnoresSelectedExplorerTarget(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	root := t.TempDir()
	child := filepath.Join(root, "repo")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	m := newShellModel(ShellOptions{
		InitialHubDir: root,
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	if len(m.explorerEntries) == 0 {
		t.Fatal("expected explorer entries")
	}
	m.explorerIndex = 0
	m.activePane = paneSidebar
	m.sidebarIndex = len(m.currentSidebarItems()) - 2 // Chat

	updated, _ := m.handleEnter()
	next := updated.(shellModel)
	if next.hubChat == nil {
		t.Fatal("expected hub chat model")
	}
	if got, want := next.hubChat.options.CWD, hubChatRootDir(); got != want {
		t.Fatalf("hub chat cwd = %q, want %q", got, want)
	}
	if got := next.hubChat.options.CWD; got == child {
		t.Fatalf("hub chat should not bind to selected explorer folder %q", child)
	}
}

func TestHubSidebarIncludesClawBelowChat(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	items := m.currentSidebarItems()
	if len(items) < 2 {
		t.Fatal("expected at least two sidebar items")
	}
	if got := items[len(items)-2].View; got != viewChat {
		t.Fatalf("penultimate hub sidebar item = %s, want %s", got, viewChat)
	}
	if got := items[len(items)-1].View; got != viewClaw {
		t.Fatalf("last hub sidebar item = %s, want %s", got, viewClaw)
	}
}

func TestHubClawInterviewStartsFromMainPane(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	m.activePane = paneSidebar
	m.sidebarIndex = len(m.currentSidebarItems()) - 1 // Claw

	updated, _ := m.handleEnter()
	m = updated.(shellModel)
	if m.activeView != viewClaw {
		t.Fatalf("activeView = %s, want %s", m.activeView, viewClaw)
	}
	if m.activePane != paneMain {
		t.Fatalf("activePane = %s, want %s after selecting Claw", m.activePane, paneMain)
	}

	updated, _ = m.handleEnter()
	m = updated.(shellModel)
	if !m.clawInterviewActive() {
		t.Fatal("expected Claw interview to become active")
	}
	if m.activePane != paneInput {
		t.Fatalf("activePane = %s, want %s after starting interview", m.activePane, paneInput)
	}
	if !strings.Contains(stripAnsi(m.renderClawMain()), "Interview") {
		t.Fatalf("expected Claw view to render interview transcript, got:\n%s", stripAnsi(m.renderClawMain()))
	}
}

func TestHubClawCompletedInterviewDoesNotRestartOnReenter(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Models["chat"] = "hub-chat"
	provider := &shellClawFakeProvider{
		models: []llm.ModelInfo{{ID: "hub-chat"}},
		responses: []string{
			`{"assistant_message":"What language do you primarily use for conversation?","done":false,"updates":{}}`,
			`{"assistant_message":"What should I call you?","done":false,"updates":{"user":{"preferences":{"preferred_language":"Spanish"}}}}`,
			`{"assistant_message":"Done.","done":true,"updates":{"user":{"display_name":"Nico"}}}`,
		},
	}
	providers := llm.NewRegistry()
	providers.Register(provider)
	service, err := claw.Open(cfg, providers, tools.NewRegistry())
	if err != nil {
		t.Fatalf("claw.Open: %v", err)
	}
	m.hubClaw = service
	if err := m.ensureClawInterview(); err != nil {
		t.Fatalf("ensureClawInterview: %v", err)
	}
	service = m.hubClawService()
	if service == nil {
		t.Fatal("expected claw service")
	}
	answers := []string{"Spanish", "Nico"}
	for _, answer := range answers {
		_, done, err := service.AnswerInterview(answer)
		if err != nil {
			t.Fatalf("AnswerInterview(%q): %v", answer, err)
		}
		if done {
			break
		}
	}
	before := service.Status().State.Interview
	if before.Active {
		t.Fatal("expected interview to be completed")
	}

	m.activeView = viewClaw
	m.activePane = paneMain
	updated, _ := m.handleEnter()
	m = updated.(shellModel)

	after := service.Status().State.Interview
	if after.Active {
		t.Fatal("expected completed interview to stay closed on re-enter")
	}
	if len(after.Transcript) != len(before.Transcript) {
		t.Fatalf("transcript length changed on re-enter: before=%d after=%d", len(before.Transcript), len(after.Transcript))
	}
}

func TestHubClawInputShowsPendingTurnBeforeReply(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	if err := m.ensureClawInterview(); err != nil {
		t.Fatalf("ensureClawInterview: %v", err)
	}
	m.activeView = viewClaw
	m.activePane = paneInput
	m.clawInput.SetValue("Nico")

	updated, cmd := m.handleClawInput(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(shellModel)
	if cmd == nil {
		t.Fatal("expected async claw answer command")
	}
	if !m.clawAwaitingReply {
		t.Fatal("expected claw awaiting reply state")
	}
	if got := m.clawPendingAnswer; got != "Nico" {
		t.Fatalf("clawPendingAnswer = %q, want %q", got, "Nico")
	}
	rendered := stripAnsi(m.renderClawMain())
	if !strings.Contains(rendered, "You: Nico") {
		t.Fatalf("expected pending user turn in claw view, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "thinking...") {
		t.Fatalf("expected thinking indicator in claw view, got:\n%s", rendered)
	}

	msg := cmd()
	updated, _ = m.Update(msg)
	m = updated.(shellModel)
	if m.clawAwaitingReply {
		t.Fatal("expected awaiting reply state to clear after response")
	}
	if m.clawPendingAnswer != "" {
		t.Fatalf("expected pending answer cleared, got %q", m.clawPendingAnswer)
	}
}

func TestHubClawKeepsChatInputAfterInterviewCompletion(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Models["chat"] = "hub-chat"
	provider := &shellClawFakeProvider{
		models: []llm.ModelInfo{{ID: "hub-chat"}},
		responses: []string{
			`{"assistant_message":"What language do you primarily use for conversation?","done":false,"updates":{}}`,
			`{"assistant_message":"Done.","done":true,"updates":{"user":{"preferences":{"preferred_language":"Spanish"}}}}`,
			`Hola, seguimos conversando.`,
		},
	}
	providers := llm.NewRegistry()
	providers.Register(provider)
	service, err := claw.Open(cfg, providers, tools.NewRegistry())
	if err != nil {
		t.Fatalf("claw.Open: %v", err)
	}
	m.hubClaw = service
	if err := m.ensureClawInterview(); err != nil {
		t.Fatalf("ensureClawInterview: %v", err)
	}
	m.activeView = viewClaw
	m.activePane = paneInput
	m.clawInput.SetValue("Spanish")

	updated, cmd := m.handleClawInput(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(shellModel)
	if cmd == nil {
		t.Fatal("expected async interview command")
	}
	updated, _ = m.Update(cmd())
	m = updated.(shellModel)
	if m.activePane != paneInput {
		t.Fatalf("activePane = %s, want %s after interview completion", m.activePane, paneInput)
	}
	if !m.clawInputEnabled() {
		t.Fatal("expected claw input to remain enabled after interview")
	}

	m.clawInput.SetValue("Sigues ahi?")
	updated, cmd = m.handleClawInput(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(shellModel)
	if cmd == nil {
		t.Fatal("expected async chat command")
	}
	updated, _ = m.Update(cmd())
	m = updated.(shellModel)
	rendered := stripAnsi(m.renderClawMain())
	if !strings.Contains(rendered, "Hola, seguimos conversando.") {
		t.Fatalf("expected post-interview claw chat reply, got:\n%s", rendered)
	}
}

func TestHubClawResetKeyStartsFreshChatSession(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Models["chat"] = "hub-chat"
	provider := &shellClawFakeProvider{
		models: []llm.ModelInfo{{ID: "hub-chat"}},
		responses: []string{
			`{"assistant_message":"What language do you primarily use for conversation?","done":false,"updates":{}}`,
			`{"assistant_message":"Done.","done":true,"updates":{"user":{"preferences":{"preferred_language":"Spanish"}}}}`,
			`Hola, seguimos conversando.`,
		},
	}
	providers := llm.NewRegistry()
	providers.Register(provider)
	service, err := claw.Open(cfg, providers, tools.NewRegistry())
	if err != nil {
		t.Fatalf("claw.Open: %v", err)
	}
	m.hubClaw = service
	if err := m.ensureClawInterview(); err != nil {
		t.Fatalf("ensureClawInterview: %v", err)
	}
	if _, done, err := service.AnswerInterview("Spanish"); err != nil || !done {
		t.Fatalf("AnswerInterview done=%v err=%v, want done=true err=nil", done, err)
	}
	if _, err := service.ChatContext(context.Background(), "Sigues ahi?"); err != nil {
		t.Fatalf("ChatContext: %v", err)
	}
	before := service.Status().State
	if before.Chat.SessionID == "" {
		t.Fatal("expected chat session id before reset")
	}
	if len(before.Chat.Transcript) == 0 {
		t.Fatal("expected chat transcript before reset")
	}

	m.activeView = viewClaw
	m.activePane = paneMain
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = updated.(shellModel)

	after := service.Status().State
	if after.Chat.SessionID == before.Chat.SessionID {
		t.Fatalf("expected chat session id to change after reset, got %q", after.Chat.SessionID)
	}
	if len(after.Chat.Transcript) != 0 {
		t.Fatalf("expected chat transcript cleared after reset, got %d turns", len(after.Chat.Transcript))
	}
	if !strings.Contains(m.statusMessage, "Claw chat reset.") {
		t.Fatalf("expected reset status message, got %q", m.statusMessage)
	}
}

func TestHubClawChannelsEnterUsesSelectedChannel(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	m.activeView = viewClaw
	m.activePane = paneMain
	m.clawSection = clawSectionChannels

	updated, _ := m.handleEnter()
	m = updated.(shellModel)
	if !m.clawChannelSelect {
		t.Fatal("expected first Enter to open channel selector")
	}
	if m.activeHubForm != hubFormNone {
		t.Fatalf("expected no modal when opening selector, got form %v", m.activeHubForm)
	}
	if !strings.Contains(m.statusMessage, "Select a channel") {
		t.Fatalf("unexpected selector status message: %q", m.statusMessage)
	}

	updated, _ = m.handleEnter()
	m = updated.(shellModel)
	if m.activeHubForm != hubFormNone {
		t.Fatalf("expected no modal for default mock channel, got form %v", m.activeHubForm)
	}
	if !strings.Contains(m.statusMessage, "Mock channel is always available") {
		t.Fatalf("unexpected status message for mock channel: %q", m.statusMessage)
	}

	updated, _ = m.handleDown()
	m = updated.(shellModel)
	if m.clawChannelIndex != 1 {
		t.Fatalf("clawChannelIndex = %d, want 1 for whatsapp", m.clawChannelIndex)
	}

	updated, _ = m.handleEnter()
	m = updated.(shellModel)
	if m.activeHubForm != hubFormWhatsApp {
		t.Fatalf("expected WhatsApp form after selecting whatsapp, got %v", m.activeHubForm)
	}
}

func TestHubClawChannelsEscClosesSelector(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	m.activeView = viewClaw
	m.activePane = paneMain
	m.clawSection = clawSectionChannels
	m.clawChannelSelect = true

	updated, _ := m.handleEsc()
	m = updated.(shellModel)
	if m.clawChannelSelect {
		t.Fatal("expected Esc to close channel selector")
	}
	if !strings.Contains(m.statusMessage, "Channel selection closed") {
		t.Fatalf("unexpected status message after closing selector: %q", m.statusMessage)
	}
}

func TestHubClawCtrlCQuitsFromInterviewInput(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	if err := m.ensureClawInterview(); err != nil {
		t.Fatalf("ensureClawInterview: %v", err)
	}
	canceled := false
	m.clawPendingCancel = func() { canceled = true }
	m.activeView = viewClaw
	m.activePane = paneInput

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", msg)
	}
	if !canceled {
		t.Fatal("expected Ctrl+C to cancel pending claw interview")
	}
}

func TestHubCtrlCQuitsWithWhatsAppFormOpen(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	m.activeView = viewClaw
	m.activeHubForm = hubFormWhatsApp
	m.whatsAppForm = newWhatsAppForm(m.theme)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected quit command with WhatsApp form open")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestClawViewRendersActiveHubForm(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	m.activeView = viewClaw
	m.activeHubForm = hubFormWhatsApp
	m.whatsAppForm = newWhatsAppForm(m.theme)

	rendered := stripAnsi(m.clawView())
	if !strings.Contains(rendered, "WhatsApp pairing") {
		t.Fatalf("expected claw view to render active WhatsApp form, got:\n%s", rendered)
	}
}

func TestClawPendingTranscriptStateAvoidsDuplicatePendingTurn(t *testing.T) {
	turns := []claw.InterviewTurn{
		{Speaker: "claw", Text: "Como te llamas?"},
		{Speaker: "user", Text: "Nico"},
	}
	showPending, showThinking := clawPendingTranscriptState(turns, "Nico")
	if showPending {
		t.Fatal("expected pending turn to stay hidden when transcript already has it")
	}
	if !showThinking {
		t.Fatal("expected thinking indicator while waiting on claw")
	}
}

func TestHubEscFromClawReturnsToHub(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	m.activeView = viewClaw
	m.activePane = paneMain
	m.sidebarIndex = len(m.currentSidebarItems()) - 1

	updated, _ := m.handleEsc()
	next := updated.(shellModel)
	if next.activeView != viewHub {
		t.Fatalf("activeView = %s, want %s", next.activeView, viewHub)
	}
	if next.sidebarIndex != 0 {
		t.Fatalf("sidebarIndex = %d, want 0 for Hub", next.sidebarIndex)
	}
}

func TestApplyHubChatConfigRefreshesOpenChat(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	t.Cleanup(func() {
		_ = (&m).Close()
	})
	if cmd := m.ensureHubChatSession(); cmd == nil {
		t.Fatal("expected hub chat session command")
	}
	if m.hubChat == nil {
		t.Fatal("expected hub chat model")
	}
	cfg := m.hubChat.options.Config
	cfg.Models["chat"] = "hub-chat"
	cfg.Models["planner"] = "hub-plan"
	cfg.ModelLoading.Enabled = true
	cfg.ModelLoading.Strategy = "parallel"

	m.applyHubChatConfig(cfg)

	if !m.hubChat.options.Config.ModelLoading.Enabled {
		t.Fatal("expected updated hub chat config to enable model loading")
	}
	if got := m.hubChat.agentRuntime.Config.Models["planner"]; got != "hub-plan" {
		t.Fatalf("planner model = %q, want %q", got, "hub-plan")
	}
	if got := m.hubChat.agentRuntime.Config.Models["chat"]; got != "hub-chat" {
		t.Fatalf("chat model = %q, want %q", got, "hub-chat")
	}
}

func TestDescribeWorkspaceSettingsShowsHubInheritedValues(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	modelID := "hub-chat"
	profile := "26B"
	if err := globalconfig.Save(globalconfig.GlobalConfig{
		Models: map[string]string{"chat": modelID},
		Yarn:   &globalconfig.YarnDefaults{Profile: &profile},
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithGlobal(cwd)
	if err != nil {
		t.Fatal(err)
	}
	config.InheritChatModelDefaults(&cfg)
	m := newModel(Options{CWD: cwd, Config: cfg})
	t.Cleanup(func() {
		if m.agentRuntime != nil {
			_ = m.agentRuntime.Close()
		}
	})

	output := stripAnsi(m.describeWorkspaceSettings())
	if !strings.Contains(output, "chat_model") || !strings.Contains(output, modelID) || !strings.Contains(output, "hub") {
		t.Fatalf("expected hub-inherited workspace settings, got:\n%s", output)
	}
}

func TestRenderHubStatusCompactsMultilineStatusMessage(t *testing.T) {
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 180, Height: 24})
	m = updated.(shellModel)
	m.activeView = viewExplorer
	m.statusMessage = "explorer = qwen\nplanner = qwen-long\nreviewer = mistral"

	got := stripAnsi(m.renderHubStatus())
	if strings.Contains(got, "\n") {
		t.Fatalf("hub status should stay single-line, got:\n%s", got)
	}
	if !strings.Contains(got, "explorer = qwen | planner = qwen-long | reviewer = mistral") {
		t.Fatalf("hub status did not compact multiline message:\n%s", got)
	}
}

func TestSelectingDifferentHubViewClearsTransientStatusMessage(t *testing.T) {
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	m.activeView = viewSettings
	m.selectSidebarView(viewSettings)
	m.statusMessage = "saved model multi"

	m.selectSidebarView(viewExplorer)

	if m.activeView != viewExplorer {
		t.Fatalf("activeView = %s, want %s", m.activeView, viewExplorer)
	}
	if m.statusMessage != "" {
		t.Fatalf("statusMessage = %q, want cleared after leaving settings", m.statusMessage)
	}
}

func TestHubViewFitsTerminalHeightWithMultilineStatusMessage(t *testing.T) {
	const terminalHeight = 24
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 96, Height: terminalHeight})
	m = updated.(shellModel)
	m.activeView = viewSettings
	m.statusMessage = "explorer = qwen\nplanner = qwen-long\nreviewer = mistral"

	if got := lipgloss.Height(m.View()); got > terminalHeight {
		t.Fatalf("view height = %d, want <= %d\n%s", got, terminalHeight, stripAnsi(m.View()))
	}
}

func TestExplorerKeepsSelectedRowVisibleWhenScrollingDown(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("dir-%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := newShellModel(ShellOptions{
		InitialHubDir: root,
		StateStore:    &memoryHubStateStore{},
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 96, Height: 14})
	m = updated.(shellModel)
	m.activeView = viewExplorer
	m.explorerIndex = 8

	lines := m.explorerVisibleLines()
	found := false
	for _, line := range lines {
		if strings.HasPrefix(stripAnsi(line), "> ") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected selected explorer row to remain visible, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestHubViewShowsContextualExplorerHelp(t *testing.T) {
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = updated.(shellModel)
	m.activePane = paneMain
	m.activeView = viewExplorer

	view := stripAnsi(m.hubView())
	if !strings.Contains(view, "Explorer: Up/Down move") || !strings.Contains(view, "Enter open dir") {
		t.Fatalf("expected explorer help footer, got:\n%s", view)
	}
}

func TestHubStatusStaysOnBottomLine(t *testing.T) {
	m := newShellModel(ShellOptions{
		InitialHubDir: t.TempDir(),
		StateStore:    &memoryHubStateStore{},
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = updated.(shellModel)
	m.activePane = paneSidebar
	m.activeView = viewExplorer

	lines := strings.Split(stripAnsi(m.hubView()), "\n")
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			last = lines[i]
			break
		}
	}
	if !strings.Contains(last, "HUB | pane:sidebar | view:explorer") {
		t.Fatalf("expected hub status on bottom line, got last non-empty line:\n%s\n\nfull view:\n%s", last, strings.Join(lines, "\n"))
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
