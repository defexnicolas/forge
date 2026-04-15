package projectstate

import (
	"database/sql"
	"errors"
	"time"
)

// Store persists project snapshots in the shared forge.db.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Get returns the snapshot for repoRoot, or (Snapshot{}, false, nil) if none.
func (s *Store) Get(repoRoot string) (Snapshot, bool, error) {
	if s == nil || s.db == nil {
		return Snapshot{}, false, nil
	}
	row := s.db.QueryRow(`SELECT snapshot_json, git_head FROM project_state WHERE repo_root = ?`, repoRoot)
	var raw, head string
	if err := row.Scan(&raw, &head); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Snapshot{}, false, nil
		}
		return Snapshot{}, false, err
	}
	snap, err := Unmarshal(raw)
	if err != nil {
		return Snapshot{}, false, err
	}
	if snap.GitHead == "" {
		snap.GitHead = head
	}
	return snap, true, nil
}

// Upsert inserts or replaces the snapshot for repoRoot.
func (s *Store) Upsert(snap Snapshot) error {
	if s == nil || s.db == nil {
		return errors.New("projectstate: nil store")
	}
	raw, err := snap.Marshal()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.Exec(`
		INSERT INTO project_state(repo_root, snapshot_json, summary, git_head, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_root) DO UPDATE SET
			snapshot_json = excluded.snapshot_json,
			summary       = excluded.summary,
			git_head      = excluded.git_head,
			updated_at    = excluded.updated_at
	`, snap.RepoRoot, raw, snap.Summary(), snap.GitHead, now, now)
	return err
}
