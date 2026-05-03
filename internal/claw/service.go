package claw

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"forge/internal/claw/channels"
	"forge/internal/config"
	contextbuilder "forge/internal/context"
	"forge/internal/globalconfig"
	"forge/internal/llm"
	"forge/internal/session"
	"forge/internal/tools"
	"forge/internal/yarn"
)

var (
	servicesMu sync.Mutex
	services   = map[string]*Service{}
)

// activeModelCacheTTL bounds how often Status() is allowed to hit
// /v1/models or /api/v0/models on the LLM provider. The Hub status bar
// re-renders every 200ms, so a short TTL turned into a steady stream
// of probe requests in the LM Studio log. The loaded model rarely
// changes mid-session — bumping the TTL to 30 minutes makes the probe
// effectively on-demand without losing freshness for routine use.
const activeModelCacheTTL = 30 * time.Minute

type Service struct {
	mu        sync.RWMutex
	store     *Store
	cfg       config.ClawConfig
	config    config.Config
	providers *llm.Registry
	tools     *tools.Registry
	channels  *channels.Registry
	ownerID   string
	stopCh    chan struct{}
	doneCh    chan struct{}

	// inboundCancel stops the goroutine that pumps every registered
	// channel's inbound stream into Claw's MemoryEvents. Set on first
	// SyncRuntime, cleared on Close. Nil-safe.
	inboundCancel context.CancelFunc

	// reminderCancel stops the reminder dispatch goroutine started by
	// startReminderPump. Same idempotency rules as inboundCancel.
	reminderCancel context.CancelFunc

	chatRoot    string
	chatSession *session.Store
	chatBuilder *contextbuilder.Builder

	// factsYarn is the Claw-global yarn store used to mirror Memory.Facts
	// as searchable nodes. Lives at ~/.forge/yarn-claw/nodes.jsonl —
	// independent of any workspace's .forge/yarn so facts persist even
	// when the user is in Hub mode (no workspace open). Nil-safe: all
	// callers test before using so a fresh-install state survives if
	// the home dir is read-only.
	factsYarn *yarn.Store

	activeModelCache    ActiveModel
	activeModelCachedAt time.Time

	// Per-turn telemetry surfaced to the Hub status bar. Updated by
	// every interview/chat reply path. Token counts are estimates
	// derived from prompt content length (4-chars-per-token heuristic)
	// because the OpenAI-compatible non-streaming Chat() doesn't decode
	// `usage`. Cheap and good enough for a "you're at 30% of the
	// window" indicator.
	lastModelUsed   string
	lastTokensUsed  int
	lastTokensTotal int // model context window if we know it, else 0
	lastMode        string
}

type InterviewQuestion struct {
	Key    string
	Prompt string
}

var interviewQuestions = []InterviewQuestion{
	{Key: "user_name", Prompt: "First, how should I call you day to day?"},
	{Key: "claw_name", Prompt: "What name should I use for myself when I talk to you?"},
	{Key: "tone", Prompt: "What tone should I default to with you? Direct, playful, formal, intense, calm, something else?"},
	{Key: "values", Prompt: "What should I protect or optimize for first when I act on your behalf?"},
	{Key: "routines", Prompt: "What routines, reminders, or follow-ups would actually make me useful to you?"},
	{Key: "timezone", Prompt: "What timezone and schedule do you live in?"},
	{Key: "day_one", Prompt: "What is one thing I should remember about you from day one and not forget?"},
}

func Open(cfg config.Config, providers *llm.Registry, registry *tools.Registry) (*Service, error) {
	path := filepath.Join(globalconfig.HomeDir(), "claw", "state.json")
	servicesMu.Lock()
	defer servicesMu.Unlock()
	if existing := services[path]; existing != nil {
		existing.SyncRuntime(cfg, providers, registry)
		if _, _, err := existing.ensureChatRuntime(); err != nil {
			return nil, err
		}
		return existing, nil
	}
	store, err := OpenStore(path)
	if err != nil {
		return nil, err
	}
	svc := &Service{
		store:     store,
		ownerID:   newID(),
		channels:  channels.NewRegistry(),
		factsYarn: yarn.NewAtPath(filepath.Join(globalconfig.HomeDir(), "yarn-claw")),
	}
	// Mock is always present so /claw inbox + tests have somewhere to
	// land. Real backends (whatsapp, slack) are registered later via
	// RegisterChannel as the user pairs them.
	svc.channels.Register(channels.NewMock())
	_ = store.Update(func(state *State) error {
		if state.Heartbeat.Running {
			state.Heartbeat.Running = false
			state.Heartbeat.Status = "stopped"
		}
		return nil
	})
	svc.SyncRuntime(cfg, providers, registry)
	if _, _, err := svc.ensureChatRuntime(); err != nil {
		return nil, err
	}
	svc.startInboundPump()
	svc.startReminderPump()
	svc.backfillYarnFromState()
	// Sanity-migrate state that earlier (buggier) interview turns may
	// have corrupted. Specifically: Identity.Name accidentally set to
	// a language label by a small model. Fixes existing state in
	// place without requiring a wipe.
	migrated := false
	_ = svc.store.Update(func(state *State) error {
		if migrateCorruptIdentityName(state) {
			migrated = true
		}
		return nil
	})
	if migrated {
		fmt.Fprintf(os.Stderr, "[claw] migrated corrupt Identity.Name back to default\n")
	}
	if err := bootstrapClawWorkspace(svc.store.Snapshot()); err != nil {
		fmt.Fprintf(os.Stderr, "[claw] workspace bootstrap failed: %v\n", err)
	}
	// If migration ran and the workspace folder already exists,
	// regenerate the IDENTITY/SOUL/USER markdown files so the .md
	// layer reflects the migrated state.
	if migrated {
		regenerateClawWorkspaceFiles(svc.store.Snapshot())
	}
	if cfg.Claw.Enabled && cfg.Claw.Autostart {
		_ = svc.Start()
	}
	services[path] = svc
	return svc, nil
}

func clawChatRootDir() string {
	return filepath.Join(globalconfig.HomeDir(), "hub", "claw")
}

func (s *Service) ensureChatRuntime() (*session.Store, *contextbuilder.Builder, error) {
	if s == nil {
		return nil, nil, fmt.Errorf("claw service is nil")
	}
	root := clawChatRootDir()
	snapshot := s.store.Snapshot()
	sessionID := strings.TrimSpace(snapshot.Chat.SessionID)

	s.mu.RLock()
	cfg := s.config
	registry := s.tools
	currentRoot := s.chatRoot
	currentSession := s.chatSession
	s.mu.RUnlock()

	var chatSession *session.Store
	if currentSession != nil && currentRoot == root && currentSession.ID() == sessionID && sessionID != "" {
		chatSession = currentSession
	}
	if chatSession == nil && sessionID != "" {
		if reopened, err := session.Open(root, sessionID); err == nil {
			chatSession = reopened
		}
	}
	if chatSession == nil {
		if latest, err := session.OpenLatest(root); err == nil {
			chatSession = latest
		}
	}
	if chatSession == nil {
		fresh, err := session.New(root)
		if err != nil {
			return nil, nil, err
		}
		chatSession = fresh
	}
	if snapshot.Chat.SessionID != chatSession.ID() {
		if err := s.store.Update(func(state *State) error {
			state.Chat.SessionID = chatSession.ID()
			return nil
		}); err != nil {
			return nil, nil, err
		}
	}

	builder := contextbuilder.NewBuilder(root, cfg, registry)
	builder.History = chatSession

	s.mu.Lock()
	s.chatRoot = root
	s.chatSession = chatSession
	s.chatBuilder = builder
	s.mu.Unlock()
	return chatSession, builder, nil
}

func clearClawChatYarn(root string) error {
	yarnDir := filepath.Join(root, ".forge", "yarn")
	if err := os.RemoveAll(yarnDir); err != nil {
		return err
	}
	return nil
}

// startInboundPump spins a goroutine that drains the channel registry's
// fan-in inbox into Claw's MemoryEvents. Each inbound message becomes
// one "inbound" event tagged by its originating channel (mock,
// whatsapp, ...) so /claw memory and the Soul/Memory submenu show the
// activity automatically.
//
// Idempotent: repeat calls are no-ops while a pump is already running.
// Cancelled by ShutdownInbound (or service close).
func (s *Service) startInboundPump() {
	if s == nil || s.channels == nil {
		return
	}
	s.mu.Lock()
	if s.inboundCancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.inboundCancel = cancel
	inbound := s.channels.Inbound()
	s.mu.Unlock()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-inbound:
				if !ok {
					return
				}
				source := msg.Source
				if source == "" {
					source = "unknown"
				}
				fmt.Fprintf(os.Stderr, "[claw] inbound pump: channel=%s from=%s isGroup=%v body=%q\n", source, msg.To, msg.IsGroup, trimText(msg.Body, 80))
				// msg.To is the recipient JID for outbound, the sender
				// JID for inbound (see WhatsApp.handleEvent: it puts
				// evt.Info.Sender into the To field for inbound). Use
				// it as the author so /claw memory shows who wrote in.
				_, _ = s.AddInboxMessage(source, msg.To, msg.Body)
				// Auto-reply only for direct messages — chiming into
				// every group message would spam contacts and risk
				// account flags. Run in a fresh goroutine so a slow
				// LLM turn doesn't back up the inbox queue.
				if msg.IsGroup {
					continue
				}
				go func(channel, sender, body string) {
					// Slow local LLMs (qwen ~35b on consumer hardware
					// via LM Studio) routinely take 60-120s for a
					// chat completion. 3 minutes is the bound where
					// "the model is genuinely thinking" gives way to
					// "something is stuck and we should give up".
					replyCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
					defer cancel()
					if _, err := s.AutoReplyToInbound(replyCtx, channel, sender, body); err != nil {
						_ = s.store.Update(func(state *State) error {
							channelState := state.Channels.Items[channel]
							channelState.Name = channel
							channelState.LastError = "auto-reply failed: " + err.Error()
							state.Channels.Items[channel] = channelState
							return nil
						})
					}
				}(source, msg.To, msg.Body)
			}
		}
	}()
}

// ShutdownInbound stops the inbound pump goroutine. Safe to call from
// any goroutine; idempotent (a second call is a no-op). Also stops
// the reminder pump for symmetry — both pumps are bound to the
// service lifecycle.
func (s *Service) ShutdownInbound() {
	if s == nil {
		return
	}
	s.mu.Lock()
	inbound := s.inboundCancel
	reminder := s.reminderCancel
	s.inboundCancel = nil
	s.reminderCancel = nil
	s.mu.Unlock()
	if inbound != nil {
		inbound()
	}
	if reminder != nil {
		reminder()
	}
}

func (s *Service) SyncRuntime(cfg config.Config, providers *llm.Registry, registry *tools.Registry) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.config = cfg
	s.cfg = cfg.Claw
	s.providers = providers
	s.tools = registry
	s.activeModelCache = ActiveModel{}
	s.activeModelCachedAt = time.Time{}
	s.mu.Unlock()
	_ = s.store.Update(func(state *State) error {
		applyConfigDefaults(state, cfg.Claw)
		state.Tools.Registered = toolDescriptors(registry)
		state.Tools.UpdatedAt = time.Now().UTC()
		return nil
	})
}

func (s *Service) Start() error {
	if s == nil {
		return fmt.Errorf("claw service is nil")
	}
	s.mu.Lock()
	if s.stopCh != nil {
		s.mu.Unlock()
		return nil
	}
	interval := time.Duration(max(5, s.cfg.HeartbeatIntervalSeconds)) * time.Second
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	ownerID := s.ownerID
	s.mu.Unlock()

	if err := s.store.Update(func(state *State) error {
		now := time.Now().UTC()
		state.Enabled = true
		state.Heartbeat.Running = true
		state.Heartbeat.Status = "running"
		state.Heartbeat.OwnerID = ownerID
		state.Heartbeat.LastStartedAt = now
		state.Heartbeat.LastBeatAt = now
		state.Heartbeat.LastError = ""
		return nil
	}); err != nil {
		return err
	}

	go s.loop(interval)
	return nil
}

func (s *Service) loop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(s.doneCh)
	for {
		select {
		case <-ticker.C:
			now := time.Now().UTC()
			fired, _ := s.tickAndCollect(now)
			for _, job := range fired {
				go s.runCronJob(job)
			}
		case <-s.stopCh:
			return
		}
	}
}

// runCronJob dispatches one fired cron's prompt as a Claw chat call so it
// can use tools (search, send messages, remember facts, etc). Output is
// stored on the job's LastResult and as an "agent" MemoryEvent — it does
// NOT pollute state.Chat.Transcript (that's for human-driven turns).
//
// The function is fire-and-forget: errors are recorded on the job, never
// propagated. Each cron gets its own context with a generous timeout so a
// stuck call can't tie up the heartbeat goroutine forever.
func (s *Service) runCronJob(job CronJob) {
	if s == nil {
		return
	}
	prompt := strings.TrimSpace(job.Prompt)
	if prompt == "" {
		return
	}
	timeout := interviewTimeout(s.config)
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, builder, err := s.ensureChatRuntime()
	if err != nil {
		s.recordCronOutcome(job.ID, "", err.Error())
		return
	}
	state := s.store.Snapshot()
	header := "[scheduled cron '" + job.Name + "'] "
	reply, err := s.generateChatReplyContextExt(ctx, state, header+prompt, builder, "You are running as a scheduled cron job, not a live chat. Take any user-facing action the prompt requests (e.g. send a WhatsApp ping, look something up online, append a note) and return a one-line confirmation summarising what you did.", true)
	if err != nil {
		s.recordCronOutcome(job.ID, "", err.Error())
		return
	}
	s.recordCronOutcome(job.ID, strings.TrimSpace(reply), "")
}

// recordCronOutcome stamps a cron's LastResult / LastError + appends a
// completion MemoryEvent so the dream consolidator and the user-facing
// status panel can see what happened.
func (s *Service) recordCronOutcome(id, result, errMsg string) {
	if s == nil || strings.TrimSpace(id) == "" {
		return
	}
	_ = s.store.Update(func(state *State) error {
		now := time.Now().UTC()
		for i := range state.Crons {
			if state.Crons[i].ID != id {
				continue
			}
			job := &state.Crons[i]
			if errMsg != "" {
				job.LastError = errMsg
				job.LastResult = "error"
			} else {
				job.LastError = ""
				if result != "" {
					job.LastResult = trimText(result, 240)
				} else {
					job.LastResult = "ok"
				}
			}
			body := job.Name
			if errMsg != "" {
				body += ": error: " + errMsg
			} else if result != "" {
				body += ": " + trimText(result, 200)
			}
			state.Memory.Events = append(state.Memory.Events, MemoryEvent{
				ID:        newID(),
				Kind:      "cron_result",
				Channel:   "system",
				Author:    "claw",
				Text:      body,
				CreatedAt: now,
			})
			break
		}
		compactState(state)
		return nil
	})
}

func (s *Service) Stop() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	stopCh := s.stopCh
	doneCh := s.doneCh
	s.stopCh = nil
	s.doneCh = nil
	s.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
		if doneCh != nil {
			<-doneCh
		}
	}
	return s.store.Update(func(state *State) error {
		state.Heartbeat.Running = false
		state.Heartbeat.Status = "stopped"
		state.Heartbeat.LastStoppedAt = time.Now().UTC()
		return nil
	})
}

func (s *Service) Status() Status {
	state := s.store.Snapshot()
	s.mu.RLock()
	telemetry := struct {
		model  string
		used   int
		total  int
		mode   string
	}{
		model: s.lastModelUsed,
		used:  s.lastTokensUsed,
		total: s.lastTokensTotal,
		mode:  s.lastMode,
	}
	s.mu.RUnlock()
	return Status{
		State:           state,
		StorePath:       s.store.Path(),
		ActiveModel:     s.activeModel(),
		InboxCount:      len(state.Memory.Events),
		LastModelUsed:   telemetry.model,
		LastTokensUsed:  telemetry.used,
		LastTokensTotal: telemetry.total,
		LastMode:        telemetry.mode,
	}
}

// recordLLMTurn captures per-turn telemetry the Hub status bar shows.
// Estimates token usage from prompt content length (4 chars ≈ 1 token —
// a rough but well-known approximation that's fine for a "how full is
// the window" indicator). The provider's non-streaming Chat() path
// doesn't decode `usage` today, so estimating client-side avoids a
// deeper LLM-layer change just to power a status line.
func (s *Service) recordLLMTurn(modelID, mode string, msgs []llm.Message) {
	if s == nil {
		return
	}
	chars := 0
	for _, msg := range msgs {
		chars += len(msg.Content)
	}
	estimated := chars / 4
	s.mu.Lock()
	s.lastModelUsed = modelID
	s.lastTokensUsed = estimated
	s.lastMode = mode
	if active := s.activeModelCache; active.LoadedContextLength > 0 {
		s.lastTokensTotal = active.LoadedContextLength
	} else if active.MaxContextLength > 0 {
		s.lastTokensTotal = active.MaxContextLength
	}
	s.mu.Unlock()
}

// RegisterChannel adds a transport (whatsapp, telegram, etc.) to the
// service's channel registry and reflects the new channel in the
// persistent State.Channels.Items map so the Hub UI can show it without
// the user having to refresh.
//
// Calling RegisterChannel for an already-registered name replaces the
// previous instance — useful when the user re-pairs WhatsApp.
func (s *Service) RegisterChannel(ch channels.Channel) {
	if s == nil || ch == nil {
		return
	}
	s.mu.Lock()
	if s.channels == nil {
		s.channels = channels.NewRegistry()
	}
	s.channels.Register(ch)
	s.mu.Unlock()
	_ = s.store.Update(func(state *State) error {
		if state.Channels.Items == nil {
			state.Channels.Items = map[string]Channel{}
		}
		live := ch.Status()
		current := state.Channels.Items[ch.Name()]
		current.Name = ch.Name()
		current.Provider = ch.Provider()
		current.Enabled = live.Connected
		current.AccountID = live.AccountID
		current.AccountName = live.AccountName
		if !live.PairedAt.IsZero() {
			current.PairedAt = live.PairedAt
		}
		// Always pre-allow the paired account itself so the owner can
		// never lock themselves out by enabling strict mode. This is a
		// no-op once the JID is already in the allowlist.
		if live.AccountID != "" {
			current.Allowlist = appendUniqueJID(current.Allowlist, live.AccountID)
		}
		state.Channels.Items[ch.Name()] = current
		if state.Channels.Default == "" {
			state.Channels.Default = ch.Name()
		}
		return nil
	})
}

// appendUniqueJID adds jid to allowlist iff not already present.
// Comparison is case-insensitive trimmed.
func appendUniqueJID(list []string, jid string) []string {
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return list
	}
	needle := strings.ToLower(jid)
	for _, existing := range list {
		if strings.ToLower(strings.TrimSpace(existing)) == needle {
			return list
		}
	}
	return append(list, jid)
}

// AutoReplyToInbound generates a Claw reply to a message that arrived
// on an external channel and sends it back through the same transport.
// Gating:
//
//   - Skipped for empty bodies (fan-in already filtered, but defensive).
//   - Caller is responsible for skipping group chats (Message.IsGroup).
//
// Reply is generated regardless of interview state. Mid-interview
// replies use whatever identity/soul defaults exist; once the interview
// completes the persona becomes richer. Gating on Interview.Active is
// counterproductive — most users pair channels well before they finish
// the onboarding flow, and we still want them to be able to test.
//
// On a non-empty Claw response the reply travels through SendVia, which
// still applies the channel's anti-ban guardrails (random delay,
// composing presence, rate limit, first-contact link guard). Errors
// surface to the caller; nil-error + empty reply means "Claw chose not
// to answer this turn".
func (s *Service) AutoReplyToInbound(ctx context.Context, channelName, sender, message string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("claw service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return "", nil
	}
	fmt.Fprintf(os.Stderr, "[claw] auto-reply: starting for channel=%s sender=%s msglen=%d\n", channelName, sender, len(message))

	// Allowlist gate: when the channel is in strict mode and the
	// sender isn't approved, send a localised rejection and short-
	// circuit. The LLM is never invoked, no thread is persisted, no
	// tokens are spent — just a one-shot polite no.
	gateState := s.store.Snapshot()
	if !channelAllowsJID(gateState, channelName, sender) {
		fmt.Fprintf(os.Stderr, "[claw] auto-reply: sender %s not in allowlist for %s; rejecting\n", sender, channelName)
		reply := rejectionMessage(gateState)
		out, err := s.SendVia(ctx, channelName, channels.Message{To: sender, Body: reply})
		if err != nil {
			return reply, fmt.Errorf("auto-reply: send rejection: %w", err)
		}
		return out.Body, nil
	}

	// Slash commands bypass the LLM AND thread persistence. They run
	// immediately, send their reply, and return — operator chatter
	// shouldn't pollute the conversation history.
	if reply, handled := s.processChannelSlashCommand(channelName, sender, message); handled {
		fmt.Fprintf(os.Stderr, "[claw] auto-reply: slash command handled, sending %d chars\n", len(reply))
		out, err := s.SendVia(ctx, channelName, channels.Message{To: sender, Body: reply})
		if err != nil {
			fmt.Fprintf(os.Stderr, "[claw] auto-reply: slash SendVia failed: %v\n", err)
			return reply, fmt.Errorf("auto-reply: send slash reply: %w", err)
		}
		return out.Body, nil
	}

	if _, _, err := s.ensureChatRuntime(); err != nil {
		fmt.Fprintf(os.Stderr, "[claw] auto-reply: ensureChatRuntime failed: %v\n", err)
		return "", fmt.Errorf("auto-reply: ensure chat runtime: %w", err)
	}
	// Persist the inbound user turn FIRST so the snapshot we read for
	// generation already includes it as the latest transcript entry.
	// This is what gives the contact a real session: each new message
	// builds on the prior turns persisted in
	// state.Channels.Items[channel].Threads[sender].
	var droppedFromUser []InterviewTurn
	if err := s.store.Update(func(state *State) error {
		droppedFromUser = appendChannelThreadTurn(state, channelName, sender, "user", message)
		return nil
	}); err != nil {
		return "", fmt.Errorf("auto-reply: persist inbound turn: %w", err)
	}
	// If this user turn pushed the thread over the cap, summarize the
	// dropped batch synchronously so the next reply has the gist of
	// the conversation that's about to fall out of the window.
	if len(droppedFromUser) > 0 {
		s.summarizeAndPersistThreadTurns(ctx, channelName, sender, droppedFromUser)
	}
	snapshot := s.store.Snapshot()

	// Per-contact transcript replaces the local TUI chat transcript on
	// the snapshot copy. State is passed by value so the persisted
	// Chat.Transcript stays intact for the local Hub → Claw chat.
	snapshot.Chat.Transcript = channelThread(snapshot, channelName, sender)
	fmt.Fprintf(os.Stderr, "[claw] auto-reply: thread for %s has %d turns\n", sender, len(snapshot.Chat.Transcript))

	// systemExtra teaches the LLM that this conversation runs over
	// WhatsApp: short, conversational, no markdown/code/tools. Builder
	// is nil so Forge workspace context (cwd, files, git) doesn't leak
	// into the contact reply.
	systemExtra := "You are now replying via " + channelName + " to one of your contacts. " +
		"Treat this conversation as a phone chat: short, friendly, conversational replies — " +
		"one or two sentences, no markdown, no code blocks, no tool calls unless the contact " +
		"explicitly asks for something that requires one. Use the contact's prior turns in this " +
		"thread to stay on topic; don't re-introduce yourself if you've already greeted them."
	if priorSummary := latestThreadSummary(snapshot, channelName, sender); priorSummary != "" {
		// Inject the gist of older turns (already trimmed out of the
		// transcript window) so the model has continuity beyond the
		// 30-turn slice it sees raw.
		systemExtra += "\n\nEarlier in this conversation: " + priorSummary
	}
	// Owner messaging from any of their linked devices counts as
	// main-session — load MEMORY.md so personal context is available.
	// Non-owner contacts get a sanitised prompt without MEMORY.
	includeMemory := isChannelOwner(snapshot, channelName, sender)
	reply, err := s.generateChatReplyContextExt(ctx, snapshot, message, nil, systemExtra, includeMemory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[claw] auto-reply: generateChatReply failed: %v\n", err)
		return "", fmt.Errorf("auto-reply: generate: %w", err)
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		fmt.Fprintf(os.Stderr, "[claw] auto-reply: generator returned empty reply\n")
		return "", nil
	}
	fmt.Fprintf(os.Stderr, "[claw] auto-reply: generated %d chars, sending via %s\n", len(reply), channelName)

	// Persist Claw's reply BEFORE sending so we never lose the turn
	// even if SendVia fails downstream — the next message will see the
	// reply in history and Claw won't repeat itself.
	var droppedFromClaw []InterviewTurn
	if err := s.store.Update(func(state *State) error {
		droppedFromClaw = appendChannelThreadTurn(state, channelName, sender, "claw", reply)
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "[claw] auto-reply: persist claw turn failed: %v\n", err)
	}
	if len(droppedFromClaw) > 0 {
		// Background-summarize: don't block the SendVia path. The
		// resulting summary lands in state for the *next* reply.
		go s.summarizeAndPersistThreadTurns(context.Background(), channelName, sender, droppedFromClaw)
	}

	out, err := s.SendVia(ctx, channelName, channels.Message{To: sender, Body: reply})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[claw] auto-reply: SendVia failed: %v\n", err)
		return reply, fmt.Errorf("auto-reply: send: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[claw] auto-reply: sent ok\n")
	return out.Body, nil
}

// AppendWorkspaceNote lets Claw mid-conversation drop a one-line
// observation into one of its markdown files. The LLM picks the file
// (MEMORY.md / SOUL.md / USER.md / TOOLS.md / IDENTITY.md) and the
// note text; we normalise + dedupe + append. Used by the
// claw_workspace_note tool so Claw can edit its own brain as the
// conversation progresses without going through the interview path.
//
// AGENTS.md and HEARTBEAT.md are intentionally not writable from this
// path — AGENTS is the constitution (operator-edited only) and
// HEARTBEAT is the operator's checklist.
func (s *Service) AppendWorkspaceNote(file, note string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("claw service is nil")
	}
	canonical := canonicalWorkspaceFileName(file)
	if canonical == "" {
		return "", fmt.Errorf("file must be one of MEMORY.md, SOUL.md, USER.md, TOOLS.md, IDENTITY.md (got %q)", file)
	}
	note = strings.TrimSpace(note)
	if note == "" {
		return "", fmt.Errorf("note text is required")
	}
	if err := appendToWorkspaceFile(canonical, note); err != nil {
		return "", err
	}
	// Audit the edit so it shows up in /claw memory and the dream
	// summary catches recurring patterns.
	_ = s.store.Update(func(state *State) error {
		state.Memory.Events = append(state.Memory.Events, MemoryEvent{
			ID:        newID(),
			Kind:      "workspace_note",
			Channel:   "claw",
			Author:    "claw_workspace_note",
			Text:      canonical + ": " + note,
			CreatedAt: time.Now().UTC(),
		})
		return nil
	})
	return canonical, nil
}

// SaveContact persists a contact entry into State.Contacts. Name is
// the only required field; phone/email/notes are stored as-given. The
// canonical key is the lowercased trimmed name so re-saves under
// different casing update the same record. Source identifies who
// triggered the save (tool name, "interview", etc.) for auditability.
//
// If a contact with the same canonical name already exists, fields
// passed empty leave the existing value alone — partial updates work.
// Pass " " (or any non-empty whitespace) to explicitly clear a field.
func (s *Service) SaveContact(name, phone, email, notes, source string) (Contact, error) {
	if s == nil {
		return Contact{}, fmt.Errorf("claw service is nil")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Contact{}, fmt.Errorf("contact name is required")
	}
	key := strings.ToLower(name)
	var saved Contact
	err := s.store.Update(func(state *State) error {
		now := time.Now().UTC()
		if state.Contacts == nil {
			state.Contacts = map[string]Contact{}
		}
		existing, exists := state.Contacts[key]
		if !exists {
			existing = Contact{Name: name, CreatedAt: now}
		} else if existing.Name == "" {
			existing.Name = name
		}
		if v := strings.TrimSpace(phone); v != "" {
			existing.Phone = v
		}
		if v := strings.TrimSpace(email); v != "" {
			existing.Email = v
		}
		if v := strings.TrimSpace(notes); v != "" {
			existing.Notes = v
		}
		if v := strings.TrimSpace(source); v != "" {
			existing.Source = v
		}
		existing.UpdatedAt = now
		state.Contacts[key] = existing
		state.Memory.Events = append(state.Memory.Events, MemoryEvent{
			ID:        newID(),
			Kind:      "contact_saved",
			Channel:   "claw",
			Author:    source,
			Text:      fmt.Sprintf("Saved contact %s (phone=%s email=%s)", existing.Name, existing.Phone, existing.Email),
			CreatedAt: now,
		})
		saved = existing
		return nil
	})
	return saved, err
}

// LookupContact returns the contact matching the given name. Match is
// case-insensitive exact on the canonical name. If no exact match,
// falls back to a substring scan so "sebas" finds "Sebastián". Returns
// (Contact{}, false) when no match is found.
func (s *Service) LookupContact(name string) (Contact, bool) {
	if s == nil {
		return Contact{}, false
	}
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return Contact{}, false
	}
	snapshot := s.store.Snapshot()
	if c, ok := snapshot.Contacts[name]; ok {
		return c, true
	}
	for key, c := range snapshot.Contacts {
		if strings.Contains(key, name) {
			return c, true
		}
	}
	return Contact{}, false
}

// RememberFact appends a fact to Memory.Facts. Idempotent on exact
// text match (case-insensitive) — re-saying the same fact updates the
// CreatedAt without duplicating the entry. Subject is optional; when
// present it acts as a tag for later filtered recall.
func (s *Service) RememberFact(text, subject, source string) (Fact, error) {
	if s == nil {
		return Fact{}, fmt.Errorf("claw service is nil")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Fact{}, fmt.Errorf("fact text is required")
	}
	subject = strings.TrimSpace(subject)
	source = strings.TrimSpace(source)
	var saved Fact
	err := s.store.Update(func(state *State) error {
		now := time.Now().UTC()
		needle := strings.ToLower(text)
		for i, existing := range state.Memory.Facts {
			if strings.ToLower(existing.Text) == needle {
				state.Memory.Facts[i].CreatedAt = now
				if subject != "" {
					state.Memory.Facts[i].Subject = subject
				}
				if source != "" {
					state.Memory.Facts[i].Source = source
				}
				saved = state.Memory.Facts[i]
				return nil
			}
		}
		fact := Fact{
			ID:        newID(),
			Text:      text,
			Subject:   subject,
			Source:    source,
			CreatedAt: now,
		}
		state.Memory.Facts = append(state.Memory.Facts, fact)
		state.Memory.Events = append(state.Memory.Events, MemoryEvent{
			ID:        newID(),
			Kind:      "fact_remembered",
			Channel:   "claw",
			Author:    source,
			Text:      "Remembered: " + text,
			CreatedAt: now,
		})
		saved = fact
		return nil
	})
	if err == nil {
		s.mirrorFactToYarn(saved)
		// Append to MEMORY.md too so the human can read what Claw
		// has remembered without grep-ing state.json. Skips silently
		// when workspace isn't bootstrapped.
		line := saved.Text
		if saved.Subject != "" {
			line += " _(subject: " + saved.Subject + ")_"
		}
		line = saved.CreatedAt.Local().Format("2006-01-02") + " — " + line
		if appendErr := appendToWorkspaceFile("MEMORY.md", line); appendErr != nil {
			fmt.Fprintf(os.Stderr, "[claw] MEMORY.md append failed: %v\n", appendErr)
		}
	}
	return saved, err
}

// backfillYarnFromState mirrors any pre-existing facts into the yarn
// store. Runs on every Open but the yarn Upsert is idempotent
// (stableID == "fact:<ID>" so re-runs replace in place). Cheap when
// the store already has them.
func (s *Service) backfillYarnFromState() {
	if s == nil || s.factsYarn == nil {
		return
	}
	snapshot := s.store.Snapshot()
	if len(snapshot.Memory.Facts) == 0 {
		return
	}
	// Skip the work if yarn already has the same number of fact-kind
	// nodes — saves rewriting nodes.jsonl on every boot once steady.
	existing, err := s.factsYarn.Load()
	if err == nil {
		factCount := 0
		for _, n := range existing {
			if n.Kind == "fact" {
				factCount++
			}
		}
		if factCount >= len(snapshot.Memory.Facts) {
			return
		}
	}
	for _, f := range snapshot.Memory.Facts {
		s.mirrorFactToYarn(f)
	}
}

// mirrorFactToYarn upserts the fact into the Claw-global yarn store so
// it shows up in the graph viewer and benefits from yarn's keyword
// scoring. Best-effort — yarn write failures are logged but never
// propagate, since state.Memory.Facts is the source of truth.
func (s *Service) mirrorFactToYarn(f Fact) {
	if s == nil || s.factsYarn == nil {
		return
	}
	node := yarn.Node{
		Kind:    "fact",
		Path:    "fact:" + f.ID,
		Summary: f.Subject,
		Content: f.Text,
	}
	if err := s.factsYarn.Upsert(node); err != nil {
		fmt.Fprintf(os.Stderr, "[claw] yarn mirror failed for fact %s: %v\n", f.ID, err)
	}
}

// RecallFacts searches Memory.Facts for entries whose text or subject
// contains the query. Tries the yarn-claw store first (keyword scoring
// + budget) and falls back to a substring scan over state when yarn
// is empty/unavailable. Empty query returns the most-recent
// maxResults facts. maxResults<=0 defaults to 10.
func (s *Service) RecallFacts(query string, maxResults int) []Fact {
	if s == nil {
		return nil
	}
	if maxResults <= 0 {
		maxResults = 10
	}
	// Try yarn first when there's a query — yarn returns scoring-
	// ranked nodes, which beats a plain substring scan once the fact
	// store grows. Empty query short-circuits to the in-memory path
	// because yarn's selector biases against zero-term queries.
	if needle := strings.TrimSpace(query); needle != "" && s.factsYarn != nil {
		if nodes, err := s.factsYarn.Select(needle, 8000, maxResults); err == nil && len(nodes) > 0 {
			snapshot := s.store.Snapshot()
			byID := map[string]Fact{}
			for _, f := range snapshot.Memory.Facts {
				byID[f.ID] = f
			}
			out := make([]Fact, 0, len(nodes))
			for _, n := range nodes {
				if n.Kind != "fact" {
					continue
				}
				id := strings.TrimPrefix(n.Path, "fact:")
				if f, ok := byID[id]; ok {
					out = append(out, f)
					continue
				}
				// Yarn has the node but state forgot — surface the
				// node's own content so the caller still gets an
				// answer instead of a silent miss.
				out = append(out, Fact{
					ID:      id,
					Text:    n.Content,
					Subject: n.Summary,
				})
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	// Fallback: in-memory substring scan.
	snapshot := s.store.Snapshot()
	needle := strings.ToLower(strings.TrimSpace(query))
	out := make([]Fact, 0, maxResults)
	for i := len(snapshot.Memory.Facts) - 1; i >= 0; i-- {
		f := snapshot.Memory.Facts[i]
		if needle != "" {
			hay := strings.ToLower(f.Text + " " + f.Subject)
			if !strings.Contains(hay, needle) {
				continue
			}
		}
		out = append(out, f)
		if len(out) >= maxResults {
			break
		}
	}
	return out
}

// ScheduleReminder persists a Reminder for future delivery. RemindAt
// must be in the future (or "now-ish" — the pump fires anything not
// yet sent regardless of past-dueness, so a missed reminder still
// goes out on the next tick after a forge restart). Body is what the
// channel will send; channelName + target tell the pump where to send
// it. Returns the persisted reminder so callers can echo the ID.
func (s *Service) ScheduleReminder(remindAt time.Time, body, channelName, target string) (Reminder, error) {
	if s == nil {
		return Reminder{}, fmt.Errorf("claw service is nil")
	}
	body = strings.TrimSpace(body)
	channelName = strings.TrimSpace(channelName)
	target = strings.TrimSpace(target)
	if body == "" {
		return Reminder{}, fmt.Errorf("reminder body is required")
	}
	if channelName == "" {
		return Reminder{}, fmt.Errorf("channel is required")
	}
	if target == "" {
		return Reminder{}, fmt.Errorf("target is required")
	}
	if remindAt.IsZero() {
		return Reminder{}, fmt.Errorf("remind_at is required")
	}
	rem := Reminder{
		ID:        newID(),
		RemindAt:  remindAt.UTC(),
		Body:      body,
		Channel:   channelName,
		Target:    target,
		Status:    "pending",
		CreatedAt: time.Now().UTC(),
	}
	err := s.store.Update(func(state *State) error {
		state.Reminders = append(state.Reminders, rem)
		state.Memory.Events = append(state.Memory.Events, MemoryEvent{
			ID:        newID(),
			Kind:      "reminder_scheduled",
			Channel:   channelName,
			Author:    "claw_schedule_reminder",
			Text:      fmt.Sprintf("Scheduled %q for %s → %s", body, remindAt.Format(time.RFC3339), target),
			CreatedAt: rem.CreatedAt,
		})
		return nil
	})
	return rem, err
}

// ListReminders returns reminders matching the optional status filter.
// Empty status returns all. Newest-first by RemindAt.
func (s *Service) ListReminders(status string) []Reminder {
	if s == nil {
		return nil
	}
	snapshot := s.store.Snapshot()
	status = strings.TrimSpace(strings.ToLower(status))
	out := make([]Reminder, 0, len(snapshot.Reminders))
	for _, r := range snapshot.Reminders {
		if status != "" && strings.ToLower(r.Status) != status {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RemindAt.After(out[j].RemindAt)
	})
	return out
}

// CancelReminder marks a pending reminder as canceled. Already-sent
// or already-canceled reminders return ErrReminderNotPending.
func (s *Service) CancelReminder(id string) error {
	if s == nil {
		return fmt.Errorf("claw service is nil")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("id is required")
	}
	return s.store.Update(func(state *State) error {
		for i := range state.Reminders {
			if state.Reminders[i].ID != id {
				continue
			}
			if state.Reminders[i].Status != "pending" {
				return fmt.Errorf("reminder %s is %s, not pending", id, state.Reminders[i].Status)
			}
			state.Reminders[i].Status = "canceled"
			return nil
		}
		return fmt.Errorf("reminder %s not found", id)
	})
}

// startReminderPump spins a goroutine that ticks every reminderTickInterval
// and fires any pending reminder whose RemindAt has passed. Idempotent:
// duplicate calls are no-ops while a pump is already running.
const reminderTickInterval = 30 * time.Second

func (s *Service) startReminderPump() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.reminderCancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.reminderCancel = cancel
	s.mu.Unlock()
	go func() {
		ticker := time.NewTicker(reminderTickInterval)
		defer ticker.Stop()
		// Fire-and-forget on boot so reminders that came due while
		// forge was offline still go out promptly.
		s.fireDueReminders(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.fireDueReminders(ctx)
			}
		}
	}()
}

func (s *Service) fireDueReminders(ctx context.Context) {
	if s == nil {
		return
	}
	now := time.Now().UTC()
	snapshot := s.store.Snapshot()
	for _, r := range snapshot.Reminders {
		if r.Status != "pending" {
			continue
		}
		if r.RemindAt.After(now) {
			continue
		}
		fmt.Fprintf(os.Stderr, "[claw] reminder pump: firing %s body=%q via %s → %s\n", r.ID, trimText(r.Body, 80), r.Channel, r.Target)
		_, err := s.SendVia(ctx, r.Channel, channels.Message{To: r.Target, Body: r.Body})
		_ = s.store.Update(func(state *State) error {
			for i := range state.Reminders {
				if state.Reminders[i].ID != r.ID {
					continue
				}
				if err != nil {
					state.Reminders[i].LastError = err.Error()
					// Leave Status as "pending" so the next tick
					// retries. Persistent failures grow lastError
					// but never silently disappear.
					return nil
				}
				state.Reminders[i].Status = "sent"
				state.Reminders[i].SentAt = time.Now().UTC()
				state.Reminders[i].LastError = ""
				return nil
			}
			return nil
		})
	}
}

// processChannelSlashCommand dispatches an inbound message that starts
// with "/" to the matching slash handler. Returns the reply text that
// should be sent to the contact and a boolean indicating whether the
// message was handled. Non-commands (no leading "/") return ("", false)
// so the caller falls through to the normal LLM-driven reply path.
//
// Slash commands are out-of-band: they bypass the LLM AND the
// per-contact thread persistence so the conversation history isn't
// polluted with operator chatter.
//
// A subset of commands (/contacts, /approve, /deny, /strict) require
// the sender to be the channel's paired owner. Owner detection
// compares the inbound JID against state.Channels.Items[name].AccountID
// using stripDeviceSuffix on both sides so an owner messaging from
// any of their linked devices still qualifies.
func (s *Service) processChannelSlashCommand(channelName, sender, message string) (string, bool) {
	body := strings.TrimSpace(message)
	if !strings.HasPrefix(body, "/") {
		return "", false
	}
	parts := strings.SplitN(body, " ", 2)
	cmd := strings.ToLower(strings.TrimSpace(parts[0]))
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	snapshot := s.store.Snapshot()
	isOwner := isChannelOwner(snapshot, channelName, sender)
	switch cmd {
	case "/help", "/?":
		return clawSlashHelp(isOwner), true
	case "/reset":
		return s.handleSlashReset(channelName, sender), true
	case "/me":
		return s.handleSlashMe(channelName, sender), true
	case "/forget":
		if arg == "" {
			return "Usage: /forget <text>. Example: /forget peanut", true
		}
		return s.handleSlashForget(arg), true
	case "/contacts":
		if !isOwner {
			return "Owner only.", true
		}
		return s.handleSlashContacts(), true
	case "/approve":
		if !isOwner {
			return "Owner only.", true
		}
		if arg == "" {
			return "Usage: /approve <jid>. Example: /approve 5215555555555@s.whatsapp.net", true
		}
		if err := s.AddAllowed(channelName, arg); err != nil {
			return "Approve failed: " + err.Error(), true
		}
		return "Approved: " + arg, true
	case "/deny":
		if !isOwner {
			return "Owner only.", true
		}
		if arg == "" {
			return "Usage: /deny <jid>. Example: /deny 5215555555555@s.whatsapp.net", true
		}
		if err := s.RemoveAllowed(channelName, arg); err != nil {
			return "Deny failed: " + err.Error(), true
		}
		return "Removed: " + arg, true
	case "/strict":
		if !isOwner {
			return "Owner only.", true
		}
		switch strings.ToLower(arg) {
		case "on", "true", "1", "yes", "si", "sí":
			if err := s.SetAllowlistEnabled(channelName, true); err != nil {
				return "Toggle failed: " + err.Error(), true
			}
			return "Strict mode: ON. Only allowlisted JIDs receive replies.", true
		case "off", "false", "0", "no":
			if err := s.SetAllowlistEnabled(channelName, false); err != nil {
				return "Toggle failed: " + err.Error(), true
			}
			return "Strict mode: OFF. All contacts receive replies.", true
		default:
			ch := snapshot.Channels.Items[channelName]
			state := "off"
			if ch.AllowlistEnabled {
				state = "on"
			}
			return fmt.Sprintf("Strict mode is %s. Usage: /strict on | /strict off", state), true
		}
	}
	return "Unknown command. Send /help for the list.", true
}

// isChannelOwner returns true when sender matches the paired account's
// JID for the channel. Match is tolerant: device suffix on either side
// is stripped so the same owner messaging from any of their linked
// devices still qualifies.
func isChannelOwner(state State, channelName, sender string) bool {
	if state.Channels.Items == nil {
		return false
	}
	ch, ok := state.Channels.Items[channelName]
	if !ok {
		return false
	}
	owner := strings.ToLower(strings.TrimSpace(ch.AccountID))
	if owner == "" {
		return false
	}
	s := strings.ToLower(strings.TrimSpace(sender))
	if s == "" {
		return false
	}
	return stripDeviceSuffix(owner) == stripDeviceSuffix(s)
}

func clawSlashHelp(isOwner bool) string {
	lines := []string{
		"Commands:",
		"/help — show this list",
		"/reset — clear our conversation history",
		"/me — show what I remember about this thread",
		"/forget <text> — drop a fact matching <text>",
	}
	if isOwner {
		lines = append(lines,
			"",
			"Owner only:",
			"/contacts — list saved contacts",
			"/approve <jid> — add a JID to the allowlist",
			"/deny <jid> — remove a JID from the allowlist",
			"/strict on|off — toggle allowlist enforcement",
		)
	}
	return strings.Join(lines, "\n")
}

// handleSlashContacts lists every saved contact in a compact form.
// Owner-only — gated by the dispatcher above.
func (s *Service) handleSlashContacts() string {
	snapshot := s.store.Snapshot()
	if len(snapshot.Contacts) == 0 {
		return "No contacts saved yet."
	}
	// Iterate sorted by name for a stable display.
	keys := make([]string, 0, len(snapshot.Contacts))
	for k := range snapshot.Contacts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := []string{fmt.Sprintf("Contacts (%d):", len(keys))}
	for _, k := range keys {
		c := snapshot.Contacts[k]
		line := "• " + c.Name
		if c.Phone != "" {
			line += " — " + c.Phone
		}
		if c.Email != "" {
			line += " — " + c.Email
		}
		if c.Notes != "" {
			line += " (" + c.Notes + ")"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// handleSlashReset wipes the thread for the given contact on the given
// channel. The contact gets a clean slate — next message starts a
// fresh conversation with no prior turns.
func (s *Service) handleSlashReset(channelName, sender string) string {
	deleted := false
	_ = s.store.Update(func(state *State) error {
		if state.Channels.Items == nil {
			return nil
		}
		ch, ok := state.Channels.Items[channelName]
		if !ok || ch.Threads == nil {
			return nil
		}
		if _, exists := ch.Threads[sender]; exists {
			delete(ch.Threads, sender)
			deleted = true
		}
		state.Channels.Items[channelName] = ch
		state.Memory.Events = append(state.Memory.Events, MemoryEvent{
			ID:        newID(),
			Kind:      "thread_reset",
			Channel:   channelName,
			Author:    sender,
			Text:      "Thread reset by /reset",
			CreatedAt: time.Now().UTC(),
		})
		return nil
	})
	if deleted {
		return "Conversation reset. Starting fresh."
	}
	return "Nothing to reset — we're already starting fresh."
}

// handleSlashMe returns a quick "what do I know about you" summary for
// the contact. No JID exposed back to them — just turn count, last-seen
// timestamp, and any matching saved Contact entry by name match.
func (s *Service) handleSlashMe(channelName, sender string) string {
	snapshot := s.store.Snapshot()
	thread := channelThread(snapshot, channelName, sender)
	lines := []string{
		fmt.Sprintf("Channel: %s", channelName),
		fmt.Sprintf("History: %d turns in our thread", len(thread)),
	}
	if len(thread) > 0 {
		last := thread[len(thread)-1]
		lines = append(lines, fmt.Sprintf("Last turn: %s (%s)", last.Speaker, last.CreatedAt.Local().Format("2006-01-02 15:04")))
	}
	// Cross-reference Contacts. Senders typically arrive as JIDs, not
	// names, so a direct map lookup won't match — scan for any contact
	// whose stored phone substring appears in the JID.
	if len(snapshot.Contacts) > 0 {
		jidLower := strings.ToLower(sender)
		for _, c := range snapshot.Contacts {
			phone := strings.TrimSpace(strings.ReplaceAll(c.Phone, "+", ""))
			if phone == "" {
				continue
			}
			if strings.Contains(jidLower, strings.ToLower(phone)) {
				lines = append(lines, fmt.Sprintf("Saved as: %s", c.Name))
				if c.Notes != "" {
					lines = append(lines, "Notes: "+c.Notes)
				}
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

// handleSlashForget removes facts whose text or subject contains the
// query (case-insensitive). Returns a count for the user.
func (s *Service) handleSlashForget(query string) string {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return "Usage: /forget <text>"
	}
	removed := 0
	_ = s.store.Update(func(state *State) error {
		kept := state.Memory.Facts[:0]
		for _, f := range state.Memory.Facts {
			hay := strings.ToLower(f.Text + " " + f.Subject)
			if strings.Contains(hay, needle) {
				removed++
				continue
			}
			kept = append(kept, f)
		}
		state.Memory.Facts = kept
		return nil
	})
	if removed == 0 {
		return fmt.Sprintf("No facts matched %q.", query)
	}
	return fmt.Sprintf("Forgot %d fact(s) matching %q.", removed, query)
}

// SetAllowlistEnabled flips the gate on a channel. When true, only
// JIDs in Allowlist receive auto-replies; everyone else gets a
// rejection message. When false, every inbound is auto-replied.
func (s *Service) SetAllowlistEnabled(channelName string, enabled bool) error {
	if s == nil {
		return fmt.Errorf("claw service is nil")
	}
	channelName = strings.TrimSpace(channelName)
	if channelName == "" {
		return fmt.Errorf("channel is required")
	}
	return s.store.Update(func(state *State) error {
		if state.Channels.Items == nil {
			state.Channels.Items = map[string]Channel{}
		}
		ch := state.Channels.Items[channelName]
		ch.Name = channelName
		ch.AllowlistEnabled = enabled
		state.Channels.Items[channelName] = ch
		return nil
	})
}

// AddAllowed appends a JID to the channel's allowlist. Idempotent —
// re-adding the same JID is a no-op. Trimmed and de-duped
// case-insensitively. Pass jid_with_or_without device suffix; we
// canonicalise lookups via channelAllowsJID.
func (s *Service) AddAllowed(channelName, jid string) error {
	if s == nil {
		return fmt.Errorf("claw service is nil")
	}
	channelName = strings.TrimSpace(channelName)
	jid = strings.TrimSpace(jid)
	if channelName == "" || jid == "" {
		return fmt.Errorf("channel and jid are required")
	}
	return s.store.Update(func(state *State) error {
		if state.Channels.Items == nil {
			state.Channels.Items = map[string]Channel{}
		}
		ch := state.Channels.Items[channelName]
		ch.Name = channelName
		ch.Allowlist = appendUniqueJID(ch.Allowlist, jid)
		state.Channels.Items[channelName] = ch
		return nil
	})
}

// RemoveAllowed drops a JID from the allowlist. No error if the JID
// isn't there.
func (s *Service) RemoveAllowed(channelName, jid string) error {
	if s == nil {
		return fmt.Errorf("claw service is nil")
	}
	channelName = strings.TrimSpace(channelName)
	jid = strings.TrimSpace(jid)
	if channelName == "" || jid == "" {
		return fmt.Errorf("channel and jid are required")
	}
	needle := strings.ToLower(jid)
	return s.store.Update(func(state *State) error {
		ch, ok := state.Channels.Items[channelName]
		if !ok {
			return nil
		}
		kept := ch.Allowlist[:0]
		for _, existing := range ch.Allowlist {
			if strings.ToLower(strings.TrimSpace(existing)) == needle {
				continue
			}
			kept = append(kept, existing)
		}
		ch.Allowlist = kept
		state.Channels.Items[channelName] = ch
		return nil
	})
}

// channelAllowsJID returns true when the channel either has the gate
// off OR the sender is on the allowlist. Match is case-insensitive
// substring against each entry — that way a JID with a device suffix
// ("119567594582255:80@lid") still matches a stored bare JID
// ("119567594582255@lid"), and vice versa.
func channelAllowsJID(state State, channelName, jid string) bool {
	if state.Channels.Items == nil {
		return true // no items map → no gate
	}
	ch, ok := state.Channels.Items[channelName]
	if !ok || !ch.AllowlistEnabled {
		return true
	}
	jidLower := strings.ToLower(strings.TrimSpace(jid))
	if jidLower == "" {
		return false
	}
	for _, allowed := range ch.Allowlist {
		al := strings.ToLower(strings.TrimSpace(allowed))
		if al == "" {
			continue
		}
		if al == jidLower {
			return true
		}
		// Strip device-part on both sides for a tolerant compare —
		// "119567594582255:80@lid" should match "119567594582255@lid".
		if stripDeviceSuffix(al) == stripDeviceSuffix(jidLower) {
			return true
		}
	}
	return false
}

// stripDeviceSuffix turns "user:device@server" into "user@server".
// JIDs without a colon pass through unchanged. Used by
// channelAllowsJID for tolerant matching.
func stripDeviceSuffix(jid string) string {
	at := strings.IndexByte(jid, '@')
	if at < 0 {
		return jid
	}
	user := jid[:at]
	server := jid[at:]
	if colon := strings.IndexByte(user, ':'); colon >= 0 {
		user = user[:colon]
	}
	return user + server
}

// rejectionMessage returns a polite "you're not approved" reply,
// localised when we know the contact's preferred language. Fallback
// is bilingual EN+ES so testing doesn't dead-end on language detection.
func rejectionMessage(state State) string {
	lang := normalizeLanguageValue(state.User.Preferences["preferred_language"])
	switch lang {
	case "Spanish":
		return "Hola — no estoy configurado para chatear con este número. Pedile al dueño que te agregue."
	case "Portuguese":
		return "Olá — não estou configurado para conversar com este número. Peça ao dono para adicionar você."
	case "French":
		return "Bonjour — je ne suis pas configuré pour ce numéro. Demandez au propriétaire de vous ajouter."
	case "Italian":
		return "Ciao — non sono configurato per chattare con questo numero. Chiedi al proprietario di aggiungerti."
	case "German":
		return "Hallo — ich bin für diese Nummer nicht freigeschaltet. Bitte den Besitzer, dich hinzuzufügen."
	case "English":
		return "Hi — I'm not set up to chat with this number. Please ask the owner to add you."
	}
	return "Hi — I'm not set up to chat with this number / Hola — no estoy configurado para chatear con este número."
}

// channelThreadMaxTurns caps each per-contact transcript so an active
// conversation can't grow without bound. 30 turns ≈ 15 user/assistant
// exchanges — plenty of context for a coherent reply, well under any
// model's window even with state-JSON dumps in the prompt. When a
// thread crosses this cap the oldest channelThreadSummarizeBatch turns
// are pulled out, summarized by the LLM, and stored as a Memory
// Summary so the gist isn't lost.
const (
	channelThreadMaxTurns         = 30
	channelThreadSummarizeBatch   = 10
	channelThreadSummarySourceTag = "thread"
)

// channelThread returns the per-contact transcript for the given
// channel + contactJID, or nil if the thread has no turns yet. Safe
// for any State (zero-value channels.Items is fine).
func channelThread(state State, channelName, contactJID string) []InterviewTurn {
	if state.Channels.Items == nil {
		return nil
	}
	ch, ok := state.Channels.Items[channelName]
	if !ok || ch.Threads == nil {
		return nil
	}
	return ch.Threads[contactJID]
}

// channelThreadSummarySource builds the Source tag we stamp on a
// MemorySummary when summarizing a per-contact thread. The format is
// stable so latestThreadSummary can find the latest one for a given
// (channel, contact) pair.
func channelThreadSummarySource(channelName, contactJID string) string {
	return channelThreadSummarySourceTag + ":" + channelName + ":" + contactJID
}

// latestThreadSummary returns the most recent thread-tagged summary
// for the given (channel, contact) pair, or "" if none exists.
func latestThreadSummary(state State, channelName, contactJID string) string {
	target := channelThreadSummarySource(channelName, contactJID)
	for i := len(state.Memory.Summaries) - 1; i >= 0; i-- {
		if state.Memory.Summaries[i].Source == target {
			return strings.TrimSpace(state.Memory.Summaries[i].Summary)
		}
	}
	return ""
}

// appendChannelThreadTurn appends a turn to the per-contact transcript
// and trims it to channelThreadMaxTurns. Mutates state in place — call
// from inside a store.Update closure.
//
// Returns the slice of turns that just got dropped (if any). The
// caller is responsible for handing those to summarizeAndPersistTurns
// outside the Update closure (the LLM call is too slow to run while
// holding the store lock).
func appendChannelThreadTurn(state *State, channelName, contactJID, speaker, text string) []InterviewTurn {
	if state.Channels.Items == nil {
		state.Channels.Items = map[string]Channel{}
	}
	ch := state.Channels.Items[channelName]
	if ch.Threads == nil {
		ch.Threads = map[string][]InterviewTurn{}
	}
	ch.Threads[contactJID] = append(ch.Threads[contactJID], InterviewTurn{
		Speaker:   speaker,
		Text:      strings.TrimSpace(text),
		CreatedAt: time.Now().UTC(),
	})
	var dropped []InterviewTurn
	if n := len(ch.Threads[contactJID]); n > channelThreadMaxTurns {
		// Drop the oldest channelThreadSummarizeBatch turns (or the
		// overflow, whichever is larger). Returning the dropped slice
		// lets the caller summarize them without holding the store
		// lock.
		dropCount := n - channelThreadMaxTurns
		if dropCount < channelThreadSummarizeBatch {
			dropCount = channelThreadSummarizeBatch
		}
		if dropCount > n {
			dropCount = n
		}
		dropped = append([]InterviewTurn(nil), ch.Threads[contactJID][:dropCount]...)
		ch.Threads[contactJID] = append([]InterviewTurn(nil), ch.Threads[contactJID][dropCount:]...)
	}
	state.Channels.Items[channelName] = ch
	return dropped
}

// summarizeAndPersistThreadTurns runs an LLM summarization of the
// dropped turns and appends the result to state.Memory.Summaries. The
// summary is tagged with the thread source so latestThreadSummary can
// surface it on the next reply for that contact.
//
// Sync — adds 5-30s to the trim event, but trim only happens once per
// 30 turns of conversation. The blocked turn is the one that triggered
// the overflow; subsequent turns proceed normally with the summary in
// state.
func (s *Service) summarizeAndPersistThreadTurns(ctx context.Context, channelName, contactJID string, turns []InterviewTurn) {
	if s == nil || len(turns) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	provider, modelID, err := s.interviewProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[claw] thread summary: provider unavailable: %v\n", err)
		return
	}
	var transcript strings.Builder
	for _, t := range turns {
		role := "User"
		if t.Speaker == "claw" {
			role = "Claw"
		}
		fmt.Fprintf(&transcript, "%s: %s\n", role, t.Text)
	}
	msgs := []llm.Message{
		{Role: "system", Content: "You produce a concise factual summary of a conversation between Claw (an assistant) and a contact. Output one short paragraph (2-4 sentences max), in the language of the conversation, capturing what the contact wants, asked, agreed to, or revealed. No greetings, no meta commentary, no lists — just the gist."},
		{Role: "user", Content: "Summarize this conversation segment:\n\n" + transcript.String()},
	}
	timeout := interviewTimeout(s.config)
	if timeout > 90*time.Second {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	temp := 0.2
	resp, err := provider.Chat(ctx, llm.ChatRequest{Model: modelID, Messages: msgs, Temperature: &temp})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[claw] thread summary: chat failed: %v\n", err)
		return
	}
	summary := ""
	if resp != nil {
		summary = strings.TrimSpace(resp.Content)
	}
	if summary == "" {
		fmt.Fprintf(os.Stderr, "[claw] thread summary: empty content from model\n")
		return
	}
	_ = s.store.Update(func(state *State) error {
		state.Memory.Summaries = append(state.Memory.Summaries, MemorySummary{
			ID:        newID(),
			Source:    channelThreadSummarySource(channelName, contactJID),
			Summary:   summary,
			CreatedAt: time.Now().UTC(),
		})
		return nil
	})
	fmt.Fprintf(os.Stderr, "[claw] thread summary: stored %d-char summary for %s/%s\n", len(summary), channelName, contactJID)
}

// LogoutChannel unlinks a paired channel from its remote service (when
// the backend supports it) and clears the persisted account fields on
// State.Channels.Items[name]. Returns an error only when the backend
// supports logout but the call failed; backends without a logout
// concept return nil so the caller can rely on a single code path.
func (s *Service) LogoutChannel(ctx context.Context, name string) error {
	if s == nil {
		return fmt.Errorf("claw service is nil")
	}
	s.mu.RLock()
	reg := s.channels
	s.mu.RUnlock()
	if reg == nil {
		return fmt.Errorf("claw channel registry not initialised")
	}
	ch, ok := reg.Get(name)
	if !ok {
		return fmt.Errorf("channel %q is not registered", name)
	}
	type logoutable interface {
		Logout(ctx context.Context) error
	}
	var logoutErr error
	if l, ok := ch.(logoutable); ok {
		logoutErr = l.Logout(ctx)
	}
	_ = s.store.Update(func(state *State) error {
		if state.Channels.Items == nil {
			return nil
		}
		current, exists := state.Channels.Items[name]
		if !exists {
			return nil
		}
		current.Enabled = false
		current.AccountID = ""
		current.AccountName = ""
		current.PairedAt = time.Time{}
		state.Channels.Items[name] = current
		return nil
	})
	return logoutErr
}

// SendVia routes a message through the named channel and stamps the
// outbound activity into State.Channels.Items[name].LastInboundAt so
// the Channels submenu reflects "last activity" without each backend
// having to know about the State store.
//
// Returns the channel-decorated Message (typically with BackendID
// populated) and any error from the underlying Send.
func (s *Service) SendVia(ctx context.Context, name string, msg channels.Message) (channels.Message, error) {
	if s == nil {
		return channels.Message{}, fmt.Errorf("claw service is nil")
	}
	s.mu.RLock()
	reg := s.channels
	s.mu.RUnlock()
	if reg == nil {
		return channels.Message{}, fmt.Errorf("claw channel registry not initialised")
	}
	out, err := reg.Send(ctx, name, msg)
	if err != nil {
		return channels.Message{}, err
	}
	_ = s.store.Update(func(state *State) error {
		if state.Channels.Items == nil {
			state.Channels.Items = map[string]Channel{}
		}
		current := state.Channels.Items[name]
		current.Name = name
		current.LastInboundAt = time.Now().UTC()
		state.Channels.Items[name] = current
		return nil
	})
	return out, nil
}

// Channels returns the live channel registry. Read-only handle for the
// few callers (TUI status renderer, send-tool factory) that need it.
// May be nil before Open completes.
func (s *Service) Channels() *channels.Registry {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.channels
}

// UpdateIdentity persists in-place edits to Claw's Identity (name, tone,
// style, seed) and bumps the revision counter so downstream caches that
// key off Identity.Revision invalidate. Empty arguments are treated as
// "leave the field as-is" so the form can submit only the fields the
// user actually changed.
func (s *Service) UpdateIdentity(name, tone, style, seed string) error {
	if s == nil {
		return fmt.Errorf("claw service is nil")
	}
	return s.store.Update(func(state *State) error {
		if v := strings.TrimSpace(name); v != "" {
			state.Identity.Name = v
		}
		if v := strings.TrimSpace(tone); v != "" {
			state.Identity.Tone = v
		}
		if v := strings.TrimSpace(style); v != "" {
			state.Identity.Style = v
		}
		if v := strings.TrimSpace(seed); v != "" {
			state.Identity.Seed = v
		}
		state.Identity.UpdatedAt = time.Now().UTC()
		state.Identity.Revision++
		return nil
	})
}

func (s *Service) AddInboxMessage(channel, author, text string) (MemoryEvent, error) {
	if s == nil {
		return MemoryEvent{}, fmt.Errorf("claw service is nil")
	}
	msg := MemoryEvent{
		ID:        newID(),
		Kind:      "inbound",
		Channel:   strings.TrimSpace(channel),
		Author:    strings.TrimSpace(author),
		Text:      strings.TrimSpace(text),
		CreatedAt: time.Now().UTC(),
	}
	if msg.Channel == "" {
		msg.Channel = "mock"
	}
	if msg.Author == "" {
		msg.Author = "user"
	}
	if msg.Text == "" {
		return MemoryEvent{}, fmt.Errorf("message text is required")
	}
	if err := s.store.Update(func(state *State) error {
		state.Enabled = true
		state.Memory.Events = append(state.Memory.Events, msg)
		channelState := state.Channels.Items[msg.Channel]
		channelState.Name = msg.Channel
		if channelState.Provider == "" {
			channelState.Provider = "inbox"
		}
		channelState.Enabled = true
		channelState.LastInboundAt = msg.CreatedAt
		state.Channels.Items[msg.Channel] = channelState
		compactState(state)
		return nil
	}); err != nil {
		return MemoryEvent{}, err
	}
	return msg, nil
}

func (s *Service) BeginInterview() (string, error) {
	if s == nil {
		return "", fmt.Errorf("claw service is nil")
	}
	snapshot := s.store.Snapshot()
	if prompt := lastAssistantInterviewTurn(snapshot.Interview); prompt != "" {
		return prompt, nil
	}
	err := s.store.Update(func(state *State) error {
		now := time.Now().UTC()
		state.Enabled = true
		state.Interview.Active = true
		if state.Interview.StartedAt.IsZero() {
			state.Interview.StartedAt = now
		}
		if state.Interview.Current < 0 || state.Interview.Current >= len(interviewQuestions) {
			state.Interview.Current = 0
		}
		if len(state.Interview.Transcript) == 0 {
			state.Interview.Transcript = append(state.Interview.Transcript, InterviewTurn{
				Speaker:   "claw",
				Text:      "I already have generic defaults. I want to interview you to personalize identity, soul, user profile, and long-term memory.",
				CreatedAt: now,
			})
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	snapshot = s.store.Snapshot()
	reply, rerr := s.generateInterviewReply(snapshot)
	if rerr != nil {
		return s.applyFallbackInterviewPrompt(""), nil
	}
	if strings.TrimSpace(reply.AssistantMessage) == "" {
		return s.applyFallbackInterviewPrompt(""), nil
	}
	if err := s.store.Update(func(state *State) error {
		appendAssistantTurn(state, reply.AssistantMessage, time.Now().UTC())
		if reply.Done {
			state.Interview.Active = false
			state.Interview.CompletedAt = time.Now().UTC()
		}
		applyInterviewUpdates(state, reply.Updates, time.Now().UTC())
		compactState(state)
		return nil
	}); err != nil {
		return "", err
	}
	regenerateClawWorkspaceFiles(s.store.Snapshot())
	return reply.AssistantMessage, nil
}

func (s *Service) AnswerInterview(answer string) (string, bool, error) {
	return s.AnswerInterviewContext(context.Background(), answer)
}

func (s *Service) AnswerInterviewContext(ctx context.Context, answer string) (string, bool, error) {
	if s == nil {
		return "", false, fmt.Errorf("claw service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "", false, fmt.Errorf("answer is required")
	}
	var currentIndex int
	err := s.store.Update(func(state *State) error {
		now := time.Now().UTC()
		if !state.Interview.Active {
			state.Interview.Active = true
			if state.Interview.StartedAt.IsZero() {
				state.Interview.StartedAt = now
			}
		}
		if state.Interview.Current < 0 || state.Interview.Current >= len(interviewQuestions) {
			state.Interview.Current = 0
		}
		currentIndex = state.Interview.Current
		state.Interview.Transcript = append(state.Interview.Transcript, InterviewTurn{
			Speaker:   "user",
			Text:      answer,
			CreatedAt: now,
		})
		state.Interview.LastAnswered = now
		state.Memory.Events = append(state.Memory.Events, MemoryEvent{
			ID:        newID(),
			Kind:      "interview_answer",
			Channel:   "interview",
			Author:    "user",
			Text:      answer,
			CreatedAt: now,
		})
		ensurePreferredLanguage(state, answer)
		compactInterview(state)
		return nil
	})
	if err != nil {
		return "", false, err
	}
	snapshot := s.store.Snapshot()
	reply, rerr := s.generateInterviewReplyContext(ctx, snapshot)
	if rerr != nil && ctx.Err() != nil {
		return "", false, ctx.Err()
	}
	if rerr != nil || strings.TrimSpace(reply.AssistantMessage) == "" {
		msg, done, ferr := s.applyFallbackInterviewAnswer(answer, currentIndex)
		return msg, done, ferr
	}
	now := time.Now().UTC()
	done := reply.Done
	if err := s.store.Update(func(state *State) error {
		appendAssistantTurn(state, reply.AssistantMessage, now)
		applyInterviewUpdates(state, reply.Updates, now)
		state.Interview.Current++
		if done {
			state.Interview.Active = false
			state.Interview.CompletedAt = now
			state.Memory.Summaries = append(state.Memory.Summaries, MemorySummary{
				ID:        newID(),
				Source:    "interview",
				Summary:   "Initial Claw interview completed and core identity/user memory were personalized.",
				CreatedAt: now,
			})
		}
		compactState(state)
		return nil
	}); err != nil {
		return "", false, err
	}
	regenerateClawWorkspaceFiles(s.store.Snapshot())
	return reply.AssistantMessage, done, nil
}

func (s *Service) Chat(message string) (string, error) {
	return s.ChatContext(context.Background(), message)
}

func (s *Service) ChatContext(ctx context.Context, message string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("claw service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return "", fmt.Errorf("message is required")
	}
	chatSession, builder, err := s.ensureChatRuntime()
	if err != nil {
		return "", err
	}
	if err := chatSession.LogUser(message); err != nil {
		return "", err
	}
	err = s.store.Update(func(state *State) error {
		now := time.Now().UTC()
		state.Enabled = true
		state.Chat.Transcript = append(state.Chat.Transcript, InterviewTurn{
			Speaker:   "user",
			Text:      message,
			CreatedAt: now,
		})
		state.Memory.Events = append(state.Memory.Events, MemoryEvent{
			ID:        newID(),
			Kind:      "chat",
			Channel:   "claw",
			Author:    "user",
			Text:      message,
			CreatedAt: now,
		})
		ensurePreferredLanguage(state, message)
		compactState(state)
		return nil
	})
	if err != nil {
		return "", err
	}
	snapshot := s.store.Snapshot()
	reply, err := s.generateChatReplyContext(ctx, snapshot, message, builder)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if err := s.store.Update(func(state *State) error {
		appendChatAssistantTurn(state, reply, now)
		state.Memory.Events = append(state.Memory.Events, MemoryEvent{
			ID:        newID(),
			Kind:      "chat",
			Channel:   "claw",
			Author:    "claw",
			Text:      reply,
			CreatedAt: now,
		})
		compactState(state)
		return nil
	}); err != nil {
		return "", err
	}
	if err := chatSession.AppendChatTurn(reply); err != nil {
		return "", err
	}
	return reply, nil
}

func (s *Service) ResetChatSession() (string, error) {
	if s == nil {
		return "", fmt.Errorf("claw service is nil")
	}
	root := clawChatRootDir()
	chatSession, err := session.New(root)
	if err != nil {
		return "", err
	}
	if err := clearClawChatYarn(root); err != nil {
		return "", err
	}
	if err := s.store.Update(func(state *State) error {
		state.Chat.SessionID = chatSession.ID()
		state.Chat.Transcript = nil
		return nil
	}); err != nil {
		return "", err
	}
	builder := contextbuilder.NewBuilder(root, s.config, s.tools)
	builder.History = chatSession
	s.mu.Lock()
	s.chatRoot = root
	s.chatSession = chatSession
	s.chatBuilder = builder
	s.mu.Unlock()
	return chatSession.ID(), nil
}

func (s *Service) RunDream(ctx context.Context, reason string) (DreamResult, error) {
	select {
	case <-ctx.Done():
		return DreamResult{}, ctx.Err()
	default:
	}
	reason = strings.TrimSpace(reason)
	// Phase 1: rule-based consolidation. Cheap, always succeeds, gives
	// the LastDreamAt advance + structured suggestions. The rule pass
	// trims to the last 6 events and stamps a summary regardless of
	// whether the LLM is reachable, so dreams keep working offline.
	var result DreamResult
	err := s.store.Update(func(state *State) error {
		result = runDream(state, reason)
		return nil
	})
	if err != nil {
		return result, err
	}
	// Phase 2 (opt-in): if the LLM is reachable and there's enough fresh
	// material, ask the model for a richer prose summary and append it
	// alongside the rule-based one. Failure is silent — the rule pass
	// already produced a usable result. Keeps the heartbeat resilient
	// while still upgrading user-facing dreams when the model is up.
	state := s.store.Snapshot()
	if shouldRunLLMDream(state) {
		if llmSummary, ok := s.tryLLMDream(ctx, state, reason); ok && strings.TrimSpace(llmSummary) != "" {
			_ = s.store.Update(func(state *State) error {
				now := time.Now().UTC()
				state.Memory.Summaries = append(state.Memory.Summaries, MemorySummary{
					ID:        newID(),
					Source:    reason + ":llm",
					Summary:   trimText(llmSummary, 600),
					CreatedAt: now,
				})
				state.Memory.LastDreamAt = now
				compactState(state)
				return nil
			})
			result.Summary = llmSummary
			result.Summaries++
		}
	}
	return result, nil
}

// shouldRunLLMDream gates the model-driven consolidation. We only call
// out when there's enough new material to justify the spend: at least 4
// events have happened since the last dream, OR no LLM dream has ever
// run (so the user gets a nice first one). The rule-based pass always
// runs first, so this is purely an enhancement step.
func shouldRunLLMDream(state State) bool {
	if len(state.Memory.Events) >= 4 {
		return true
	}
	for _, sum := range state.Memory.Summaries {
		if strings.HasSuffix(sum.Source, ":llm") {
			return false
		}
	}
	return len(state.Memory.Events) > 0
}

// tryLLMDream asks the configured chat model for a paragraph that
// consolidates Claw's recent events into a memorable summary. Returns
// (summary, true) on success; (_, false) for any failure (no provider,
// timeout, empty response). Never panics, never propagates errors —
// dreaming is always best-effort.
func (s *Service) tryLLMDream(parent context.Context, state State, reason string) (string, bool) {
	provider, modelID, err := s.interviewProvider()
	if err != nil || provider == nil {
		return "", false
	}
	if parent == nil {
		parent = context.Background()
	}
	timeout := interviewTimeout(s.config)
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	events := state.Memory.Events
	if len(events) > 12 {
		events = events[len(events)-12:]
	}
	var lines []string
	for _, e := range events {
		text := strings.TrimSpace(e.Text)
		if text == "" {
			continue
		}
		lines = append(lines, "- ["+e.Kind+"] "+e.Author+": "+trimText(text, 180))
	}
	if len(lines) == 0 {
		return "", false
	}
	persona := strings.TrimSpace(state.Identity.Name)
	if persona == "" {
		persona = "Claw"
	}
	system := "You are " + persona + "'s dream consolidator. Read the recent events and produce ONE compact paragraph (3-5 sentences) capturing the through-line: what the user has been doing, what they care about right now, and any open thread you should follow up on. No bullet lists, no markdown headers, no JSON. Reply in the user's preferred language if known (state.user.preferences)."
	user := "Reason for this dream: " + reason + "\n\nRecent events:\n" + strings.Join(lines, "\n") + "\n\nReturn only the consolidated paragraph."

	resp, err := provider.Chat(ctx, llm.ChatRequest{
		Model: modelID,
		Messages: []llm.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	})
	if err != nil || resp == nil {
		return "", false
	}
	out := strings.TrimSpace(resp.Content)
	if out == "" {
		return "", false
	}
	return out, true
}

func (s *Service) AddCron(name, schedule, prompt string) (CronJob, error) {
	job := CronJob{
		ID:       newID(),
		Name:     strings.TrimSpace(name),
		Schedule: strings.TrimSpace(schedule),
		Prompt:   strings.TrimSpace(prompt),
		Enabled:  true,
	}
	if job.Name == "" || job.Schedule == "" {
		return CronJob{}, fmt.Errorf("name and schedule are required")
	}
	loc := userLocation(s.store.Snapshot())
	next, err := nextCronTime(job.Schedule, time.Now().UTC(), loc)
	if err != nil {
		return CronJob{}, err
	}
	job.NextRunAt = next
	if err := s.store.Update(func(state *State) error {
		state.Crons = append(state.Crons, job)
		return nil
	}); err != nil {
		return CronJob{}, err
	}
	return job, nil
}

// ListCrons returns a copy of every cron registered with Claw, in
// definition order.
func (s *Service) ListCrons() []CronJob {
	state := s.store.Snapshot()
	out := make([]CronJob, len(state.Crons))
	copy(out, state.Crons)
	return out
}

// RemoveCron deletes a cron by ID. Returns nil if the ID was not found —
// idempotent so the LLM can call it without checking first.
func (s *Service) RemoveCron(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("id is required")
	}
	return s.store.Update(func(state *State) error {
		filtered := state.Crons[:0]
		for _, job := range state.Crons {
			if job.ID != id {
				filtered = append(filtered, job)
			}
		}
		state.Crons = filtered
		return nil
	})
}

// Tick advances the heartbeat one beat. Side effects:
//   - LastBeatAt updated
//   - any cron whose NextRunAt <= now is marked LastRunAt and rescheduled
//   - a system MemoryEvent is appended for each fired cron
//   - if the dream interval has elapsed, the rule-based dream consolidator
//     runs in-place
//   - state is compacted
//
// Returns the list of crons that fired this tick (deep copies — safe to
// dispatch on goroutines without racing the store). Errors from rescheduling
// a malformed cron are stored on the job's LastError and surfaced via
// Heartbeat.Status="degraded" but do NOT propagate as the function's error.
func (s *Service) Tick(now time.Time) error {
	_, err := s.tickAndCollect(now)
	return err
}

func (s *Service) tickAndCollect(now time.Time) ([]CronJob, error) {
	var fired []CronJob
	err := s.store.Update(func(state *State) error {
		state.Heartbeat.LastBeatAt = now.UTC()
		if state.Heartbeat.Running {
			state.Heartbeat.Status = "running"
		}
		loc := userLocation(*state)
		for i := range state.Crons {
			job := &state.Crons[i]
			if !job.Enabled || job.NextRunAt.IsZero() || job.NextRunAt.After(now) {
				continue
			}
			job.LastRunAt = now.UTC()
			job.LastResult = "scheduled"
			job.LastError = ""
			state.Memory.Events = append(state.Memory.Events, MemoryEvent{
				ID:        newID(),
				Kind:      "cron",
				Channel:   "system",
				Author:    "heartbeat",
				Text:      job.Name + ": " + job.Prompt,
				CreatedAt: now.UTC(),
			})
			next, err := nextCronTime(job.Schedule, now, loc)
			if err != nil {
				job.LastError = err.Error()
				state.Heartbeat.Status = "degraded"
				state.Heartbeat.LastError = err.Error()
				continue
			}
			job.NextRunAt = next
			fired = append(fired, *job)
		}
		if shouldDream(state, s.cfg, now) {
			runDream(state, "heartbeat")
		}
		compactState(state)
		return nil
	})
	return fired, err
}

func (s *Service) activeModel() ActiveModel {
	s.mu.RLock()
	cfg := s.config
	providers := s.providers
	cached := s.activeModelCache
	cachedAt := s.activeModelCachedAt
	s.mu.RUnlock()
	if !cachedAt.IsZero() && time.Since(cachedAt) < activeModelCacheTTL {
		return cached
	}

	providerName := strings.TrimSpace(cfg.Providers.Default.Name)
	if providerName == "" {
		providerName = "lmstudio"
	}
	modelID := strings.TrimSpace(cfg.Models["chat"])
	if modelID == "" {
		if providerName == "openai_compatible" {
			modelID = cfg.Providers.OpenAICompatible.DefaultModel
		} else {
			modelID = cfg.Providers.LMStudio.DefaultModel
		}
	}
	active := ActiveModel{
		ProviderName: providerName,
		ModelID:      modelID,
	}
	switch providerName {
	case "openai_compatible":
		active.SupportsTools = cfg.Providers.OpenAICompatible.SupportsTools
	default:
		active.SupportsTools = cfg.Providers.LMStudio.SupportsTools
	}
	if detected := config.DetectedForRole(cfg, "chat", modelID); detected != nil {
		active.LoadedContextLength = detected.LoadedContextLength
		active.MaxContextLength = detected.MaxContextLength
	}
	if providers == nil {
		s.cacheActiveModel(active)
		return active
	}
	provider, ok := providers.Get(providerName)
	if !ok {
		s.cacheActiveModel(active)
		return active
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	info, err := provider.ProbeModel(ctx, modelID)
	if err != nil || info == nil {
		s.cacheActiveModel(active)
		return active
	}
	active.ModelID = info.ID
	if info.LoadedContextLength > 0 {
		active.LoadedContextLength = info.LoadedContextLength
	}
	if info.MaxContextLength > 0 {
		active.MaxContextLength = info.MaxContextLength
	}
	s.cacheActiveModel(active)
	return active
}

func (s *Service) cacheActiveModel(active ActiveModel) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.activeModelCache = active
	s.activeModelCachedAt = time.Now()
	s.mu.Unlock()
}

func runDream(state *State, reason string) DreamResult {
	now := time.Now().UTC()
	events := state.Memory.Events
	if len(events) > 6 {
		events = events[len(events)-6:]
	}
	summaryText := "Dream mode checked in with no new conversational material."
	if len(events) > 0 {
		var snippets []string
		senders := map[string]int{}
		for _, event := range events {
			text := strings.TrimSpace(event.Text)
			if text != "" {
				snippets = append(snippets, trimText(text, 72))
			}
			senders[event.Author]++
		}
		summaryText = "Dream mode consolidated " + fmt.Sprintf("%d", len(events)) + " recent memories"
		if len(snippets) > 0 {
			summaryText += ": " + strings.Join(snippets, " | ")
		}
		if len(senders) > 0 {
			for author, count := range senders {
				if count >= 2 {
					state.Memory.Suggestions = append(state.Memory.Suggestions, ActionSuggestion{
						ID:        newID(),
						Source:    reason,
						Summary:   "Repeated interaction with " + author + "; consider a follow-up cron or check-in.",
						CreatedAt: now,
					})
				}
			}
		}
	}
	state.Memory.Summaries = append(state.Memory.Summaries, MemorySummary{
		ID:        newID(),
		Source:    reason,
		Summary:   summaryText,
		CreatedAt: now,
	})
	state.Memory.LastDreamAt = now
	state.Soul.Revision++
	state.Soul.UpdatedAt = now
	state.Soul.LearnedNotes = append(state.Soul.LearnedNotes, "Dreamed at "+now.Format(time.RFC3339))
	compactState(state)
	return DreamResult{
		Summary:     summaryText,
		Suggestions: len(state.Memory.Suggestions),
		Summaries:   len(state.Memory.Summaries),
	}
}

func shouldDream(state *State, cfg config.ClawConfig, now time.Time) bool {
	if cfg.DreamIntervalMinutes <= 0 {
		return false
	}
	if state.Memory.LastDreamAt.IsZero() {
		return true
	}
	return now.Sub(state.Memory.LastDreamAt) >= time.Duration(cfg.DreamIntervalMinutes)*time.Minute
}

func applyConfigDefaults(state *State, cfg config.ClawConfig) {
	if cfg.IdentitySeed != "" && state.Identity.Seed == "" {
		state.Identity.Seed = cfg.IdentitySeed
	}
	if cfg.PersonaName != "" && state.Identity.Name == "Claw" {
		state.Identity.Name = cfg.PersonaName
	}
	if cfg.PersonaTone != "" && state.Identity.Tone == "warm" {
		state.Identity.Tone = cfg.PersonaTone
	}
	if cfg.DefaultChannel != "" {
		state.Channels.Default = cfg.DefaultChannel
	}
}

func compactState(state *State) {
	if len(state.Memory.Events) > 100 {
		state.Memory.Events = append([]MemoryEvent(nil), state.Memory.Events[len(state.Memory.Events)-100:]...)
	}
	if len(state.Memory.Summaries) > 40 {
		state.Memory.Summaries = append([]MemorySummary(nil), state.Memory.Summaries[len(state.Memory.Summaries)-40:]...)
	}
	if len(state.Memory.Suggestions) > 40 {
		state.Memory.Suggestions = append([]ActionSuggestion(nil), state.Memory.Suggestions[len(state.Memory.Suggestions)-40:]...)
	}
	if len(state.Soul.LearnedNotes) > 50 {
		state.Soul.LearnedNotes = append([]string(nil), state.Soul.LearnedNotes[len(state.Soul.LearnedNotes)-50:]...)
	}
	compactInterview(state)
	compactChat(state)
}

func toolDescriptors(registry *tools.Registry) []ToolDescriptor {
	if registry == nil {
		return nil
	}
	descs := registry.Describe()
	out := make([]ToolDescriptor, 0, len(descs))
	for _, desc := range descs {
		out = append(out, ToolDescriptor{Name: desc.Name, Status: desc.Status})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func trimText(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}

func newID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

func compactInterview(state *State) {
	if len(state.Interview.Transcript) > 40 {
		state.Interview.Transcript = append([]InterviewTurn(nil), state.Interview.Transcript[len(state.Interview.Transcript)-40:]...)
	}
}

func compactChat(state *State) {
	if len(state.Chat.Transcript) > 40 {
		state.Chat.Transcript = append([]InterviewTurn(nil), state.Chat.Transcript[len(state.Chat.Transcript)-40:]...)
	}
}

func appendAssistantTurn(state *State, text string, now time.Time) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len(state.Interview.Transcript) > 0 {
		last := state.Interview.Transcript[len(state.Interview.Transcript)-1]
		if last.Speaker == "claw" && strings.TrimSpace(last.Text) == text {
			return
		}
	}
	state.Interview.Transcript = append(state.Interview.Transcript, InterviewTurn{
		Speaker:   "claw",
		Text:      text,
		CreatedAt: now,
	})
}

func appendChatAssistantTurn(state *State, text string, now time.Time) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len(state.Chat.Transcript) > 0 {
		last := state.Chat.Transcript[len(state.Chat.Transcript)-1]
		if last.Speaker == "claw" && strings.TrimSpace(last.Text) == text {
			return
		}
	}
	state.Chat.Transcript = append(state.Chat.Transcript, InterviewTurn{
		Speaker:   "claw",
		Text:      text,
		CreatedAt: now,
	})
}

func lastAssistantInterviewTurn(interview Interview) string {
	for i := len(interview.Transcript) - 1; i >= 0; i-- {
		turn := interview.Transcript[i]
		if turn.Speaker == "claw" && strings.TrimSpace(turn.Text) != "" {
			return turn.Text
		}
	}
	return ""
}

func applyInterviewAnswer(state *State, key, answer string, now time.Time) {
	switch key {
	case "user_name":
		state.User.DisplayName = answer
		state.User.UpdatedAt = now
	case "claw_name":
		state.Identity.Name = answer
		state.Identity.UpdatedAt = now
		state.Identity.Revision++
	case "tone":
		state.Identity.Tone = answer
		state.Identity.UpdatedAt = now
		state.Identity.Revision++
	case "values":
		state.Soul.Values = splitList(answer)
		state.Soul.UpdatedAt = now
		state.Soul.Revision++
	case "routines":
		state.Soul.Goals = splitList(answer)
		state.User.Preferences["routines"] = answer
		state.User.UpdatedAt = now
		state.Soul.UpdatedAt = now
		state.Soul.Revision++
	case "timezone":
		state.User.Timezone = answer
		state.User.UpdatedAt = now
	case "day_one":
		state.User.Preferences["day_one_note"] = answer
		state.User.UpdatedAt = now
		state.Soul.LearnedNotes = append(state.Soul.LearnedNotes, "Day-one note: "+answer)
		state.Memory.Summaries = append(state.Memory.Summaries, MemorySummary{
			ID:        newID(),
			Source:    "interview",
			Summary:   "Day-one note: " + answer,
			CreatedAt: now,
		})
	}
}

func applyInterviewUpdates(state *State, updates InterviewUpdates, now time.Time) {
	if updates.Identity != nil {
		identityName := strings.TrimSpace(updates.Identity.Name)
		userName := ""
		if updates.User != nil {
			userName = strings.TrimSpace(updates.User.DisplayName)
		}
		// Reject language names ("Spanish", "English", etc.). Small
		// models routinely mis-attribute the user's answer to the
		// language-preference question as the assistant's name. The
		// language belongs in user.preferences.preferred_language and
		// is owned by the deterministic detector — not here.
		if identityName != "" && !sameInterviewValue(identityName, userName) && identityName != state.Identity.Name && !isLanguageName(identityName) {
			state.Identity.Name = updates.Identity.Name
			state.Identity.Revision++
		}
		if updates.Identity.Tone != "" && updates.Identity.Tone != state.Identity.Tone {
			state.Identity.Tone = updates.Identity.Tone
			state.Identity.Revision++
		}
		if updates.Identity.Style != "" {
			state.Identity.Style = updates.Identity.Style
		}
		if updates.Identity.Seed != "" {
			state.Identity.Seed = updates.Identity.Seed
		}
		if updates.Identity.Description != "" {
			state.Identity.Description = updates.Identity.Description
		}
		state.Identity.UpdatedAt = now
	}
	if updates.Soul != nil {
		if len(updates.Soul.Values) > 0 {
			state.Soul.Values = cleanList(updates.Soul.Values)
		}
		if len(updates.Soul.Goals) > 0 {
			state.Soul.Goals = cleanList(updates.Soul.Goals)
		}
		if len(updates.Soul.Traits) > 0 {
			state.Soul.Traits = cleanList(updates.Soul.Traits)
		}
		if len(updates.Soul.LearnedNotes) > 0 {
			state.Soul.LearnedNotes = append(state.Soul.LearnedNotes, cleanList(updates.Soul.LearnedNotes)...)
		}
		state.Soul.UpdatedAt = now
		state.Soul.Revision++
	}
	if updates.User != nil {
		if updates.User.DisplayName != "" {
			state.User.DisplayName = updates.User.DisplayName
		}
		if updates.User.Timezone != "" {
			state.User.Timezone = updates.User.Timezone
		}
		if len(updates.User.Preferences) > 0 {
			if state.User.Preferences == nil {
				state.User.Preferences = map[string]string{}
			}
			for key, value := range updates.User.Preferences {
				key = strings.TrimSpace(key)
				value = strings.TrimSpace(value)
				if key == "" || value == "" {
					continue
				}
				// preferred_language is owned exclusively by
				// ensurePreferredLanguage (the deterministic detector
				// driven by the user's actual answers). The LLM tries
				// to set this field on the very first turn — before
				// the user has even answered — based on whatever
				// language *it* happens to be writing in, which then
				// poisons the idempotency guard. Blocking every LLM
				// write to this key keeps the detector authoritative.
				if key == "preferred_language" {
					continue
				}
				state.User.Preferences[key] = value
			}
		}
		state.User.UpdatedAt = now
	}
	if strings.TrimSpace(updates.MemorySummary) != "" {
		state.Memory.Summaries = append(state.Memory.Summaries, MemorySummary{
			ID:        newID(),
			Source:    "interview",
			Summary:   strings.TrimSpace(updates.MemorySummary),
			CreatedAt: now,
		})
	}
}

// detectLanguageFromAnswer matches common language-declaration patterns
// in either English or the language itself. Used to set preferred_language
// without relying on the LLM to emit it correctly — small local models
// drop nested JSON updates often enough that we can't trust them with
// the single most important persistence on the interview path.
//
// Returns ISO-ish language tags ("Spanish", "English", "Portuguese",
// etc. — capitalized words rather than codes since downstream prompts
// embed the value into natural-language directives).
func detectLanguageFromAnswer(text string) string {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return ""
	}
	// Order matters: longest/most specific patterns first so "español"
	// wins over a stray "es" inside a longer sentence. Includes common
	// typos ("spanol" without leading 'e', "portuges" missing 'u') so
	// users who type quickly still get caught.
	patterns := []struct {
		needle string
		lang   string
	}{
		{"español", "Spanish"}, {"espanol", "Spanish"},
		{"castellano", "Spanish"}, {"spanish", "Spanish"},
		{"spanol", "Spanish"}, // common typo of espanol
		{"english", "English"}, {"inglés", "English"}, {"ingles", "English"},
		{"portugués", "Portuguese"}, {"portugues", "Portuguese"},
		{"português", "Portuguese"}, {"portuguese", "Portuguese"},
		{"français", "French"}, {"francais", "French"},
		{"frances", "French"}, {"french", "French"},
		{"italiano", "Italian"}, {"italian", "Italian"},
		{"deutsch", "German"}, {"alemán", "German"},
		{"aleman", "German"}, {"german", "German"},
	}
	for _, p := range patterns {
		if strings.Contains(t, p.needle) {
			return p.lang
		}
	}
	return ""
}

// isLanguageName reports whether a proposed identity name looks like
// a language label rather than an actual name. Used to defend against
// the common LLM error where the user's answer to "what language do
// you prefer?" gets mis-attributed to identity.name. Match is
// case/whitespace insensitive on the canonical language list.
func isLanguageName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	for _, lang := range []string{
		"spanish", "español", "espanol", "spanol", "castellano",
		"english", "inglés", "ingles",
		"portuguese", "portugués", "portugues", "português",
		"french", "français", "francais", "frances",
		"italian", "italiano",
		"german", "deutsch", "alemán", "aleman",
	} {
		if n == lang {
			return true
		}
	}
	return false
}

// migrateCorruptIdentityName resets Identity.Name back to the default
// "Claw" when an earlier interview turn accidentally stamped a
// language label there. Idempotent and safe to run on every Open —
// healthy state passes through unchanged.
func migrateCorruptIdentityName(state *State) bool {
	if !isLanguageName(state.Identity.Name) {
		return false
	}
	state.Identity.Name = "Claw"
	state.Identity.Revision++
	state.Identity.UpdatedAt = time.Now().UTC()
	return true
}

// inferLanguageFromText counts language-specific function words in
// the user's text. Lower-precision than detectLanguageFromAnswer (which
// only fires on explicit declarations like "Spanish" or "español") —
// this catches the common case where the user just *writes* in their
// preferred language without naming it. Requires a 2-marker margin
// over the other language so a stray "the" or "que" doesn't trigger.
//
// Returns "Spanish" / "English" / "Portuguese" / "" (uncertain).
func inferLanguageFromText(text string) string {
	t := " " + strings.ToLower(strings.TrimSpace(text)) + " "
	if t == "  " {
		return ""
	}
	// Markers chosen for high precision: function words that are
	// rare-or-absent in the other language. Prefixed/suffixed with
	// space so "soy" doesn't match "isoyl" etc.
	spanishMarkers := []string{
		" podemos ", " puedo ", " puedes ", " puede ",
		" quiero ", " quieres ", " quiere ",
		" hablar ", " hablamos ", " hablo ", " hablas ",
		" está ", " están ", " estamos ", " estoy ",
		" soy ", " somos ", " eres ",
		" tengo ", " tienes ", " tiene ",
		" porque ", " pero ", " para ",
		" español ", " espanol ", " spanol ", " castellano ",
		" gracias ", " hola ", " bueno ", " buenos ",
		" que ", " es ", " yo ", " mi ", " tu ", " su ",
		" cómo ", " como ", " cuando ", " donde ",
	}
	englishMarkers := []string{
		" the ", " and ", " with ", " have ", " has ",
		" i'm ", " you're ", " we're ", " they're ",
		" english ",
		" yes ", " no, ", " hi ", " hello ",
		" what's ", " let's ", " that's ",
		" can ", " will ", " would ", " could ",
		" i ", " we ", " they ", " my ", " your ",
		" how ", " when ", " where ", " what ",
	}
	portugueseMarkers := []string{
		" você ", " voce ", " eu ", " nós ",
		" português ", " portugues ", " falar ",
		" obrigado ", " obrigada ", " olá ",
		" não ", " sim ", " bom ", " bem ",
	}
	sp := countMarkers(t, spanishMarkers)
	en := countMarkers(t, englishMarkers)
	pt := countMarkers(t, portugueseMarkers)
	// Pick the leader if it beats every other language by 2+. Ties
	// or weak leads return "" so we don't lock the wrong language on
	// noisy input.
	scores := []struct {
		lang  string
		count int
	}{
		{"Spanish", sp},
		{"English", en},
		{"Portuguese", pt},
	}
	leader := scores[0]
	for _, s := range scores[1:] {
		if s.count > leader.count {
			leader = s
		}
	}
	if leader.count == 0 {
		return ""
	}
	for _, s := range scores {
		if s.lang == leader.lang {
			continue
		}
		if leader.count-s.count < 2 {
			return ""
		}
	}
	return leader.lang
}

func countMarkers(text string, markers []string) int {
	n := 0
	for _, m := range markers {
		if strings.Contains(text, m) {
			n++
		}
	}
	return n
}

// ensurePreferredLanguage persists the detected language into state if
// preferred_language is still unset. Idempotent — once a language is
// stored, later answers can't accidentally overwrite it (the user might
// briefly mention another language without intending to switch).
func ensurePreferredLanguage(state *State, userText string) {
	if state.User.Preferences != nil {
		if existing := strings.TrimSpace(state.User.Preferences["preferred_language"]); existing != "" {
			return
		}
	}
	// First try the explicit-declaration matcher ("Spanish", "español").
	// If the user didn't *name* a language, fall through to the
	// word-frequency inferrer, which catches "Nicolas, podemos hablar
	// en spanol?" by counting Spanish-only function words.
	lang := detectLanguageFromAnswer(userText)
	if lang == "" {
		lang = inferLanguageFromText(userText)
	}
	if lang == "" {
		return
	}
	if state.User.Preferences == nil {
		state.User.Preferences = map[string]string{}
	}
	state.User.Preferences["preferred_language"] = normalizeLanguageValue(lang)
	state.User.UpdatedAt = time.Now().UTC()
}

// normalizeLanguageValue maps ISO language codes and common aliases
// onto the canonical English name we feed into LANGUAGE LOCK. Small
// local models interpret "Spanish" reliably; "es" or "es-MX" they
// often ignore or treat as a noisy token. Empty input returns empty,
// unknown values pass through capitalized so an exotic language at
// least survives.
func normalizeLanguageValue(raw string) string {
	t := strings.ToLower(strings.TrimSpace(raw))
	if t == "" {
		return ""
	}
	// Strip a region tag if present ("es-mx" → "es").
	if idx := strings.IndexAny(t, "-_"); idx > 0 {
		t = t[:idx]
	}
	switch t {
	case "es", "spa", "spanish", "español", "espanol", "castellano":
		return "Spanish"
	case "en", "eng", "english", "inglés", "ingles":
		return "English"
	case "pt", "por", "portuguese", "portugués", "portugues", "português":
		return "Portuguese"
	case "fr", "fra", "fre", "french", "français", "francais", "frances":
		return "French"
	case "it", "ita", "italian", "italiano":
		return "Italian"
	case "de", "deu", "ger", "german", "alemán", "aleman", "deutsch":
		return "German"
	}
	// Unknown tongue: capitalize the first rune so the directive at
	// least reads naturally ("LANGUAGE LOCK: ... in Klingon").
	runes := []rune(t)
	runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
	return string(runes)
}

func sameInterviewValue(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func (s *Service) generateInterviewReply(state State) (InterviewReply, error) {
	return s.generateInterviewReplyContext(context.Background(), state)
}

func (s *Service) generateInterviewReplyContext(ctx context.Context, state State) (InterviewReply, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	provider, modelID, err := s.interviewProvider()
	if err != nil {
		return InterviewReply{}, err
	}
	stateJSON, err := json.Marshal(stateForInterview(state))
	if err != nil {
		return InterviewReply{}, err
	}
	clockLine := "Current local time: " + time.Now().Local().Format("2006-01-02 15:04:05 MST")
	msgs := []llm.Message{
		{Role: "system", Content: clockLine + "\n\n" + interviewSystemPromptForState(state)},
		{Role: "user", Content: "Current Claw state JSON:\n" + string(stateJSON)},
	}
	for _, turn := range trimmedInterviewTranscript(state.Interview.Transcript, 12) {
		role := "user"
		if turn.Speaker == "claw" {
			role = "assistant"
		}
		msgs = append(msgs, llm.Message{Role: role, Content: turn.Text})
	}
	if lang := normalizeLanguageValue(state.User.Preferences["preferred_language"]); lang != "" {
		// Append-into-last-user trumps a tail system message for small
		// models — providers (LM Studio's OpenAI shim included) often
		// collapse consecutive system messages, so the tail directive
		// silently merges back into the head one. Inlining the hint at
		// the top of the latest user turn keeps it in the message
		// immediately before generation, with no role-disrupt risk.
		injectLanguageHintIntoLatestUser(msgs, "interview", lang)
	}
	s.recordLLMTurn(modelID, "interview", msgs)
	temp := 0.2
	ctx, cancel := context.WithTimeout(ctx, interviewTimeout(s.config))
	defer cancel()
	resp, err := provider.Chat(ctx, llm.ChatRequest{
		Model:       modelID,
		Messages:    msgs,
		Temperature: &temp,
	})
	if err != nil {
		return InterviewReply{}, err
	}
	if resp == nil {
		return InterviewReply{}, fmt.Errorf("empty interview response")
	}
	return parseInterviewReply(resp.Content)
}

func (s *Service) generateChatReplyContext(ctx context.Context, state State, userMessage string, builder *contextbuilder.Builder) (string, error) {
	// Local TUI chat is always a main session (the human running forge
	// IS the owner), so MEMORY.md is included.
	return s.generateChatReplyContextExt(ctx, state, userMessage, builder, "", true)
}

// generateChatReplyContextExt is the underlying implementation; the
// systemExtra parameter appends a free-form instruction block to the
// system prompt for callers that need channel-specific framing
// (e.g. "you're chatting on WhatsApp, keep replies short"). Empty
// systemExtra means no extension — identical to the legacy behaviour.
//
// includeMemory gates whether MEMORY.md is loaded into the system
// prompt. False for shared contexts (non-owner WhatsApp contacts,
// group chats) so curated long-term memory never leaks to strangers.
func (s *Service) generateChatReplyContextExt(ctx context.Context, state State, userMessage string, builder *contextbuilder.Builder, systemExtra string, includeMemory bool) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	provider, modelID, err := s.interviewProvider()
	if err != nil {
		return "", err
	}
	stateJSON, err := json.Marshal(stateForChat(state))
	if err != nil {
		return "", err
	}
	// LLMs have no clock. Inject the current local time at the top of
	// the system prompt so "qué hora es?" / "what time is it?" gets a
	// correct answer instead of a hallucinated guess. The format
	// includes the timezone offset for unambiguous reading.
	clockLine := "Current local time: " + time.Now().Local().Format("2006-01-02 15:04:05 MST")
	awarenessLine := buildAwarenessLine(state)
	header := clockLine
	if awarenessLine != "" {
		header += "\n" + awarenessLine
	}
	systemPrompt := header + "\n\n" + clawChatSystemPromptForState(state, includeMemory)
	if extra := strings.TrimSpace(systemExtra); extra != "" {
		systemPrompt = systemPrompt + "\n\n" + extra
	}
	msgs := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: "Current Claw state JSON:\n" + string(stateJSON)},
	}
	if builder != nil {
		snapshot := builder.Build(userMessage)
		contextText := strings.TrimSpace(snapshot.Render())
		if contextText != "" {
			msgs = append(msgs, llm.Message{Role: "user", Content: "Forge session context:\n" + contextText})
		}
	}
	for _, turn := range trimmedChatTranscript(state.Chat.Transcript, 14) {
		role := "user"
		if turn.Speaker == "claw" {
			role = "assistant"
		}
		msgs = append(msgs, llm.Message{Role: role, Content: turn.Text})
	}
	if lang := normalizeLanguageValue(state.User.Preferences["preferred_language"]); lang != "" {
		injectLanguageHintIntoLatestUser(msgs, "chat", lang)
	}
	s.recordLLMTurn(modelID, "chat", msgs)
	temp := 0.4
	ctx, cancel := context.WithTimeout(ctx, interviewTimeout(s.config))
	defer cancel()

	s.mu.RLock()
	registry := s.tools
	toolsEnabled := s.cfg.ToolsEnabled
	s.mu.RUnlock()

	reply, err := runClawChatWithTools(ctx, provider, modelID, registry, msgs, &temp, toolsEnabled)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(reply) == "" {
		return "", fmt.Errorf("empty claw chat response")
	}
	return reply, nil
}

func (s *Service) interviewProvider() (llm.Provider, string, error) {
	s.mu.RLock()
	cfg := s.config
	providers := s.providers
	s.mu.RUnlock()
	if providers == nil {
		return nil, "", fmt.Errorf("provider registry unavailable")
	}
	providerName := strings.TrimSpace(cfg.Providers.Default.Name)
	if providerName == "" {
		providerName = "lmstudio"
	}
	provider, ok := providers.Get(providerName)
	if !ok {
		return nil, "", fmt.Errorf("provider %s not registered", providerName)
	}
	modelID := strings.TrimSpace(cfg.Models["chat"])
	if modelID == "" {
		switch providerName {
		case "openai_compatible":
			modelID = strings.TrimSpace(cfg.Providers.OpenAICompatible.DefaultModel)
		default:
			modelID = strings.TrimSpace(cfg.Providers.LMStudio.DefaultModel)
		}
	}
	if modelID == "" {
		return nil, "", fmt.Errorf("no active chat model configured")
	}
	return provider, modelID, nil
}

func parseInterviewReply(raw string) (InterviewReply, error) {
	raw = strings.TrimSpace(raw)
	var reply InterviewReply
	if err := json.Unmarshal([]byte(raw), &reply); err == nil {
		return reply, nil
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(raw[start:end+1]), &reply); err == nil {
			return reply, nil
		}
	}
	return InterviewReply{}, fmt.Errorf("invalid interview json: %s", trimText(raw, 160))
}

// interviewSystemPromptForState assembles the system prompt for the
// interview reply path: a markdown-driven block (AGENTS, IDENTITY,
// SOUL, USER, TOOLS, HEARTBEAT, MEMORY) followed by the interview
// JSON schema rules and the language lock directive (when known).
//
// Interview always includes MEMORY because the user driving the
// interview IS the owner — they're personalising Claw's brain.
func interviewSystemPromptForState(state State) string {
	files := loadClawWorkspace(true)
	persona := composeClawSystemPrompt(files)
	prompt := interviewSystemPrompt()
	if persona != "" {
		prompt = persona + "\n\n" + prompt
	}
	if lang := normalizeLanguageValue(state.User.Preferences["preferred_language"]); lang != "" {
		return strings.ToUpper(interviewLanguageDirective(lang)) + "\n\n" + prompt
	}
	return prompt
}

// clawChatSystemPromptForState mirrors interviewSystemPromptForState
// for the post-interview chat path. includeMemory gates MEMORY.md so
// shared contexts (non-owner WhatsApp contacts, group chats) never
// see the user's curated long-term memory.
func clawChatSystemPromptForState(state State, includeMemory bool) string {
	files := loadClawWorkspace(includeMemory)
	persona := composeClawSystemPrompt(files)
	prompt := clawChatSystemPrompt()
	if persona != "" {
		prompt = persona + "\n\n" + prompt
	}
	if lang := normalizeLanguageValue(state.User.Preferences["preferred_language"]); lang != "" {
		return strings.ToUpper(chatLanguageDirective(lang)) + "\n\n" + prompt
	}
	return prompt
}

// interviewLanguageDirective targets the JSON-shaped output of the
// interview path: the LLM must populate the assistant_message field in
// the requested language. Wording mentions the JSON contract explicitly
// so small models can't rationalize "the json schema is universal".
func interviewLanguageDirective(lang string) string {
	return "Language lock: the assistant_message field of your JSON response must be written entirely in " + lang +
		". This rule overrides every other instruction in this prompt. Do not switch languages even if the transcript contains turns in another tongue."
}

// chatLanguageDirective is the chat-mode counterpart — the model
// emits free-form text, so the rule binds the whole reply.
func chatLanguageDirective(lang string) string {
	return "Language lock: your entire reply must be written in " + lang +
		". This rule overrides every other instruction in this prompt. Do not switch languages even if the user briefly writes in another one."
}

// languageDirective is kept as a thin alias for the rare callers that
// want a generic phrasing. New code should prefer interviewLanguageDirective
// or chatLanguageDirective so the wording matches the expected output.
func languageDirective(lang string) string {
	return chatLanguageDirective(lang)
}

// injectLanguageHintIntoLatestUser prepends a one-line language
// reminder to the most recent user message in msgs (if any). Mode
// selects the wording: "interview" emphasizes the JSON output schema,
// "chat" binds the free-form reply.
//
// Modifies msgs in place — the caller passes its own slice. Safe even
// when no user message exists yet (no-op).
func injectLanguageHintIntoLatestUser(msgs []llm.Message, mode, lang string) {
	if lang == "" {
		return
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		hint := "[Language lock: respond in " + lang + ".] "
		if mode == "interview" {
			hint = "[Language lock: assistant_message must be in " + lang + ".] "
		}
		// Avoid double-prepending if the same hint is already there
		// (defensive — shouldn't happen given the call sites, but
		// idempotency is cheap insurance).
		if !strings.HasPrefix(msgs[i].Content, hint) {
			msgs[i].Content = hint + msgs[i].Content
		}
		return
	}
}

func interviewSystemPrompt() string {
	return "You are the interview brain for Claw, a persistent assistant inside Forge. " +
		"Your job is to run a natural onboarding interview one question at a time and return ONLY strict JSON. " +
		"Decide the next best question based on the transcript and current state. " +
		"If user.preferences.preferred_language is already known, every future assistant_message must use that language. " +
		"Only fall back to the user's most recent transcript turn when preferred_language is still unknown; if the language is unclear, default to Spanish. " +
		"If the transcript does not yet contain a user answer and preferred language is still unknown, the first question must ask which language the user primarily uses and you should store it in updates.user.preferences.preferred_language once known. " +
		"Do not set updates.identity.name from the user's own name. Only write identity.name if the user explicitly answers a question about what Claw should call itself. " +
		"Never set identity.name to a language label (Spanish, English, Portuguese, French, Italian, German, etc.). When the user names a language, that goes into updates.user.preferences.preferred_language ONLY. " +
		"Use this exact shape: " +
		`{"assistant_message":"...","done":false,"updates":{"identity":{"name":"","tone":"","style":"","seed":"","description":""},"soul":{"values":[],"goals":[],"traits":[],"learned_notes":[]},"user":{"display_name":"","timezone":"","preferences":{}},"memory_summary":"..."}} ` +
		"Rules: ask exactly one concise next question unless done=true; keep assistant_message to at most two sentences; only include updates you are confident about; do not emit markdown, prose, or code fences."
}

func clawChatSystemPrompt() string {
	return "You are Claw, the resident assistant inside Forge. " +
		"Speak with the identity, tone, style, values, goals, and learned notes provided in state. " +
		"If user.preferences.preferred_language is known, always answer in that language; otherwise follow the user's latest language and default to Spanish when unclear. " +
		"Use memory summaries, recent transcript turns, and any Forge session context snapshot you receive to stay consistent. " +
		"Be concise, natural, and helpful. Do not emit JSON, markdown fences, or tool syntax unless the user explicitly asks for it. " +
		"\n\nCAPABILITIES YOU ACTUALLY HAVE — these tools are wired and live in this environment, not hypothetical. NEVER claim you 'can't', 'don't have access', or 'cannot do this in this environment' for any action covered by these tools. INVOKE the tool. The runtime executes the side effect for real:\n" +
		"- web_search / web_fetch: search and read web pages.\n" +
		"- whatsapp_send: send a WhatsApp message. Required: 'to' (a phone number with country code, e.g. +573214447235, or a JID) and 'body' (the message). Use this whenever the user gives you both a recipient and a message to deliver.\n" +
		"- claw_remember / claw_recall: persist and look up free-form facts about the user, contacts, schedules, preferences.\n" +
		"- claw_save_contact / claw_lookup_contact: structured contact storage with name + phone + email + notes.\n" +
		"- claw_schedule_reminder / claw_list_reminders / claw_cancel_reminder: one-shot timers — at the specified UTC time the body is sent through a channel. Use this whenever the user says 'remind me in N minutes', 'in 30 seconds', 'tomorrow at 9'. Convert the relative time to an absolute ISO 8601 timestamp before calling. The reminder pump WILL fire it; this is not theoretical.\n" +
		"- claw_add_cron / claw_list_crons / claw_remove_cron: recurring scheduled tasks (@every, @daily, @at HH:MM, @dow Mon HH:MM, or 5-field cron). Use these for 'every morning', 'every Monday', recurring check-ins.\n" +
		"- claw_recent_memory / claw_dream_now: read your own recent memory and trigger a consolidation pass.\n" +
		"- claw_workspace_note: append prose to your own personality .md files (IDENTITY, SOUL, USER, MEMORY, TOOLS).\n" +
		"\nRULES:\n" +
		"1. If the user gives you an instruction that maps to a tool above, INVOKE the tool. Do NOT reply with 'I can't' / 'no puedo' / 'no tengo la capacidad' / 'no puedo hacerlo en este entorno' — those phrases are forbidden when a matching tool exists. The user knows what tools you have; they would not ask if you didn't.\n" +
		"2. whatsapp_send: still require an explicit recipient + message in the user's request. Never invent a number, never send unsolicited content. But once the user gives both, send it.\n" +
		"3. claw_schedule_reminder needs an ISO 8601 remind_at. Compute it from the current local time the system gives you (e.g. 'in 30 seconds' → now + 30s in RFC3339).\n" +
		"4. For greetings, small talk, opinions, or anything answerable from knowledge: reply directly with NO tool call.\n" +
		"5. After a tool returns, READ the result and incorporate its facts into your reply — never respond as if the tool returned nothing when it did.\n" +
		"6. web_* only when the answer depends on current info you don't already know."
}

func stateForInterview(state State) map[string]any {
	// Interview keeps identity/soul/user in the JSON dump because the
	// LLM needs to read CURRENT field values to decide what to ask
	// next AND to populate the updates.* keys in its JSON response.
	// The markdown workspace is also loaded into the system prompt
	// for personality, but the interview's update-shaped output
	// requires structured input alongside it.
	return map[string]any{
		"identity": map[string]any{
			"name":        state.Identity.Name,
			"tone":        state.Identity.Tone,
			"style":       state.Identity.Style,
			"seed":        state.Identity.Seed,
			"description": state.Identity.Description,
		},
		"soul": map[string]any{
			"values":        state.Soul.Values,
			"goals":         state.Soul.Goals,
			"traits":        state.Soul.Traits,
			"learned_notes": tailStrings(state.Soul.LearnedNotes, 6),
		},
		"user": map[string]any{
			"display_name": state.User.DisplayName,
			"timezone":     state.User.Timezone,
			"preferences":  state.User.Preferences,
		},
		"memory": map[string]any{
			"summaries": tailMemorySummaries(state.Memory.Summaries, 4),
		},
		"interview": map[string]any{
			"active":    state.Interview.Active,
			"completed": !state.Interview.CompletedAt.IsZero(),
		},
	}
}

func stateForChat(state State) map[string]any {
	// identity/soul/user used to live in this JSON blob, but the
	// markdown workspace (AGENTS/IDENTITY/SOUL/USER/...) now carries
	// the personality. Duplicating both confuses small models — they
	// see "in JSON tone='warm', in IDENTITY.md tone='direct'" and
	// pick one at random. Markdown is the source of truth; this dump
	// keeps only structured/operational fields the LLM can't read
	// from a .md (memory summaries, language preference, heartbeat
	// flag).
	return map[string]any{
		"user": map[string]any{
			"preferences": state.User.Preferences,
		},
		"memory": map[string]any{
			"summaries": tailMemorySummaries(state.Memory.Summaries, 6),
		},
		"heartbeat": map[string]any{
			"running": state.Heartbeat.Running,
			"status":  state.Heartbeat.Status,
		},
		"chat": map[string]any{
			"session_id": state.Chat.SessionID,
			"turns":      len(state.Chat.Transcript),
		},
	}
}

func trimmedInterviewTranscript(turns []InterviewTurn, maxTurns int) []InterviewTurn {
	if len(turns) <= maxTurns {
		return turns
	}
	return turns[len(turns)-maxTurns:]
}

func trimmedChatTranscript(turns []InterviewTurn, maxTurns int) []InterviewTurn {
	if len(turns) <= maxTurns {
		return turns
	}
	return turns[len(turns)-maxTurns:]
}

// buildAwarenessLine produces a one-line "you have X reminders, Y crones,
// Z facts in memory" hint the LLM sees at the top of every chat turn. The
// goal is self-awareness: without this, Claw routinely answers "no tengo
// nada agendado contigo" when state.Reminders is full because it never
// looked. Empty when nothing's actually scheduled, so we don't waste
// prompt tokens on zeroes.
func buildAwarenessLine(state State) string {
	pendingReminders := 0
	for _, r := range state.Reminders {
		if strings.EqualFold(strings.TrimSpace(r.Status), "pending") {
			pendingReminders++
		}
	}
	enabledCrons := 0
	for _, c := range state.Crons {
		if c.Enabled {
			enabledCrons++
		}
	}
	contacts := len(state.Contacts)
	facts := len(state.Memory.Facts)
	if pendingReminders == 0 && enabledCrons == 0 && contacts == 0 && facts == 0 {
		return ""
	}
	parts := make([]string, 0, 4)
	if pendingReminders > 0 {
		parts = append(parts, fmt.Sprintf("%d pending reminder(s)", pendingReminders))
	}
	if enabledCrons > 0 {
		parts = append(parts, fmt.Sprintf("%d active cron(s)", enabledCrons))
	}
	if contacts > 0 {
		parts = append(parts, fmt.Sprintf("%d known contact(s)", contacts))
	}
	if facts > 0 {
		parts = append(parts, fmt.Sprintf("%d remembered fact(s)", facts))
	}
	return "Memory state: " + strings.Join(parts, ", ") + " (use claw_recent_memory, claw_list_reminders, claw_list_crons, claw_recall to read them before saying you don't know)."
}

func tailMemorySummaries(in []MemorySummary, n int) []string {
	if len(in) > n {
		in = in[len(in)-n:]
	}
	out := make([]string, 0, len(in))
	for _, item := range in {
		if text := strings.TrimSpace(item.Summary); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func tailStrings(in []string, n int) []string {
	if len(in) > n {
		in = in[len(in)-n:]
	}
	out := make([]string, 0, len(in))
	for _, item := range in {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func interviewTimeout(cfg config.Config) time.Duration {
	if cfg.Runtime.RequestTimeoutSeconds > 0 {
		return time.Duration(cfg.Runtime.RequestTimeoutSeconds) * time.Second
	}
	// 20s was tuned for hosted models. Local LM Studio with mid-tier
	// (35b-class) checkpoints regularly takes 60-120s per turn, so the
	// default needs to absorb that without surfacing a "context deadline
	// exceeded" each time. Users with hosted providers can override
	// via runtime.request_timeout_seconds in config.
	return 3 * time.Minute
}

func (s *Service) applyFallbackInterviewPrompt(prefix string) string {
	prompt := interviewQuestions[0].Prompt
	_ = s.store.Update(func(state *State) error {
		now := time.Now().UTC()
		state.Interview.Current = 0
		if strings.TrimSpace(prefix) != "" {
			appendAssistantTurn(state, prefix, now)
		}
		appendAssistantTurn(state, prompt, now)
		return nil
	})
	if strings.TrimSpace(prefix) == "" {
		return prompt
	}
	return prefix + " " + prompt
}

func (s *Service) applyFallbackInterviewAnswer(answer string, currentIndex int) (string, bool, error) {
	var (
		next string
		done bool
	)
	err := s.store.Update(func(state *State) error {
		now := time.Now().UTC()
		if currentIndex < 0 || currentIndex >= len(interviewQuestions) {
			currentIndex = 0
		}
		applyInterviewAnswer(state, interviewQuestions[currentIndex].Key, answer, now)
		state.Interview.Current = currentIndex + 1
		if state.Interview.Current >= len(interviewQuestions) {
			done = true
			state.Interview.Active = false
			state.Interview.CompletedAt = now
			next = "Interview complete. I updated my identity, soul, user profile, and memory seed. You can keep refining me from this Claw section anytime."
			appendAssistantTurn(state, next, now)
			state.Memory.Summaries = append(state.Memory.Summaries, MemorySummary{
				ID:        newID(),
				Source:    "interview",
				Summary:   "Initial Claw interview completed and core identity/user memory were personalized.",
				CreatedAt: now,
			})
			compactState(state)
			return nil
		}
		next = interviewQuestions[state.Interview.Current].Prompt
		appendAssistantTurn(state, next, now)
		compactState(state)
		return nil
	})
	return next, done, err
}

func splitList(text string) []string {
	parts := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	})
	var out []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		out = []string{strings.TrimSpace(text)}
	}
	return out
}

func cleanList(items []string) []string {
	var out []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
