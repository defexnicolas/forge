package plans

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"forge/internal/db"
)

const currentPlanID = "current"

// Document is the durable planning artifact. It describes intent and design;
// executable progress remains in internal/tasks.
type Document struct {
	ID          string    `json:"id"`
	Summary     string    `json:"summary"`
	Context     string    `json:"context,omitempty"`
	Assumptions []string  `json:"assumptions,omitempty"`
	Approach    string    `json:"approach,omitempty"`
	Stubs       []string  `json:"stubs,omitempty"`
	Risks       []string  `json:"risks,omitempty"`
	Validation  []string  `json:"validation,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Store struct {
	mu  sync.Mutex
	cwd string
	db  *sql.DB
}

func New(cwd string) *Store {
	s := &Store{cwd: cwd}
	handle, err := db.Open(cwd)
	if err != nil {
		return s
	}
	s.db = handle
	return s
}

func (s *Store) Path() string {
	return filepath.Join(s.cwd, ".forge", "forge.db")
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Save(doc Document) (Document, error) {
	if s.db == nil {
		return Document{}, fmt.Errorf("plans db unavailable")
	}
	doc = clean(doc)
	if doc.Summary == "" && doc.Approach == "" && len(doc.Stubs) == 0 && len(doc.Validation) == 0 {
		return Document{}, fmt.Errorf("plan_write requires at least summary, approach, stubs, or validation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	existing, ok, err := s.currentLocked()
	if err != nil {
		return Document{}, err
	}
	doc.ID = currentPlanID
	if ok {
		doc.CreatedAt = existing.CreatedAt
	} else {
		doc.CreatedAt = now
	}
	doc.UpdatedAt = now

	assumptions, err := encodeList(doc.Assumptions)
	if err != nil {
		return Document{}, err
	}
	stubs, err := encodeList(doc.Stubs)
	if err != nil {
		return Document{}, err
	}
	risks, err := encodeList(doc.Risks)
	if err != nil {
		return Document{}, err
	}
	validation, err := encodeList(doc.Validation)
	if err != nil {
		return Document{}, err
	}
	_, err = s.db.Exec(
		`INSERT INTO plans(id, summary, context, assumptions, approach, stubs, risks, validation, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		 summary = excluded.summary,
		 context = excluded.context,
		 assumptions = excluded.assumptions,
		 approach = excluded.approach,
		 stubs = excluded.stubs,
		 risks = excluded.risks,
		 validation = excluded.validation,
		 updated_at = excluded.updated_at`,
		doc.ID, doc.Summary, doc.Context, assumptions, doc.Approach, stubs, risks, validation,
		doc.CreatedAt.Format(time.RFC3339Nano), doc.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Document{}, fmt.Errorf("save plan: %w", err)
	}
	return doc, nil
}

func (s *Store) Clear() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM plans WHERE id = ?`, currentPlanID)
	if err != nil {
		return fmt.Errorf("clear plan: %w", err)
	}
	return nil
}

func (s *Store) Current() (Document, bool, error) {
	if s.db == nil {
		return Document{}, false, fmt.Errorf("plans db unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentLocked()
}

func (s *Store) currentLocked() (Document, bool, error) {
	row := s.db.QueryRow(`SELECT id, summary, context, assumptions, approach, stubs, risks, validation, created_at, updated_at FROM plans WHERE id = ?`, currentPlanID)
	var doc Document
	var assumptions, stubs, risks, validation, createdAt, updatedAt string
	err := row.Scan(&doc.ID, &doc.Summary, &doc.Context, &assumptions, &doc.Approach, &stubs, &risks, &validation, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return Document{}, false, nil
	}
	if err != nil {
		return Document{}, false, fmt.Errorf("get plan: %w", err)
	}
	doc.Assumptions = decodeList(assumptions)
	doc.Stubs = decodeList(stubs)
	doc.Risks = decodeList(risks)
	doc.Validation = decodeList(validation)
	doc.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	doc.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return doc, true, nil
}

func Format(doc Document) string {
	var b strings.Builder
	writeSection(&b, "Summary", []string{doc.Summary})
	writeSection(&b, "Context", []string{doc.Context})
	writeSection(&b, "Assumptions", doc.Assumptions)
	writeSection(&b, "Approach", []string{doc.Approach})
	writeSection(&b, "Stubs", doc.Stubs)
	writeSection(&b, "Risks", doc.Risks)
	writeSection(&b, "Validation", doc.Validation)
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "No plan yet."
	}
	return out
}

func clean(doc Document) Document {
	doc.Summary = strings.TrimSpace(doc.Summary)
	doc.Context = strings.TrimSpace(doc.Context)
	doc.Approach = strings.TrimSpace(doc.Approach)
	doc.Assumptions = cleanList(doc.Assumptions)
	doc.Stubs = cleanList(doc.Stubs)
	doc.Risks = cleanList(doc.Risks)
	doc.Validation = cleanList(doc.Validation)
	return doc
}

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func encodeList(items []string) (string, error) {
	if items == nil {
		items = []string{}
	}
	data, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("encode plan list: %w", err)
	}
	return string(data), nil
}

func decodeList(raw string) []string {
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	return cleanList(items)
}

func writeSection(b *strings.Builder, title string, items []string) {
	cleaned := cleanList(items)
	if len(cleaned) == 0 {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString(title)
	b.WriteString(":\n")
	if len(cleaned) == 1 {
		b.WriteString(cleaned[0])
		return
	}
	for _, item := range cleaned {
		b.WriteString("- ")
		b.WriteString(item)
		b.WriteByte('\n')
	}
}
