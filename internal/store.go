package internal

import (
	"database/sql"
	"errors"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

// ErrEntryNotFound is returned by Store.Get when no row exists for the
// namespace and name.
var ErrEntryNotFound = errors.New("envmagic: entry not found")

// Store is a SQLite-backed encrypted variable store.
type Store struct {
	db *sql.DB
}

// OpenStore opens (or creates) the SQLite database at path.
func OpenStore(path string) (*Store, error) {
	created := false
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		created = true
	}

	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database, path: %s, error: %w", path, err)
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping database, path: %s, error: %w", path, err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS env_vars (
			namespace  TEXT NOT NULL,
			name       TEXT NOT NULL,
			value      BLOB NOT NULL,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (namespace, name)
		)
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to create table, path: %s, error: %w", path, err)
	}

	if created {
		if err := os.Chmod(path, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "envmagic: warning: could not chmod %s to 0600: %v\n", path, err)
		}
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Set(namespace, name string, encrypted []byte) error {
	_, err := s.db.Exec(`
		INSERT INTO env_vars (namespace, name, value, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(namespace, name) DO UPDATE SET
			value = excluded.value,
			updated_at = CURRENT_TIMESTAMP
	`, namespace, name, encrypted)
	if err != nil {
		return fmt.Errorf("failed to set entry, namespace: %s, name: %s, error: %w", namespace, name, err)
	}
	return nil
}

func (s *Store) Get(namespace, name string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRow(
		`SELECT value FROM env_vars WHERE namespace = ? AND name = ?`,
		namespace, name,
	).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEntryNotFound
	}

	return data, err
}

func (s *Store) List(namespace string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT name FROM env_vars WHERE namespace = ? ORDER BY name`,
		namespace,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}

	return names, rows.Err()
}

func (s *Store) Delete(namespace, name string) (int64, error) {
	res, err := s.db.Exec(
		`DELETE FROM env_vars WHERE namespace = ? AND name = ?`,
		namespace, name,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Entry holds an encrypted variable retrieved from the store.
type Entry struct {
	Name string
	Enc  []byte
}

func (s *Store) GetAll(namespace string) ([]Entry, error) {
	rows, err := s.db.Query(
		`SELECT name, value FROM env_vars WHERE namespace = ? ORDER BY name`,
		namespace,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var entries []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Name, &e.Enc); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
