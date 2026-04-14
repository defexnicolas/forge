package yarn

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const yarnSchema = `
CREATE TABLE IF NOT EXISTS nodes (
	id         TEXT PRIMARY KEY,
	kind       TEXT NOT NULL,
	path       TEXT,
	summary    TEXT,
	content    TEXT,
	links      TEXT,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_nodes_kind ON nodes(kind);
`

// SQLiteStore stores YARN context nodes in SQLite.
type SQLiteStore struct {
	db   *sql.DB
	path string
	cwd  string
}

// NewSQLite creates or opens a SQLite-backed YARN store.
func NewSQLite(cwd string) (*SQLiteStore, error) {
	dir := filepath.Join(cwd, ".forge", "yarn")
	_ = os.MkdirAll(dir, 0o755)
	dbPath := filepath.Join(dir, "yarn.db")

	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(yarnSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("yarn sqlite schema: %w", err)
	}
	return &SQLiteStore{db: db, path: dbPath, cwd: cwd}, nil
}

func (s *SQLiteStore) Path() string { return s.path }

func (s *SQLiteStore) Upsert(node Node) error {
	if strings.TrimSpace(node.Kind) == "" {
		return fmt.Errorf("yarn node kind is required")
	}
	node.ID = stableID(node)
	node.UpdatedAt = time.Now().UTC()

	linksJSON := "[]"
	if len(node.Links) > 0 {
		data, _ := json.Marshal(node.Links)
		linksJSON = string(data)
	}

	_, err := s.db.Exec(
		`INSERT INTO nodes (id, kind, path, summary, content, links, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, path=excluded.path, summary=excluded.summary, content=excluded.content, links=excluded.links, updated_at=excluded.updated_at`,
		node.ID, node.Kind, node.Path, node.Summary, node.Content, linksJSON, node.UpdatedAt,
	)
	return err
}

func (s *SQLiteStore) Load() ([]Node, error) {
	rows, err := s.db.Query("SELECT id, kind, path, summary, content, links, updated_at FROM nodes ORDER BY updated_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		var n Node
		var linksJSON string
		var updatedAt string
		if err := rows.Scan(&n.ID, &n.Kind, &n.Path, &n.Summary, &n.Content, &linksJSON, &updatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(linksJSON), &n.Links)
		n.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		if n.UpdatedAt.IsZero() {
			n.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedAt)
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func (s *SQLiteStore) Select(query string, budgetBytes, limit int) ([]Node, error) {
	nodes, err := s.Load()
	if err != nil {
		return nil, err
	}
	queryTerms := terms(query)
	type scored struct {
		node  Node
		score int
	}
	scoredNodes := make([]scored, 0, len(nodes))
	for _, node := range nodes {
		score := scoreNode(node, queryTerms)
		if score == 0 && node.Kind == "instructions" {
			score = 1
		}
		if score == 0 {
			continue
		}
		scoredNodes = append(scoredNodes, scored{node: node, score: score})
	}
	sort.SliceStable(scoredNodes, func(i, j int) bool {
		if scoredNodes[i].score == scoredNodes[j].score {
			return scoredNodes[i].node.UpdatedAt.After(scoredNodes[j].node.UpdatedAt)
		}
		return scoredNodes[i].score > scoredNodes[j].score
	})
	if budgetBytes <= 0 {
		budgetBytes = 48000
	}
	if limit <= 0 {
		limit = 12
	}
	selected := make([]Node, 0, min(limit, len(scoredNodes)))
	used := 0
	for _, scored := range scoredNodes {
		if len(selected) >= limit {
			break
		}
		size := len(scored.node.Content) + len(scored.node.Summary) + len(scored.node.Path)
		if size == 0 {
			continue
		}
		if used > 0 && used+size > budgetBytes {
			continue
		}
		used += size
		selected = append(selected, scored.node)
	}
	return selected, nil
}

// Stats returns storage statistics.
func (s *SQLiteStore) Stats() string {
	var count int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count)
	return fmt.Sprintf("YARN SQLite: %d nodes (%s)", count, s.path)
}

// Close closes the database.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
