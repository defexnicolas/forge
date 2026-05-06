package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"forge/internal/agent"
)

type Store struct {
	mu     sync.Mutex
	cwd    string
	id     string
	dir    string
	events string
	// currentTurnID holds the TurnID of the in-flight agent turn so every
	// LogAgentEvent can stamp its record with that ID. Set when an
	// EventTurnStart arrives, cleared when EventTurnEnd is processed.
	// Empty between turns — events outside a turn (commands, side calls)
	// land with TurnID="" and are never filtered as part of a turn.
	currentTurnID string
	// turnEventBuffer accumulates the events of the in-flight turn so we
	// can classify the outcome at EventTurnEnd time without re-reading
	// the JSONL log. Cleared on EventTurnEnd. Capped indirectly by the
	// max-step cap of the runtime.
	turnEventBuffer []agent.Event
}

type Info struct {
	ID         string
	Dir        string
	CWD        string
	EventCount int
	UpdatedAt  time.Time
}

type Event struct {
	Time     time.Time       `json:"time"`
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ToolName string          `json:"tool_name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Summary  string          `json:"summary,omitempty"`
	Diff     string          `json:"diff,omitempty"`
	Error    string          `json:"error,omitempty"`
	// TurnID groups events that belong to the same agent turn so the
	// context renderer can drop everything from an aborted turn at once.
	// Empty string is treated as "ungrouped" (legacy events from before
	// turn tracking — never filtered).
	TurnID string `json:"turn_id,omitempty"`
	// Outcome is set only on EventTurnEnd records. Values: "completed"
	// (turn produced a final answer or finished a checklist), "aborted"
	// (narration loop, parse-failures-exhausted, max-steps cap — model
	// didn't actually fail at the user task), "failed" (real error like
	// repeated tool failure or policy denial). Used by contextEvents to
	// decide whether the turn's contents reach the next prompt.
	Outcome string `json:"outcome,omitempty"`
	// Transient marks events that the runtime knows are recovery noise:
	// parse retries, narration-loop cancels, soft-nudge stops. Filtered
	// out of context rendering even when their parent turn's outcome is
	// completed (the turn recovered, but the failed attempts shouldn't
	// pollute the next turn's prompt).
	Transient bool `json:"transient,omitempty"`
}

// Turn outcomes. Mirrored as constants so callers don't typo the string.
const (
	OutcomeCompleted = "completed"
	OutcomeAborted   = "aborted"
	OutcomeFailed    = "failed"
)

// Turn lifecycle event types. These are session-store-only sentinels —
// they do not have agent.Event* counterparts because the runtime
// constructs them directly via Store.LogTurnStart / LogTurnEnd.
const (
	EventTurnStart         = "turn_start"
	EventTurnEnd           = "turn_end"
	EventCompactedHistory  = "compacted_history"
)

func New(cwd string) (*Store, error) {
	id := time.Now().UTC().Format("20060102T150405Z")
	dir := filepath.Join(cwd, ".forge", "sessions", id)
	for suffix := 2; ; suffix++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		dir = filepath.Join(cwd, ".forge", "sessions", fmt.Sprintf("%s-%d", id, suffix))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	id = filepath.Base(dir)
	store := &Store{
		cwd:    cwd,
		id:     id,
		dir:    dir,
		events: filepath.Join(dir, "events.jsonl"),
	}
	if err := store.writeMeta(); err != nil {
		return nil, err
	}
	return store, nil
}

func Open(cwd, id string) (*Store, error) {
	if id == "" {
		return nil, fmt.Errorf("empty session id")
	}
	if id != filepath.Base(id) || strings.Contains(id, "..") {
		return nil, fmt.Errorf("invalid session id: %s", id)
	}
	dir := filepath.Join(cwd, ".forge", "sessions", id)
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("session path is not a directory: %s", dir)
	}
	store := &Store{
		cwd:    cwd,
		id:     id,
		dir:    dir,
		events: filepath.Join(dir, "events.jsonl"),
	}
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); os.IsNotExist(err) {
		if err := store.writeMeta(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func OpenLatest(cwd string) (*Store, error) {
	sessions, err := List(cwd, 1)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, fmt.Errorf("no previous sessions found")
	}
	return Open(cwd, sessions[0].ID)
}

func List(cwd string, limit int) ([]Info, error) {
	root := filepath.Join(cwd, ".forge", "sessions")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Info, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		store, err := Open(cwd, id)
		if err != nil {
			continue
		}
		info := Info{ID: id, Dir: store.Dir(), CWD: cwd}
		if stat, err := os.Stat(store.events); err == nil {
			info.UpdatedAt = stat.ModTime()
		}
		events, err := store.Tail(0)
		if err == nil {
			info.EventCount = len(events)
			if len(events) > 0 {
				info.UpdatedAt = events[len(events)-1].Time
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) ID() string {
	return s.id
}

func (s *Store) Dir() string {
	return s.dir
}

func (s *Store) LiveLogPath() string {
	return filepath.Join(s.dir, "live.log")
}

func (s *Store) LogUser(text string) error {
	_ = s.appendChatMD("## You\n\n" + strings.TrimRight(text, "\n") + "\n\n")
	liveErr := s.appendLiveLog("You", text)
	err := s.append(Event{Time: time.Now().UTC(), Type: "user", Text: text})
	if err != nil {
		return err
	}
	return liveErr
}

// AppendChatTurn writes a clean Q&A pair (the assistant reply) to chat.md,
// alongside the JSONL event log. The TUI calls this at end of turn with the
// accumulated assistant text. Empty replies are skipped.
func (s *Store) AppendChatTurn(assistantText string) error {
	text := strings.TrimSpace(assistantText)
	if text == "" {
		return nil
	}
	chatErr := s.appendChatMD("## Forge\n\n" + text + "\n\n---\n\n")
	liveErr := s.appendLiveLog("Forge", text)
	if chatErr != nil {
		return chatErr
	}
	return liveErr
}

func (s *Store) appendChatMD(content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, "chat.md")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

func (s *Store) LogCommand(text, result string) error {
	liveErr := s.appendLiveLog("Command "+text, result)
	err := s.append(Event{Time: time.Now().UTC(), Type: "command", Text: text, Summary: result})
	if err != nil {
		return err
	}
	return liveErr
}

func (s *Store) LogAgentEvent(event agent.Event) error {
	if event.Type == agent.EventModelProgress {
		return nil
	}
	// Turn lifecycle: stamp every event between EventTurnStart and
	// EventTurnEnd with the same TurnID so the next prompt can drop a
	// whole aborted turn at once. EventTurnEnd's outcome is classified
	// here from the buffered events of the turn — keeping the logic in
	// the session store means the runtime stays oblivious to context
	// hygiene and only emits start/end markers.
	switch event.Type {
	case agent.EventTurnStart:
		s.mu.Lock()
		s.currentTurnID = event.TurnID
		s.turnEventBuffer = nil
		s.mu.Unlock()
	case agent.EventTurnEnd:
		s.mu.Lock()
		buf := s.turnEventBuffer
		s.turnEventBuffer = nil
		// Classify outcome from the buffered events. Override the
		// caller-provided TurnOutcome only when the runtime didn't set
		// one (the default path).
		if event.TurnOutcome == "" {
			event.TurnOutcome = classifyTurnOutcome(buf)
		}
		s.currentTurnID = ""
		s.mu.Unlock()
	}
	record := Event{
		Time:        time.Now().UTC(),
		Type:        event.Type,
		Text:        event.Text,
		ToolName:    event.ToolName,
		Input:       event.Input,
		TurnID:      event.TurnID,
		Outcome:     event.TurnOutcome,
		Transient:   event.Transient,
	}
	// Stamp TurnID from the running counter when the runtime didn't set
	// one explicitly. This catches subagent emissions that come through
	// the events channel without thinking about turn IDs.
	if record.TurnID == "" {
		s.mu.Lock()
		record.TurnID = s.currentTurnID
		s.mu.Unlock()
	}
	if event.Result != nil {
		record.Summary = event.Result.Summary
	}
	if event.Approval != nil {
		record.Summary = event.Approval.Summary
		record.Diff = event.Approval.Diff
	}
	if event.Error != nil {
		record.Error = event.Error.Error()
	}
	// Buffer non-marker events so the EventTurnEnd handler above can
	// inspect them when classifying outcome. Bounded implicitly by the
	// runtime's max-step cap; explicit cap as a safety net.
	if event.Type != agent.EventTurnStart && event.Type != agent.EventTurnEnd {
		s.mu.Lock()
		if s.currentTurnID != "" && len(s.turnEventBuffer) < 1000 {
			s.turnEventBuffer = append(s.turnEventBuffer, event)
		}
		s.mu.Unlock()
	}
	liveErr := s.appendLiveLogForAgentEvent(event)
	err := s.append(record)
	if err != nil {
		return err
	}
	return liveErr
}

// classifyTurnOutcome inspects the events of a completed turn and decides
// whether it counts as completed, aborted, or failed. The classification
// is conservative: anything that didn't ALSO trip a real failure and
// reached a clean EventDone is "completed". Only the runtime's own
// "stopped: ..." / "narration loop detected" / "parse error (attempt
// N/N)" / "agent stopped after N steps" patterns mark a turn aborted.
// "tool X failed N times" patterns mark it failed.
func classifyTurnOutcome(buf []agent.Event) string {
	var lastError string
	for _, e := range buf {
		if e.Type == agent.EventError && e.Error != nil {
			lastError = e.Error.Error()
		}
	}
	if lastError == "" {
		return OutcomeCompleted
	}
	low := strings.ToLower(lastError)
	switch {
	case strings.Contains(low, "narration loop detected"),
		strings.Contains(low, "parse error (attempt "),
		strings.Contains(low, "consecutive read-only tool calls"),
		strings.Contains(low, "agent stopped after"),
		strings.Contains(low, "build response(s) in prose"),
		strings.Contains(low, "planner step(s) with no actionable progress"):
		return OutcomeAborted
	case strings.Contains(low, "failed") && strings.Contains(low, "times in a row"),
		strings.Contains(low, "denied by"),
		strings.Contains(low, "task ") && strings.Contains(low, "already failed"):
		return OutcomeFailed
	}
	// Default: an EventError happened but didn't match a known pattern.
	// Treat as completed since the turn may have recovered after the
	// error and produced useful work — better to keep the turn visible
	// than to silently hide it on a pattern miss.
	return OutcomeCompleted
}

func (s *Store) appendLiveLogForAgentEvent(event agent.Event) error {
	switch event.Type {
	case agent.EventAssistantDelta, agent.EventAssistantText, agent.EventModelProgress, agent.EventClearStreaming, agent.EventDone:
		return nil
	case agent.EventToolCall:
		input := strings.TrimSpace(string(event.Input))
		if input == "" {
			input = "{}"
		}
		return s.appendLiveLog("Tool call "+event.ToolName, input)
	case agent.EventToolResult:
		var b strings.Builder
		if event.Text != "" {
			b.WriteString(event.Text)
		} else if event.Result != nil && event.Result.Summary != "" {
			b.WriteString(event.Result.Summary)
		}
		if event.Result != nil {
			for _, block := range event.Result.Content {
				if strings.TrimSpace(block.Text) == "" {
					continue
				}
				if b.Len() > 0 {
					b.WriteString("\n\n")
				}
				b.WriteString(block.Text)
			}
			for _, artifact := range event.Result.Artifacts {
				if artifact.Path == "" {
					continue
				}
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString("artifact: ")
				b.WriteString(artifact.Path)
			}
		}
		return s.appendLiveLog("Tool result "+event.ToolName, b.String())
	case agent.EventError:
		if event.Error == nil {
			return nil
		}
		return s.appendLiveLog("Error", event.Error.Error())
	case agent.EventAskUser:
		if event.AskUser == nil {
			return nil
		}
		return s.appendLiveLog("Question", event.AskUser.Question)
	case agent.EventApproval:
		if event.Approval == nil {
			return s.appendLiveLog("Approval", "approval required")
		}
		text := event.Approval.Summary
		if event.Approval.Diff != "" {
			text += "\n\n" + event.Approval.Diff
		}
		return s.appendLiveLog("Approval "+event.Approval.ToolName, text)
	default:
		value := event.Text
		if value == "" && event.Error != nil {
			value = event.Error.Error()
		}
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return s.appendLiveLog(event.Type, value)
	}
}

func (s *Store) appendLiveLog(section, content string) error {
	content = strings.TrimRight(stripANSI(content), "\n")
	if strings.TrimSpace(content) == "" {
		return nil
	}
	section = strings.TrimSpace(stripANSI(section))
	if section == "" {
		section = "Log"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.OpenFile(s.LiveLogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = fmt.Fprintf(file, "## %s\n%s\n\n", section, content)
	return err
}

func (s *Store) Tail(limit int) ([]Event, error) {
	data, err := os.ReadFile(s.events)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	events := make([]Event, 0, len(lines))
	for _, line := range lines {
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (s *Store) ContextText(limit int) string {
	events, err := s.Tail(limit)
	if err != nil {
		return "Session history unavailable: " + err.Error()
	}
	if len(events) == 0 {
		return FormatTail(events)
	}
	events = contextEvents(events)
	return "Session summary:\n" + Summarize(events) + "\n\nRecent timeline:\n" + FormatTail(events)
}

func (s *Store) append(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.OpenFile(s.events, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func (s *Store) writeMeta() error {
	meta := map[string]string{
		"id":  s.id,
		"cwd": s.cwd,
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, "meta.json"), data, 0o644)
}

func FormatTail(events []Event) string {
	if len(events) == 0 {
		return "No session events yet."
	}
	var b strings.Builder
	for _, event := range events {
		label := event.Type
		if event.ToolName != "" {
			label += ":" + event.ToolName
		}
		value := event.Text
		if value == "" {
			value = event.Summary
		}
		if value == "" {
			value = event.Error
		}
		fmt.Fprintf(&b, "%s %s\n", event.Time.Format(time.RFC3339), label)
		if value != "" {
			fmt.Fprintf(&b, "%s\n", value)
		}
	}
	return strings.TrimSpace(b.String())
}

// contextEvents returns the events that should be rendered to the next
// turn's prompt. Two filters apply:
//
//  1. Drop the entire contents of any turn whose EventTurnEnd has
//     Outcome="aborted" — narration loops, parse-failures-exhausted, and
//     max-step caps are recovery noise that confuses the next turn into
//     "I was just stuck, better tread carefully" loops.
//  2. Drop individual Transient events even from completed turns —
//     parse retries and soft-nudge stops happen, the model recovers,
//     but their messages would still pollute the prompt if rendered.
//
// Events outside any turn (TurnID == "") are always preserved — those
// are commands and side calls that have no concept of being aborted.
// The on-disk JSONL log keeps everything for /sessions and /resume.
func contextEvents(events []Event) []Event {
	// Auto-compact replay: if there's an EventCompactedHistory marker,
	// drop everything before it (it's already absorbed into the
	// summary) and keep only the marker + everything after. The marker
	// itself renders into the timeline as a single "compacted history"
	// summary block — see FormatTail.
	lastCompactIdx := -1
	for i, e := range events {
		if e.Type == EventCompactedHistory {
			lastCompactIdx = i
		}
	}
	if lastCompactIdx >= 0 {
		events = events[lastCompactIdx:]
	}
	// First pass: build TurnID -> Outcome map.
	outcomes := map[string]string{}
	for _, e := range events {
		if e.Type == agent.EventTurnEnd && e.TurnID != "" {
			outcomes[e.TurnID] = e.Outcome
		}
	}
	out := make([]Event, 0, len(events))
	for _, event := range events {
		if event.TurnID != "" && outcomes[event.TurnID] == OutcomeAborted {
			continue
		}
		if event.Transient {
			continue
		}
		// Skip the marker events themselves — they're internal
		// bookkeeping, not useful in the rendered timeline.
		if event.Type == agent.EventTurnStart || event.Type == agent.EventTurnEnd {
			continue
		}
		out = append(out, compactPlanningEvent(event))
	}
	return out
}

// AutoCompact implements agent.AutoCompactor. Called by the runtime at
// the top of each turn before building the next prompt. Decides whether
// to compact based on the previous turn's token usage and the count of
// non-compacted events; if so, dispatches the summarizer subagent and
// records a synthetic EventCompactedHistory marker that future
// contextEvents() calls render in place of the absorbed events.
//
// Non-fatal: any failure returns an error to the caller (which logs it
// and proceeds with the un-compacted history). The session log on disk
// is never destructively rewritten — the marker only changes what the
// renderer shows; older events stay queryable via /sessions.
func (s *Store) AutoCompact(ctx context.Context, opts agent.AutoCompactOptions) error {
	if s == nil {
		return nil
	}
	// Decide whether to compact. Two triggers (any one fires it):
	//  - Token usage on the LAST turn was >= threshold * budget.
	//  - More than MaxEvents non-compacted events have accumulated.
	events, err := s.Tail(0)
	if err != nil {
		return fmt.Errorf("auto_compact: read tail: %w", err)
	}
	// Count events emitted AFTER the last EventCompactedHistory (if any).
	tailStart := 0
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == EventCompactedHistory {
			tailStart = i + 1
			break
		}
	}
	pending := events[tailStart:]
	tokenTrigger := false
	if opts.TokensBudget > 0 && opts.TokensUsed > 0 {
		tokenTrigger = float64(opts.TokensUsed) >= opts.Threshold*float64(opts.TokensBudget)
	}
	eventTrigger := opts.MaxEvents > 0 && len(pending) >= opts.MaxEvents
	if !tokenTrigger && !eventTrigger {
		return nil
	}
	if opts.Runner == nil {
		return fmt.Errorf("auto_compact: no SubagentRunner provided")
	}
	// Build the summarizer prompt from the pending events. We render
	// them through contextEvents first so aborted turns don't poison
	// the summary itself with their own noise — the summarizer should
	// only ever see "real" history.
	filtered := contextEvents(pending)
	if len(filtered) == 0 {
		return nil // nothing worth summarizing after filtering
	}
	transcript := FormatTail(filtered)
	if strings.TrimSpace(transcript) == "" {
		return nil
	}
	prompt := "Summarize the following session transcript into bullet points covering:\n" +
		"- What was actually done (files edited, tools succeeded, decisions made)\n" +
		"- What the user asked for (intent, constraints)\n" +
		"- Any open questions or pending tasks\n" +
		"Exclude: aborted attempts, parse retries, model self-talk, redundant tool re-reads.\n" +
		"Aim for ≤ 60 lines, ≤ 1500 characters. Pure factual recap, no narration.\n\n" +
		"=== TRANSCRIPT ===\n" + transcript
	result, err := opts.Runner(ctx, agent.SubagentRequest{
		Agent:  "summarizer",
		Prompt: prompt,
	})
	if err != nil {
		return fmt.Errorf("auto_compact: summarizer failed: %w", err)
	}
	summary := strings.TrimSpace(result.Summary)
	if summary == "" {
		// Fallback to content blocks if the result returned text but no
		// summary line.
		var b strings.Builder
		for _, c := range result.Content {
			if strings.TrimSpace(c.Text) != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(strings.TrimSpace(c.Text))
			}
		}
		summary = b.String()
	}
	if summary == "" {
		return fmt.Errorf("auto_compact: summarizer produced empty output")
	}
	// Append the marker. This event has no TurnID — it's session-scoped,
	// not turn-scoped. contextEvents passes EventCompactedHistory
	// through verbatim (it's not an aborted-turn event and not
	// transient). The summary lives in Text (FormatTail's primary
	// rendering field) so it shows up directly in the next prompt; the
	// raw count metadata goes in Summary for /sessions debug views.
	marker := Event{
		Time:    time.Now().UTC(),
		Type:    EventCompactedHistory,
		Text:    summary,
		Summary: fmt.Sprintf("compacted %d events into %d-char summary", len(pending), len(summary)),
	}
	if err := s.append(marker); err != nil {
		return fmt.Errorf("auto_compact: persist marker: %w", err)
	}
	return nil
}

// IsTransientErrorMessage reports whether a runtime error message
// represents recovery noise (parse retries, narration cancels, soft-nudge
// stops, max-step caps) rather than a real failure. Exposed so the
// runtime can mark events transient when emitting them, instead of
// relying on string matching here.
func IsTransientErrorMessage(msg string) bool {
	if msg == "" {
		return false
	}
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "narration loop detected"),
		strings.Contains(low, "parse error (attempt "),
		strings.Contains(low, "consecutive read-only tool calls"),
		strings.Contains(low, "agent stopped after"),
		strings.Contains(low, "build response(s) in prose"),
		strings.Contains(low, "planner step(s) with no actionable progress"):
		return true
	}
	return false
}

func compactPlanningEvent(event Event) Event {
	if !isPlanningArtifactTool(event.ToolName) {
		return event
	}
	event.Input = nil
	event.Diff = ""
	event.Text = ""
	switch event.Type {
	case agent.EventToolCall:
		event.Summary = "planning tool call compacted; use plan_get/task_list for current state"
	case agent.EventToolResult:
		event.Summary = "planning artifact updated; use plan_get/task_list for current state"
	}
	return event
}

func isPlanningArtifactTool(toolName string) bool {
	switch toolName {
	case "plan_write", "plan_get", "todo_write", "task_list":
		return true
	default:
		return false
	}
}

func Summarize(events []Event) string {
	if len(events) == 0 {
		return "No session events yet."
	}
	counts := map[string]int{}
	var lastUser, lastAssistant, lastTool string
	for _, event := range events {
		counts[event.Type]++
		value := event.Text
		if value == "" {
			value = event.Summary
		}
		value = oneLine(value)
		switch event.Type {
		case "user":
			lastUser = value
		case agent.EventAssistantText:
			lastAssistant = value
		case agent.EventToolCall, agent.EventToolResult:
			if event.ToolName != "" {
				lastTool = event.ToolName
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "events=%d users=%d assistant=%d tool_calls=%d tool_results=%d", len(events), counts["user"], counts[agent.EventAssistantText], counts[agent.EventToolCall], counts[agent.EventToolResult])
	if lastUser != "" {
		fmt.Fprintf(&b, "\nlast_user: %s", lastUser)
	}
	if lastAssistant != "" {
		fmt.Fprintf(&b, "\nlast_assistant: %s", lastAssistant)
	}
	if lastTool != "" {
		fmt.Fprintf(&b, "\nlast_tool: %s", lastTool)
	}
	return b.String()
}

func oneLine(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 240 {
		return text[:240] + "..."
	}
	return text
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !((s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z')) {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
