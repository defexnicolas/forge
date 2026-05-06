// Package db owns the SQLite connection for forge. It lives in
// .forge/forge.db and is the future home for sessions, messages, tool_calls,
// approvals, patches, context_items, agents, and skills per
// docs/ARCHITECTURE.md. For now, only tasks are migrated — the rest stays on
// JSON/JSONL until each piece is rewritten.
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open returns a connection to .forge/forge.db, creating the file and schema
// if needed. Safe to call multiple times: the migrations are idempotent.
func Open(cwd string) (*sql.DB, error) {
	dir := filepath.Join(cwd, ".forge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create .forge dir: %w", err)
	}
	path := filepath.Join(dir, "forge.db")
	// _txlock=immediate avoids surprise upgrades from deferred→exclusive when
	// a writer arrives during a read transaction.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_txlock=immediate"
	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}
	// Single writer at a time — SQLite with WAL allows many readers but only
	// one writer, and Go's sql pool can easily create contention without this.
	handle.SetMaxOpenConns(1)
	if err := migrate(handle); err != nil {
		handle.Close()
		return nil, err
	}
	return handle, nil
}

// migrate applies the current schema. Each migration is wrapped in a
// transaction and records itself in the schema_version table so future
// additions can be additive.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}
	for i, stmt := range migrations {
		version := i + 1
		var exists int
		if err := db.QueryRow("SELECT COUNT(*) FROM schema_version WHERE version = ?", version).Scan(&exists); err != nil {
			return fmt.Errorf("check version %d: %w", version, err)
		}
		if exists > 0 {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		if _, err := tx.Exec(stmt); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := tx.Exec("INSERT INTO schema_version(version) VALUES(?)", version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}
	return nil
}

var migrations = []string{
	// v1: tasks table — mirrors internal/tasks/Task.
	`CREATE TABLE tasks (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		status TEXT NOT NULL CHECK(status IN ('pending','in_progress','completed','cancelled')),
		notes TEXT DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	// v2: current rich planning document. Tasks remain the executable checklist.
	`CREATE TABLE plans (
		id TEXT PRIMARY KEY,
		summary TEXT NOT NULL DEFAULT '',
		context TEXT NOT NULL DEFAULT '',
		assumptions TEXT NOT NULL DEFAULT '[]',
		approach TEXT NOT NULL DEFAULT '',
		stubs TEXT NOT NULL DEFAULT '[]',
		risks TEXT NOT NULL DEFAULT '[]',
		validation TEXT NOT NULL DEFAULT '[]',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	// v3: one-row-per-repo cached project snapshot (structure, manifests, tech).
	// Populated lazily on first session so the model can answer structural
	// questions without re-walking the tree.
	`CREATE TABLE project_state (
		repo_root TEXT PRIMARY KEY,
		snapshot_json TEXT NOT NULL,
		summary TEXT NOT NULL DEFAULT '',
		git_head TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	// v4: granular task fields for the build-mode executor. target_files
	// names the files the task will touch; acceptance_criteria is the
	// concrete check that determines "done"; depends_on enforces ordering
	// across tasks. All three are optional (default empty JSON array / "")
	// so plan-mode output predating this migration loads as zero-values
	// and the existing pipeline keeps working. Each ALTER lives in its
	// own migration slot so a partial failure leaves a clean recoverable
	// state — SQLite's Exec only runs the first statement in a
	// multi-statement string under most drivers.
	`ALTER TABLE tasks ADD COLUMN target_files TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE tasks ADD COLUMN acceptance_criteria TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE tasks ADD COLUMN depends_on TEXT NOT NULL DEFAULT '[]'`,
}
