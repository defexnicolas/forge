package tasks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Store struct {
	mu   sync.Mutex
	cwd  string
	path string
}

type Task struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Notes     string    `json:"notes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func New(cwd string) *Store {
	return &Store{
		cwd:  cwd,
		path: filepath.Join(cwd, ".forge", "tasks", "tasks.json"),
	}
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Create(title, notes string) (Task, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Task{}, fmt.Errorf("task title is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tasks, err := s.loadLocked()
	if err != nil {
		return Task{}, err
	}
	now := time.Now().UTC()
	task := Task{
		ID:        nextID(tasks),
		Title:     title,
		Status:    "pending",
		Notes:     strings.TrimSpace(notes),
		CreatedAt: now,
		UpdatedAt: now,
	}
	tasks = append(tasks, task)
	if err := s.writeLocked(tasks); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s *Store) List() ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) Get(id string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.loadLocked()
	if err != nil {
		return Task{}, err
	}
	for _, task := range tasks {
		if task.ID == id {
			return task, nil
		}
	}
	return Task{}, fmt.Errorf("task not found: %s", id)
}

func (s *Store) Update(id, title, status, notes string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.loadLocked()
	if err != nil {
		return Task{}, err
	}
	for i := range tasks {
		if tasks[i].ID != id {
			continue
		}
		if strings.TrimSpace(title) != "" {
			tasks[i].Title = strings.TrimSpace(title)
		}
		if strings.TrimSpace(status) != "" {
			tasks[i].Status = normalizeStatus(status)
		}
		if notes != "" {
			tasks[i].Notes = strings.TrimSpace(notes)
		}
		tasks[i].UpdatedAt = time.Now().UTC()
		if err := s.writeLocked(tasks); err != nil {
			return Task{}, err
		}
		return tasks[i], nil
	}
	return Task{}, fmt.Errorf("task not found: %s", id)
}

func (s *Store) ReplacePlan(items []string) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	tasks := make([]Task, 0, len(items))
	for _, item := range items {
		title := strings.TrimSpace(item)
		if title == "" {
			continue
		}
		// Detect completed items from model output (✅, DONE, [x], etc.)
		status := "pending"
		lower := strings.ToLower(title)
		if strings.Contains(title, "✅") || strings.Contains(lower, "- done") ||
			strings.HasPrefix(lower, "[x]") || strings.Contains(lower, "completed") {
			status = "completed"
		}
		// Clean up status markers from the title.
		title = strings.TrimLeft(title, "✅ ")
		title = strings.TrimSuffix(title, " - DONE")
		title = strings.TrimSuffix(title, " - done")
		title = strings.TrimPrefix(title, "[x] ")
		title = strings.TrimPrefix(title, "[X] ")
		title = strings.TrimSpace(title)
		if title == "" {
			continue
		}
		tasks = append(tasks, Task{
			ID:        fmt.Sprintf("plan-%d", len(tasks)+1),
			Title:     title,
			Status:    status,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	if err := s.writeLocked(tasks); err != nil {
		return nil, err
	}
	return tasks, nil
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

func (s *Store) loadLocked() ([]Task, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var tasks []Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s *Store) writeLocked(tasks []Task) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

func nextID(tasks []Task) string {
	seen := map[string]bool{}
	for _, task := range tasks {
		seen[task.ID] = true
	}
	for i := len(tasks) + 1; ; i++ {
		id := fmt.Sprintf("task-%d", i)
		if !seen[id] {
			return id
		}
	}
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
