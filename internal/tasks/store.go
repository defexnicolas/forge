package tasks

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"forge/internal/db"
)

// Store is the persistence layer for the session plan. Backed by SQLite in
// .forge/forge.db (see internal/db). On first open, tasks previously stored
// in the legacy .forge/tasks/tasks.json file are imported once.
type Store struct {
	mu   sync.Mutex
	cwd  string
	db   *sql.DB
	path string // legacy JSON path, used only for one-time import
}

type Task struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Notes     string    `json:"notes,omitempty"`
	// TargetFiles names the files the task will touch. Build mode uses
	// this to skip re-reading files that were already read in explore /
	// plan and to scope its edit operations precisely. Empty array is a
	// valid zero value for tasks that came from older runs (pre-v4 schema)
	// or simple checklist items that fit in their title.
	TargetFiles []string `json:"target_files,omitempty"`
	// AcceptanceCriteria is the concrete verification step that determines
	// the task is done — typically a shell command (`go test ./pkg/x`) or
	// a grep assertion (`grep -c old src/file.go == 0`). The build mode
	// executor surfaces this back to the user as the success/fail signal
	// for the task.
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty"`
	// DependsOn lists task IDs that must complete before this one can
	// start. Used by the build-mode loop to refuse out-of-order execution
	// and by the TUI to render arrows in the checklist panel. Empty array
	// means "no dependencies" — pick by checklist position.
	DependsOn []string  `json:"depends_on,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func New(cwd string) *Store {
	s := &Store{
		cwd:  cwd,
		path: filepath.Join(cwd, ".forge", "tasks", "tasks.json"),
	}
	handle, err := db.Open(cwd)
	if err != nil {
		// SQLite failing to open is a hard problem worth surfacing; tests and
		// callers use New() without error handling, so we stash nil and make
		// every operation error out until the user resolves the DB.
		return s
	}
	s.db = handle
	s.importLegacyJSON()
	return s
}

func (s *Store) Path() string {
	if s.db == nil {
		return s.path
	}
	return filepath.Join(s.cwd, ".forge", "forge.db")
}

// Close releases the database handle. Safe to call on a nil/errored Store.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// CreateInput carries the field bag for Create / task_create. Using a struct
// keeps the call site readable as we add granular fields (target_files,
// acceptance_criteria, depends_on) without ballooning the positional arg
// list. The legacy two-arg Create is preserved as a thin wrapper for the
// existing call sites that still pass title+notes.
type CreateInput struct {
	Title              string
	Notes              string
	TargetFiles        []string
	AcceptanceCriteria string
	DependsOn          []string
}

func (s *Store) Create(title, notes string) (Task, error) {
	return s.CreateWith(CreateInput{Title: title, Notes: notes})
}

func (s *Store) CreateWith(in CreateInput) (Task, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return Task{}, fmt.Errorf("task title is required")
	}
	if s.db == nil {
		return Task{}, fmt.Errorf("tasks db unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.listLocked()
	if err != nil {
		return Task{}, err
	}
	now := time.Now().UTC()
	task := Task{
		ID:                 nextID(existing),
		Title:              title,
		Status:             "pending",
		Notes:              strings.TrimSpace(in.Notes),
		TargetFiles:        sanitizeStringList(in.TargetFiles),
		AcceptanceCriteria: strings.TrimSpace(in.AcceptanceCriteria),
		DependsOn:          sanitizeStringList(in.DependsOn),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := s.insertLocked(task); err != nil {
		return Task{}, err
	}
	return task, nil
}

// sanitizeStringList drops empty entries and trims surrounding whitespace.
// Returns nil for an empty result so the JSON-serialized form stays "[]"
// rather than a sparse list with empty strings.
func sanitizeStringList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if v := strings.TrimSpace(s); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// marshalJSONList encodes a string slice for SQLite storage. nil → "[]".
func marshalJSONList(in []string) string {
	if len(in) == 0 {
		return "[]"
	}
	data, err := json.Marshal(in)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// unmarshalJSONList is the inverse — silently swallows malformed payloads
// so a legacy/corrupted row doesn't break listing the whole table. The
// runtime treats a malformed entry as if it had no targets/deps.
func unmarshalJSONList(raw string) []string {
	if raw == "" || raw == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return sanitizeStringList(out)
}

func (s *Store) List() ([]Task, error) {
	if s.db == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *Store) Get(id string) (Task, error) {
	if s.db == nil {
		return Task{}, fmt.Errorf("tasks db unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked(id)
}

func (s *Store) Update(id, title, status, notes string) (Task, error) {
	if s.db == nil {
		return Task{}, fmt.Errorf("tasks db unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	resolvedID := strings.TrimSpace(id)
	if resolvedID == "" {
		var err error
		resolvedID, err = s.resolveTaskIDLocked(title)
		if err != nil {
			return Task{}, err
		}
	}
	task, err := s.getLocked(resolvedID)
	if err != nil {
		return Task{}, err
	}
	if t := strings.TrimSpace(title); t != "" {
		task.Title = t
	}
	if st := strings.TrimSpace(status); st != "" {
		task.Status = normalizeStatus(st)
	}
	if notes != "" {
		task.Notes = strings.TrimSpace(notes)
	}
	task.UpdatedAt = time.Now().UTC()
	_, err = s.db.Exec(
		`UPDATE tasks SET title = ?, status = ?, notes = ?, target_files = ?, acceptance_criteria = ?, depends_on = ?, updated_at = ? WHERE id = ?`,
		task.Title, task.Status, task.Notes,
		marshalJSONList(task.TargetFiles), task.AcceptanceCriteria, marshalJSONList(task.DependsOn),
		task.UpdatedAt.Format(time.RFC3339Nano), task.ID,
	)
	if err != nil {
		return Task{}, fmt.Errorf("update task: %w", err)
	}
	return task, nil
}

func (s *Store) resolveTaskIDLocked(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", fmt.Errorf("task_update requires id or title")
	}
	list, err := s.listLocked()
	if err != nil {
		return "", err
	}
	lower := strings.ToLower(title)
	for _, task := range list {
		if strings.EqualFold(strings.TrimSpace(task.Title), title) {
			return task.ID, nil
		}
	}
	var matches []Task
	for _, task := range list {
		if strings.Contains(strings.ToLower(strings.TrimSpace(task.Title)), lower) {
			matches = append(matches, task)
		}
	}
	if len(matches) == 1 {
		return matches[0].ID, nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("task_update title matched multiple tasks: %s", title)
	}
	return "", fmt.Errorf("task not found for title: %s", title)
}

// RichItem is the granular form of a checklist entry. Used by
// ReplacePlanRich and exposed via task_create's extended schema. Empty
// fields are tolerated and load as zero-values from older callers; the
// validation that REQUIRES non-empty TargetFiles or a path-shaped Title
// lives in the task tool wrapper, not here, so the store stays a dumb
// persistence layer.
type RichItem struct {
	Title              string
	Notes              string
	Status             string
	TargetFiles        []string
	AcceptanceCriteria string
	DependsOn          []string
}

// ReplacePlanRich is the structured form of ReplacePlan. The model can pass
// a list of {title, target_files, acceptance_criteria, depends_on, ...}
// objects via todo_write and have them persist with all the granular
// fields wired up. Existing string-array callers stay on ReplacePlan.
//
// Same empty-list guard as ReplacePlan: if the input is empty AND there
// are existing tasks, the call is rejected so an accidental empty
// regeneration doesn't wipe work in progress.
func (s *Store) ReplacePlanRich(items []RichItem) ([]Task, error) {
	if s.db == nil {
		return nil, fmt.Errorf("tasks db unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	out := make([]Task, 0, len(items))
	for _, in := range items {
		title := strings.TrimSpace(in.Title)
		if title == "" {
			continue
		}
		// Honor legacy status hints embedded in the title (e.g. "[x] foo")
		// so the rich form stays compatible with the markdown shorthand
		// the model sometimes emits.
		status, cleaned := parsePlanStatus(title)
		if cleaned == "" {
			continue
		}
		// Explicit Status field on the rich item wins over title-embedded
		// hints — that's the more deliberate channel.
		if s := strings.TrimSpace(in.Status); s != "" {
			status = normalizeStatus(s)
		}
		out = append(out, Task{
			ID:                 fmt.Sprintf("plan-%d", len(out)+1),
			Title:              cleaned,
			Status:             status,
			Notes:              strings.TrimSpace(in.Notes),
			TargetFiles:        sanitizeStringList(in.TargetFiles),
			AcceptanceCriteria: strings.TrimSpace(in.AcceptanceCriteria),
			DependsOn:          sanitizeStringList(in.DependsOn),
			CreatedAt:          now,
			UpdatedAt:          now,
		})
	}
	if len(out) == 0 {
		existing, _ := s.listLocked()
		if len(existing) > 0 {
			return existing, fmt.Errorf("refusing to replace %d existing tasks with empty list — use task_update for individual changes, or task_create to add; todo_write replaces the whole plan only when you pass the full new list", len(existing))
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin replace: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM tasks"); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("clear tasks: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO tasks(id, title, status, notes, target_files, acceptance_criteria, depends_on, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("prepare insert: %w", err)
	}
	for _, t := range out {
		if _, err := stmt.Exec(t.ID, t.Title, t.Status, t.Notes,
			marshalJSONList(t.TargetFiles), t.AcceptanceCriteria, marshalJSONList(t.DependsOn),
			t.CreatedAt.Format(time.RFC3339Nano), t.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
			stmt.Close()
			tx.Rollback()
			return nil, fmt.Errorf("insert %s: %w", t.ID, err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit replace: %w", err)
	}
	return out, nil
}

// Clear removes the current executable checklist. This is only for explicit
// user-driven reset flows; todo_write should continue to use ReplacePlan so
// accidental empty model output cannot erase work in progress.
func (s *Store) Clear() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec("DELETE FROM tasks"); err != nil {
		return fmt.Errorf("clear tasks: %w", err)
	}
	return nil
}

// ReplacePlan swaps the entire task list for a new one parsed from the model's
// todo_write payload. Guards against empty overwrites: if the model emits no
// valid items while tasks exist, we reject the rewrite so the panel never
// vanishes silently mid-session.
func (s *Store) ReplacePlan(items []string) ([]Task, error) {
	if s.db == nil {
		return nil, fmt.Errorf("tasks db unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	tasks := make([]Task, 0, len(items))
	for _, item := range items {
		title := strings.TrimSpace(item)
		if title == "" {
			continue
		}
		status, cleaned := parsePlanStatus(title)
		if cleaned == "" {
			continue
		}
		tasks = append(tasks, Task{
			ID:        fmt.Sprintf("plan-%d", len(tasks)+1),
			Title:     cleaned,
			Status:    status,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	if len(tasks) == 0 {
		existing, _ := s.listLocked()
		if len(existing) > 0 {
			return existing, fmt.Errorf("refusing to replace %d existing tasks with empty list — use task_update for individual changes, or task_create to add; todo_write replaces the whole plan only when you pass the full new list", len(existing))
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin replace: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM tasks"); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("clear tasks: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO tasks(id, title, status, notes, target_files, acceptance_criteria, depends_on, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("prepare insert: %w", err)
	}
	for _, t := range tasks {
		if _, err := stmt.Exec(t.ID, t.Title, t.Status, t.Notes,
			marshalJSONList(t.TargetFiles), t.AcceptanceCriteria, marshalJSONList(t.DependsOn),
			t.CreatedAt.Format(time.RFC3339Nano), t.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
			stmt.Close()
			tx.Rollback()
			return nil, fmt.Errorf("insert %s: %w", t.ID, err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit replace: %w", err)
	}
	return tasks, nil
}

func (s *Store) listLocked() ([]Task, error) {
	// Order strictly by insertion via SQLite's implicit rowid so that tasks
	// created in the same tick (e.g. a single ReplacePlan pass) still render
	// in the order they were inserted. Secondary created_at is a
	// belt-and-braces for legacy rows imported from JSON.
	rows, err := s.db.Query(`SELECT id, title, status, notes, target_files, acceptance_criteria, depends_on, created_at, updated_at FROM tasks ORDER BY rowid ASC, created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		var t Task
		var createdAt, updatedAt, targets, deps string
		if err := rows.Scan(&t.ID, &t.Title, &t.Status, &t.Notes, &targets, &t.AcceptanceCriteria, &deps, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		t.TargetFiles = unmarshalJSONList(targets)
		t.DependsOn = unmarshalJSONList(deps)
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *Store) getLocked(id string) (Task, error) {
	row := s.db.QueryRow(`SELECT id, title, status, notes, target_files, acceptance_criteria, depends_on, created_at, updated_at FROM tasks WHERE id = ?`, id)
	var t Task
	var createdAt, updatedAt, targets, deps string
	err := row.Scan(&t.ID, &t.Title, &t.Status, &t.Notes, &targets, &t.AcceptanceCriteria, &deps, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return Task{}, fmt.Errorf("task not found: %s", id)
	}
	if err != nil {
		return Task{}, fmt.Errorf("get task: %w", err)
	}
	t.TargetFiles = unmarshalJSONList(targets)
	t.DependsOn = unmarshalJSONList(deps)
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return t, nil
}

func (s *Store) insertLocked(t Task) error {
	_, err := s.db.Exec(
		`INSERT INTO tasks(id, title, status, notes, target_files, acceptance_criteria, depends_on, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Title, t.Status, t.Notes,
		marshalJSONList(t.TargetFiles), t.AcceptanceCriteria, marshalJSONList(t.DependsOn),
		t.CreatedAt.Format(time.RFC3339Nano), t.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	return nil
}

// importLegacyJSON is a one-shot migration from .forge/tasks/tasks.json. Runs
// whenever the tasks table is empty AND the legacy file exists, then renames
// the file so it doesn't re-import on the next boot.
func (s *Store) importLegacyJSON() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var tasks []Task
	if err := json.Unmarshal(data, &tasks); err != nil || len(tasks) == 0 {
		_ = os.Rename(s.path, s.path+".bak")
		return
	}
	var count int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM tasks").Scan(&count)
	if count > 0 {
		_ = os.Rename(s.path, s.path+".bak")
		return
	}
	for _, t := range tasks {
		if t.CreatedAt.IsZero() {
			t.CreatedAt = time.Now().UTC()
		}
		if t.UpdatedAt.IsZero() {
			t.UpdatedAt = t.CreatedAt
		}
		if t.Status == "" {
			t.Status = "pending"
		}
		_ = s.insertLocked(t)
	}
	_ = os.Rename(s.path, s.path+".bak")
}

func Format(tasks []Task) string {
	if len(tasks) == 0 {
		return "No tasks yet."
	}
	var b strings.Builder
	for _, task := range tasks {
		fmt.Fprintf(&b, "- [%s] %s %s", task.Status, task.ID, task.Title)
		if task.Notes != "" {
			fmt.Fprintf(&b, " - %s", task.Notes)
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func nextID(existing []Task) string {
	seen := map[string]bool{}
	for _, task := range existing {
		seen[task.ID] = true
	}
	for i := len(existing) + 1; ; i++ {
		id := fmt.Sprintf("task-%d", i)
		if !seen[id] {
			return id
		}
	}
}

// parsePlanStatus extracts a task status from common markdown/checkbox prefixes
// the model uses when re-emitting the plan via todo_write, and returns the cleaned title.
func parsePlanStatus(raw string) (status, cleaned string) {
	title := strings.TrimSpace(raw)
	status = "pending"

	lower := strings.ToLower(title)
	switch {
	case strings.Contains(title, "✅"), strings.Contains(lower, " - done"),
		strings.Contains(lower, "(done)"), strings.Contains(lower, "(completed)"),
		strings.HasSuffix(lower, " done"), strings.HasSuffix(lower, " completed"):
		status = "completed"
	case strings.Contains(lower, "(in_progress)"), strings.Contains(lower, "(in progress)"),
		strings.Contains(lower, "(doing)"), strings.Contains(lower, "(wip)"),
		strings.Contains(lower, " - wip"), strings.Contains(lower, " - in progress"):
		status = "in_progress"
	}

	prefixes := []struct {
		match  string
		status string
	}{
		{"[x] ", "completed"}, {"[X] ", "completed"},
		{"✅ ", "completed"}, {"✓ ", "completed"},
		{"[>] ", "in_progress"}, {"[~] ", "in_progress"}, {"[*] ", "in_progress"},
		{"→ ", "in_progress"}, {"➜ ", "in_progress"}, {"▶ ", "in_progress"},
		{"[ ] ", "pending"}, {"☐ ", "pending"}, {"☑ ", "completed"},
	}
	for _, p := range prefixes {
		if strings.HasPrefix(title, p.match) {
			title = strings.TrimPrefix(title, p.match)
			if status == "pending" {
				status = p.status
			}
			break
		}
	}

	for _, suffix := range []string{" - DONE", " - done", " - WIP", " - wip", " ✅", " ✓"} {
		title = strings.TrimSuffix(title, suffix)
	}
	cleaned = strings.TrimSpace(title)
	return status, cleaned
}

func normalizeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending", "in_progress", "completed", "cancelled":
		return strings.ToLower(strings.TrimSpace(status))
	case "done":
		return "completed"
	case "doing":
		return "in_progress"
	default:
		return "pending"
	}
}
