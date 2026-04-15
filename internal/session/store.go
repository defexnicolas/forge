package session

import (
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
}

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
	record := Event{
		Time:     time.Now().UTC(),
		Type:     event.Type,
		Text:     event.Text,
		ToolName: event.ToolName,
		Input:    event.Input,
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
	liveErr := s.appendLiveLogForAgentEvent(event)
	err := s.append(record)
	if err != nil {
		return err
	}
	return liveErr
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
