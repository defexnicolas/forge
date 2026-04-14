package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"forge/internal/agent"

	_ "modernc.org/sqlite"
)

// SQLiteStore implements session persistence using SQLite.
type SQLiteStore struct {
	mu   sync.Mutex
	db   *sql.DB
	id   string
	cwd  string
	path string
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
	id         TEXT PRIMARY KEY,
	cwd        TEXT NOT NULL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS messages (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id TEXT NOT NULL REFERENCES sessions(id),
	time       DATETIME NOT NULL,
	type       TEXT NOT NULL,
	text       TEXT,
	tool_name  TEXT,
	input      TEXT,
	summary    TEXT,
	diff       TEXT,
	error      TEXT
);

CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, time);

CREATE TABLE IF NOT EXISTS tool_calls (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id TEXT NOT NULL REFERENCES sessions(id),
	time       DATETIME NOT NULL,
	tool_name  TEXT NOT NULL,
	input      TEXT,
	result     TEXT,
	approved   INTEGER,
	duration_ms INTEGER
);

CREATE INDEX IF NOT EXISTS idx_tool_calls_session ON tool_calls(session_id, time);

CREATE TABLE IF NOT EXISTS context_items (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id TEXT NOT NULL REFERENCES sessions(id),
	kind       TEXT NOT NULL,
	path       TEXT,
	content    TEXT,
	tokens     INTEGER
);

CREATE TABLE IF NOT EXISTS tasks (
	id         TEXT NOT NULL,
	session_id TEXT NOT NULL REFERENCES sessions(id),
	title      TEXT NOT NULL,
	status     TEXT DEFAULT 'pending',
	notes      TEXT,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (session_id, id)
);
`

// NewSQLite creates or opens a SQLite session store.
func NewSQLite(cwd string) (*SQLiteStore, error) {
	dir := filepath.Join(cwd, ".forge")
	_ = os.MkdirAll(dir, 0o755)
	dbPath := filepath.Join(dir, "sessions.db")

	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite schema: %w", err)
	}

	id := time.Now().UTC().Format("20060102T150405Z")
	if _, err := db.Exec("INSERT INTO sessions (id, cwd) VALUES (?, ?)", id, cwd); err != nil {
		// ID collision — add suffix.
		for suffix := 2; suffix < 100; suffix++ {
			candidate := fmt.Sprintf("%s-%d", id, suffix)
			if _, err := db.Exec("INSERT INTO sessions (id, cwd) VALUES (?, ?)", candidate, cwd); err == nil {
				id = candidate
				break
			}
		}
	}

	return &SQLiteStore{db: db, id: id, cwd: cwd, path: dbPath}, nil
}

// OpenSQLite opens an existing SQLite session.
func OpenSQLite(cwd, sessionID string) (*SQLiteStore, error) {
	dbPath := filepath.Join(cwd, ".forge", "sessions.db")
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	var exists int
	if err := db.QueryRow("SELECT 1 FROM sessions WHERE id = ?", sessionID).Scan(&exists); err != nil {
		db.Close()
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	return &SQLiteStore{db: db, id: sessionID, cwd: cwd, path: dbPath}, nil
}

// OpenLatestSQLite opens the most recent session.
func OpenLatestSQLite(cwd string) (*SQLiteStore, error) {
	dbPath := filepath.Join(cwd, ".forge", "sessions.db")
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	var id string
	if err := db.QueryRow("SELECT id FROM sessions ORDER BY created_at DESC LIMIT 1").Scan(&id); err != nil {
		db.Close()
		return nil, fmt.Errorf("no sessions found")
	}
	return &SQLiteStore{db: db, id: id, cwd: cwd, path: dbPath}, nil
}

func (s *SQLiteStore) ID() string  { return s.id }
func (s *SQLiteStore) Dir() string { return filepath.Dir(s.path) }

func (s *SQLiteStore) LogUser(text string) error {
	return s.appendMessage(Event{Time: time.Now().UTC(), Type: "user", Text: text})
}

func (s *SQLiteStore) LogCommand(text, result string) error {
	return s.appendMessage(Event{Time: time.Now().UTC(), Type: "command", Text: text, Summary: result})
}

func (s *SQLiteStore) LogAgentEvent(event agent.Event) error {
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
	return s.appendMessage(record)
}

func (s *SQLiteStore) appendMessage(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	inputStr := ""
	if len(event.Input) > 0 {
		inputStr = string(event.Input)
	}

	_, err := s.db.Exec(
		`INSERT INTO messages (session_id, time, type, text, tool_name, input, summary, diff, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.id, event.Time, event.Type, event.Text, event.ToolName, inputStr, event.Summary, event.Diff, event.Error,
	)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec("UPDATE sessions SET updated_at = CURRENT_TIMESTAMP WHERE id = ?", s.id)
	return nil
}

func (s *SQLiteStore) Tail(limit int) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := "SELECT time, type, text, tool_name, input, summary, diff, error FROM messages WHERE session_id = ? ORDER BY time ASC"
	if limit > 0 {
		query = fmt.Sprintf("SELECT * FROM (SELECT time, type, text, tool_name, input, summary, diff, error FROM messages WHERE session_id = ? ORDER BY time DESC LIMIT %d) ORDER BY time ASC", limit)
	}

	rows, err := s.db.Query(query, s.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var inputStr, toolName, summary, diff, errStr sql.NullString
		if err := rows.Scan(&e.Time, &e.Type, &e.Text, &toolName, &inputStr, &summary, &diff, &errStr); err != nil {
			return nil, err
		}
		e.ToolName = toolName.String
		if inputStr.Valid {
			e.Input = json.RawMessage(inputStr.String)
		}
		e.Summary = summary.String
		e.Diff = diff.String
		e.Error = errStr.String
		events = append(events, e)
	}
	return events, nil
}

func (s *SQLiteStore) ContextText(limit int) string {
	events, err := s.Tail(limit)
	if err != nil {
		return "Session history unavailable: " + err.Error()
	}
	if len(events) == 0 {
		return FormatTail(events)
	}
	return "Session summary:\n" + Summarize(events) + "\n\nRecent timeline:\n" + FormatTail(events)
}

// ListSQLite returns all sessions from SQLite, ordered by most recent.
func ListSQLite(cwd string, limit int) ([]Info, error) {
	dbPath := filepath.Join(cwd, ".forge", "sessions.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	query := "SELECT s.id, s.cwd, s.updated_at, COUNT(m.id) FROM sessions s LEFT JOIN messages m ON m.session_id = s.id GROUP BY s.id ORDER BY s.updated_at DESC"
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Info
	for rows.Next() {
		var info Info
		var updatedAt string
		if err := rows.Scan(&info.ID, &info.CWD, &updatedAt, &info.EventCount); err != nil {
			return nil, err
		}
		info.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		if info.UpdatedAt.IsZero() {
			info.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedAt)
		}
		info.Dir = filepath.Dir(dbPath)
		out = append(out, info)
	}
	return out, nil
}

// LogToolCall records a tool call with timing and approval status.
func (s *SQLiteStore) LogToolCall(toolName string, input json.RawMessage, result string, approved bool, durationMs int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	approvedInt := 0
	if approved {
		approvedInt = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO tool_calls (session_id, time, tool_name, input, result, approved, duration_ms) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.id, time.Now().UTC(), toolName, string(input), result, approvedInt, durationMs,
	)
	return err
}

// Close closes the SQLite database.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// Stats returns session statistics.
func (s *SQLiteStore) Stats() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var msgCount, toolCount int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = ?", s.id).Scan(&msgCount)
	_ = s.db.QueryRow("SELECT COUNT(*) FROM tool_calls WHERE session_id = ?", s.id).Scan(&toolCount)

	var b strings.Builder
	fmt.Fprintf(&b, "session: %s\n", s.id)
	fmt.Fprintf(&b, "storage: SQLite (%s)\n", s.path)
	fmt.Fprintf(&b, "messages: %d\n", msgCount)
	fmt.Fprintf(&b, "tool_calls: %d\n", toolCount)
	return b.String()
}
