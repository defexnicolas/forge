package claw

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge/internal/claw/channels"
	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/tools"
)

type clawFakeProvider struct {
	responses  []string
	models     []llm.ModelInfo
	calls      int
	blockChat  bool
	probeCalls int
	requests   []llm.ChatRequest
}

func (f *clawFakeProvider) Name() string { return "fake" }
func (f *clawFakeProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	if f.blockChat {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	f.requests = append(f.requests, req)
	content := `{"assistant_message":"What should I call you?","done":false,"updates":{}}`
	if f.calls < len(f.responses) {
		content = f.responses[f.calls]
	}
	f.calls++
	return &llm.ChatResponse{Content: content}, nil
}
func (f *clawFakeProvider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	ch := make(chan llm.ChatEvent, 1)
	close(ch)
	return ch, nil
}
func (f *clawFakeProvider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return f.models, nil
}
func (f *clawFakeProvider) ProbeModel(ctx context.Context, modelID string) (*llm.ModelInfo, error) {
	f.probeCalls++
	for _, model := range f.models {
		if model.ID == modelID {
			copy := model
			return &copy, nil
		}
	}
	return nil, nil
}
func (f *clawFakeProvider) LoadModel(ctx context.Context, modelID string, cfg llm.LoadConfig) error {
	return nil
}

// TestServiceConsumesInboundIntoMemory verifies the inbound pump end-
// to-end: a Mock channel injects a "from outside" message; the
// Service's startInboundPump goroutine drains the registry's Inbound()
// stream and lands the message in State.Memory.Events tagged by source
// channel. Without this test, a regression in the pump wiring would
// silently drop every WhatsApp message Claw should be reacting to.
func TestServiceConsumesInboundIntoMemory(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	svc := &Service{
		store:    store,
		ownerID:  "test-owner",
		channels: channels.NewRegistry(),
	}
	mock := channels.NewMock()
	svc.channels.Register(mock)
	svc.startInboundPump()
	defer svc.ShutdownInbound()

	mock.Inject(channels.Message{To: "+5215555555555", Body: "hola desde fuera"})

	// Poll for the event to land — the pump runs in its own goroutine
	// so we cannot assume synchronous arrival.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state := svc.store.Snapshot()
		for _, ev := range state.Memory.Events {
			if ev.Kind == "inbound" && ev.Channel == "mock" && ev.Text == "hola desde fuera" {
				if ev.Author != "+5215555555555" {
					t.Errorf("Author = %q, want '+5215555555555' (channel.Message.To routed as author for inbound)", ev.Author)
				}
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("inbound message did not land in memory within 2s; events=%v", svc.store.Snapshot().Memory.Events)
}

func TestServiceDreamAndCron(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	svc := &Service{
		store:   store,
		cfg:     config.ClawConfig{HeartbeatIntervalSeconds: 5, DreamIntervalMinutes: 1},
		ownerID: "test-owner",
	}
	if _, err := svc.AddInboxMessage("mock", "alice", "remember to check the repo tomorrow"); err != nil {
		t.Fatalf("AddInboxMessage: %v", err)
	}
	job, err := svc.AddCron("follow-up", "@every 1m", "send a follow-up")
	if err != nil {
		t.Fatalf("AddCron: %v", err)
	}
	if job.NextRunAt.IsZero() {
		t.Fatal("expected cron to compute next run")
	}
	result, err := svc.RunDream(context.Background(), "test")
	if err != nil {
		t.Fatalf("RunDream: %v", err)
	}
	if result.Summaries == 0 {
		t.Fatal("expected dream mode to create a summary")
	}
	if err := svc.Tick(time.Now().UTC().Add(2 * time.Minute)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	state := svc.store.Snapshot()
	if len(state.Memory.Events) < 2 {
		t.Fatalf("expected cron execution memory event, got %d events", len(state.Memory.Events))
	}
	if state.Crons[0].LastRunAt.IsZero() {
		t.Fatal("expected cron LastRunAt to be set")
	}
}

func TestInterviewUsesLLMStructuredUpdates(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Models["chat"] = "hub-chat"
	provider := &clawFakeProvider{
		models: []llm.ModelInfo{{ID: "hub-chat", LoadedContextLength: 32000, MaxContextLength: 65536}},
		responses: []string{
			`{"assistant_message":"Before anything else, what should I call you?","done":false,"updates":{"memory_summary":"Started the Claw personalization interview."}}`,
			`{"assistant_message":"What tone should I use with you most of the time?","done":false,"updates":{"user":{"display_name":"Nico"},"memory_summary":"The user wants to be called Nico."}}`,
		},
	}
	providers := llm.NewRegistry()
	providers.Register(provider)
	registry := tools.NewRegistry()
	svc := &Service{
		store:     store,
		cfg:       cfg.Claw,
		config:    cfg,
		providers: providers,
		tools:     registry,
		ownerID:   "test-owner",
	}

	first, err := svc.BeginInterview()
	if err != nil {
		t.Fatalf("BeginInterview: %v", err)
	}
	if first != "Before anything else, what should I call you?" {
		t.Fatalf("unexpected first interview message: %q", first)
	}

	next, done, err := svc.AnswerInterview("Nico")
	if err != nil {
		t.Fatalf("AnswerInterview: %v", err)
	}
	if done {
		t.Fatal("interview should not be done after first answer")
	}
	if next != "What tone should I use with you most of the time?" {
		t.Fatalf("unexpected next interview message: %q", next)
	}

	state := svc.store.Snapshot()
	if got := state.User.DisplayName; got != "Nico" {
		t.Fatalf("User.DisplayName = %q, want Nico", got)
	}
	if len(state.Interview.Transcript) < 4 {
		t.Fatalf("expected interview transcript to grow, got %d turns", len(state.Interview.Transcript))
	}
}

func TestBeginInterviewDoesNotRestartWhenClosed(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Models["chat"] = "hub-chat"
	provider := &clawFakeProvider{
		models: []llm.ModelInfo{{ID: "hub-chat"}},
		responses: []string{
			`{"assistant_message":"What should I call you?","done":false,"updates":{}}`,
			`{"assistant_message":"Done.","done":true,"updates":{"user":{"display_name":"Nico"}}}`,
			`{"assistant_message":"Let's start over. What should I call you now?","done":false,"updates":{}}`,
		},
	}
	providers := llm.NewRegistry()
	providers.Register(provider)
	svc := &Service{
		store:     store,
		cfg:       cfg.Claw,
		config:    cfg,
		providers: providers,
		tools:     tools.NewRegistry(),
		ownerID:   "test-owner",
	}

	if _, err := svc.BeginInterview(); err != nil {
		t.Fatalf("BeginInterview first: %v", err)
	}
	if _, done, err := svc.AnswerInterview("Nico"); err != nil || !done {
		t.Fatalf("AnswerInterview done=%v err=%v, want done=true err=nil", done, err)
	}
	msg, err := svc.BeginInterview()
	if err != nil {
		t.Fatalf("BeginInterview repeat: %v", err)
	}
	if msg != "Done." {
		t.Fatalf("unexpected repeated message: %q", msg)
	}
	state := svc.store.Snapshot()
	if state.Interview.Active {
		t.Fatal("expected interview to stay completed, not restart")
	}
	if state.Interview.Current != 1 {
		t.Fatalf("Interview.Current = %d, want 1", state.Interview.Current)
	}
}

func TestApplyInterviewUpdatesDoesNotOverwriteIdentityWithUserName(t *testing.T) {
	now := time.Now().UTC()
	state := &State{}
	state.Identity.Name = "Claw"

	applyInterviewUpdates(state, InterviewUpdates{
		Identity: &InterviewIdentityUpdate{Name: "Nicolas"},
		User:     &InterviewUserUpdate{DisplayName: "Nicolas"},
	}, now)

	if got := state.Identity.Name; got != "Claw" {
		t.Fatalf("Identity.Name = %q, want %q", got, "Claw")
	}
	if got := state.User.DisplayName; got != "Nicolas" {
		t.Fatalf("User.DisplayName = %q, want %q", got, "Nicolas")
	}
}

func TestAnswerInterviewContextCancelsWithoutFallback(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Models["chat"] = "hub-chat"
	provider := &clawFakeProvider{
		models:    []llm.ModelInfo{{ID: "hub-chat"}},
		blockChat: true,
	}
	providers := llm.NewRegistry()
	providers.Register(provider)
	svc := &Service{
		store:     store,
		cfg:       cfg.Claw,
		config:    cfg,
		providers: providers,
		tools:     tools.NewRegistry(),
		ownerID:   "test-owner",
	}

	if _, err := svc.BeginInterview(); err != nil {
		t.Fatalf("BeginInterview: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = svc.AnswerInterviewContext(ctx, "Spanish")
	if err != context.Canceled {
		t.Fatalf("AnswerInterviewContext err = %v, want %v", err, context.Canceled)
	}
	state := svc.store.Snapshot()
	if len(state.Interview.Transcript) == 0 {
		t.Fatal("expected user turn to remain in transcript")
	}
	last := state.Interview.Transcript[len(state.Interview.Transcript)-1]
	if last.Speaker != "user" || last.Text != "Spanish" {
		t.Fatalf("unexpected last transcript turn: %+v", last)
	}
}

func TestStatusCachesActiveModelProbe(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Models["chat"] = "hub-chat"
	provider := &clawFakeProvider{
		models: []llm.ModelInfo{{ID: "hub-chat", LoadedContextLength: 32000, MaxContextLength: 65536}},
	}
	providers := llm.NewRegistry()
	providers.Register(provider)
	svc := &Service{
		store:     store,
		cfg:       cfg.Claw,
		config:    cfg,
		providers: providers,
		tools:     tools.NewRegistry(),
		ownerID:   "test-owner",
	}

	first := svc.Status()
	second := svc.Status()
	third := svc.Status()

	if first.ActiveModel.ModelID != "hub-chat" || second.ActiveModel.ModelID != "hub-chat" || third.ActiveModel.ModelID != "hub-chat" {
		t.Fatalf("expected cached active model to stay stable: %#v %#v %#v", first.ActiveModel, second.ActiveModel, third.ActiveModel)
	}
	if provider.probeCalls != 1 {
		t.Fatalf("ProbeModel called %d times, want 1", provider.probeCalls)
	}
}

func TestChatContextAppendsConversationAfterInterview(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Models["chat"] = "hub-chat"
	provider := &clawFakeProvider{
		models: []llm.ModelInfo{{ID: "hub-chat"}},
		responses: []string{
			`{"assistant_message":"What language do you primarily use for conversation?","done":false,"updates":{}}`,
			`{"assistant_message":"Done.","done":true,"updates":{"user":{"preferences":{"preferred_language":"Spanish"}}}}`,
			`Hola, sigo aqui.`,
		},
	}
	providers := llm.NewRegistry()
	providers.Register(provider)
	svc := &Service{
		store:     store,
		cfg:       cfg.Claw,
		config:    cfg,
		providers: providers,
		tools:     tools.NewRegistry(),
		ownerID:   "test-owner",
	}

	if _, err := svc.BeginInterview(); err != nil {
		t.Fatalf("BeginInterview: %v", err)
	}
	if _, done, err := svc.AnswerInterview("Spanish"); err != nil || !done {
		t.Fatalf("AnswerInterview done=%v err=%v, want done=true err=nil", done, err)
	}
	reply, err := svc.ChatContext(context.Background(), "Sigues ahi?")
	if err != nil {
		t.Fatalf("ChatContext: %v", err)
	}
	if reply != "Hola, sigo aqui." {
		t.Fatalf("reply = %q, want %q", reply, "Hola, sigo aqui.")
	}
	state := svc.store.Snapshot()
	if state.Chat.SessionID == "" {
		t.Fatal("expected chat session id to be persisted")
	}
	if len(state.Chat.Transcript) != 2 {
		t.Fatalf("expected dedicated chat transcript with user+assistant turns, got %d", len(state.Chat.Transcript))
	}
	last := state.Chat.Transcript[len(state.Chat.Transcript)-1]
	if last.Speaker != "claw" || last.Text != "Hola, sigo aqui." {
		t.Fatalf("unexpected last transcript turn: %+v", last)
	}
	if svc.chatSession == nil {
		t.Fatal("expected chat session store to be initialized")
	}
	if !strings.Contains(svc.chatSession.ContextText(10), "Sigues ahi?") {
		t.Fatalf("expected session context to include chat turn, got:\n%s", svc.chatSession.ContextText(10))
	}
	if len(provider.requests) == 0 {
		t.Fatal("expected provider chat requests")
	}
	foundSessionContext := false
	for _, msg := range provider.requests[len(provider.requests)-1].Messages {
		if strings.Contains(msg.Content, "Forge session context:") && strings.Contains(msg.Content, "Recent timeline:") {
			foundSessionContext = true
			break
		}
	}
	if !foundSessionContext {
		t.Fatalf("expected chat prompt to include Forge session context, got messages: %+v", provider.requests[len(provider.requests)-1].Messages)
	}
}

func TestResetChatSessionPreservesInterviewAndClearsChatTranscript(t *testing.T) {
	t.Setenv("FORGE_GLOBAL_HOME", t.TempDir())
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	cfg := config.Defaults()
	cfg.Providers.Default.Name = "fake"
	cfg.Models["chat"] = "hub-chat"
	provider := &clawFakeProvider{
		models: []llm.ModelInfo{{ID: "hub-chat"}},
		responses: []string{
			`{"assistant_message":"What language do you primarily use for conversation?","done":false,"updates":{}}`,
			`{"assistant_message":"Done.","done":true,"updates":{"user":{"preferences":{"preferred_language":"Spanish"}}}}`,
			`Hola.`,
		},
	}
	providers := llm.NewRegistry()
	providers.Register(provider)
	svc := &Service{
		store:     store,
		cfg:       cfg.Claw,
		config:    cfg,
		providers: providers,
		tools:     tools.NewRegistry(),
		ownerID:   "test-owner",
	}

	if _, err := svc.BeginInterview(); err != nil {
		t.Fatalf("BeginInterview: %v", err)
	}
	if _, done, err := svc.AnswerInterview("Spanish"); err != nil || !done {
		t.Fatalf("AnswerInterview done=%v err=%v, want done=true err=nil", done, err)
	}
	if _, err := svc.ChatContext(context.Background(), "Sigues ahi?"); err != nil {
		t.Fatalf("ChatContext: %v", err)
	}
	before := svc.store.Snapshot()
	if before.Chat.SessionID == "" {
		t.Fatal("expected chat session before reset")
	}
	if len(before.Chat.Transcript) == 0 {
		t.Fatal("expected chat transcript before reset")
	}
	if before.Interview.CompletedAt.IsZero() {
		t.Fatal("expected completed interview before reset")
	}

	newSessionID, err := svc.ResetChatSession()
	if err != nil {
		t.Fatalf("ResetChatSession: %v", err)
	}
	after := svc.store.Snapshot()
	if newSessionID == before.Chat.SessionID {
		t.Fatalf("expected new session id after reset, got %q", newSessionID)
	}
	if after.Chat.SessionID != newSessionID {
		t.Fatalf("state chat session id = %q, want %q", after.Chat.SessionID, newSessionID)
	}
	if len(after.Chat.Transcript) != 0 {
		t.Fatalf("expected chat transcript cleared after reset, got %d turns", len(after.Chat.Transcript))
	}
	if after.Interview.CompletedAt.IsZero() {
		t.Fatal("expected interview completion to survive reset")
	}
	if len(after.Interview.Transcript) == 0 {
		t.Fatal("expected interview transcript to survive reset")
	}
}
