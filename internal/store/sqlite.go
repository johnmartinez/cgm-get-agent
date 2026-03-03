// Package store provides SQLite-backed persistence for meals, exercise sessions,
// and a glucose EGV cache. It uses versioned migrations applied on startup.
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Store wraps a SQLite database and exposes all persistence operations.
type Store struct {
	db *sql.DB
}

// migrations is the ordered list of SQL migration scripts.
// Append new entries to add schema versions; never edit existing ones.
var migrations = []string{
	// v1: initial schema
	`
	CREATE TABLE IF NOT EXISTS meals (
		id          TEXT PRIMARY KEY,
		description TEXT NOT NULL,
		carbs_est   REAL,
		protein_est REAL,
		fat_est     REAL,
		timestamp   TEXT NOT NULL,
		logged_at   TEXT NOT NULL,
		notes       TEXT
	);

	CREATE TABLE IF NOT EXISTS exercise (
		id           TEXT PRIMARY KEY,
		type         TEXT NOT NULL,
		duration_min INTEGER NOT NULL,
		intensity    TEXT NOT NULL,
		timestamp    TEXT NOT NULL,
		logged_at    TEXT NOT NULL,
		notes        TEXT
	);

	CREATE TABLE IF NOT EXISTS glucose_cache (
		record_id    TEXT PRIMARY KEY,
		system_time  TEXT NOT NULL,
		display_time TEXT NOT NULL,
		value        INTEGER NOT NULL,
		trend        TEXT NOT NULL,
		trend_rate   REAL,
		raw_json     TEXT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_meals_timestamp        ON meals (timestamp);
	CREATE INDEX IF NOT EXISTS idx_exercise_timestamp     ON exercise (timestamp);
	CREATE INDEX IF NOT EXISTS idx_glucose_cache_systime  ON glucose_cache (system_time);
	`,
}

// Open opens (or creates) the SQLite database at path, enables WAL mode and
// foreign keys, and runs any pending migrations. Returns a ready-to-use Store.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("store: opening db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("store: pinging db: %w", err)
	}
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: running migrations: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Ping checks that the database is still reachable.
func (s *Store) Ping() error {
	return s.db.Ping()
}

func runMigrations(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS migrations (
			version    INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("creating migrations table: %w", err)
	}

	var current int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM migrations`).Scan(&current); err != nil {
		return fmt.Errorf("reading migration version: %w", err)
	}

	for i, ddl := range migrations {
		version := i + 1
		if version <= current {
			continue
		}
		if _, err := db.Exec(ddl); err != nil {
			return fmt.Errorf("applying migration v%d: %w", version, err)
		}
		_, err := db.Exec(
			`INSERT INTO migrations (version, applied_at) VALUES (?, ?)`,
			version, time.Now().UTC().Format(time.RFC3339),
		)
		if err != nil {
			return fmt.Errorf("recording migration v%d: %w", version, err)
		}
	}
	return nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}
