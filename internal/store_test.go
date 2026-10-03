package internal

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenStorePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".envmagic")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode=%#o, want 0600", got)
	}
}

func TestOpenStoreRejectsDSNPragmaInjection(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "a?_pragma=writable_schema(1)&b=")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".envmagic")
	other := filepath.Join(base, "a")
	store, err := OpenStore(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	copyStore := func() {
		t.Helper()
		data, err := os.ReadFile(other)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	copyStore()
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var seq int
	var name, openedPath string
	if err := store.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &openedPath); err != nil {
		t.Fatal(err)
	}
	if openedPath != path {
		t.Errorf("opened %q, want %q", openedPath, path)
	}
	var writableSchema int
	if err := store.db.QueryRow(`PRAGMA writable_schema`).Scan(&writableSchema); err != nil {
		t.Fatal(err)
	}
	if writableSchema != 0 {
		t.Errorf("writable_schema = %d, want 0", writableSchema)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", "file:"+other)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA writable_schema=ON; INSERT INTO sqlite_master VALUES('table','t','env_vars',0,'CREATE TRIGGER t AFTER INSERT ON env_vars BEGIN INSERT OR REPLACE INTO env_vars(namespace,name,value) VALUES(NEW.namespace,''PROMPT_COMMAND'',NEW.value); END')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	copyStore()
	reopened, err := OpenStore(path)
	if reopened != nil {
		_ = reopened.Close()
		t.Fatal("OpenStore accepted a table-typed CREATE TRIGGER row")
	}
	if err == nil {
		t.Fatal("OpenStore returned no error for a table-typed CREATE TRIGGER row")
	}
}

func TestOpenStoreSpecialPathsPersist(t *testing.T) {
	for _, dirName := range []string{"a#b", "pct%41", "sp ace"} {
		t.Run(dirName, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), dirName)
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ".envmagic")
			store, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			var journalMode string
			if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
				t.Fatal(err)
			}
			if journalMode != "wal" {
				t.Errorf("journal_mode = %q, want wal", journalMode)
			}
			var foreignKeys int
			if err := store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
				t.Fatal(err)
			}
			if foreignKeys != 1 {
				t.Errorf("foreign_keys = %d, want 1", foreignKeys)
			}
			want := []byte("stored value")
			if err := store.Set("default", "KEY", want); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("requested file: %v", err)
			}
			reopened, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			got, err := reopened.Get("default", "KEY")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("Get = %q, want %q", got, want)
			}
		})
	}
}

func TestOpenStoreRelativePathsPersist(t *testing.T) {
	for _, path := range []string{".envmagic", "sub/.envmagic", "../x/.envmagic"} {
		t.Run(path, func(t *testing.T) {
			base := t.TempDir()
			cwd := filepath.Join(base, "cwd")
			if err := os.Mkdir(cwd, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Chdir(cwd)
			wantPath := filepath.Join(cwd, path)
			if err := os.MkdirAll(filepath.Dir(wantPath), 0o755); err != nil {
				t.Fatal(err)
			}
			store, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			var journalMode string
			if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
				t.Fatal(err)
			}
			if journalMode != "wal" {
				t.Errorf("journal_mode = %q, want wal", journalMode)
			}
			var foreignKeys int
			if err := store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
				t.Fatal(err)
			}
			if foreignKeys != 1 {
				t.Errorf("foreign_keys = %d, want 1", foreignKeys)
			}
			want := []byte("stored value")
			if err := store.Set("default", "KEY", want); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(wantPath); err != nil {
				t.Fatalf("requested file %q: %v", wantPath, err)
			}
			reopened, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			got, err := reopened.Get("default", "KEY")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("Get = %q, want %q", got, want)
			}
		})
	}
}

func TestOpenStoreRejectsNULPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".envmagic")
	store, err := OpenStore(path + "\x00suffix")
	if store != nil {
		_ = store.Close()
		t.Error("OpenStore returned a store for a path containing NUL")
	}
	if err == nil {
		t.Error("OpenStore returned no error for a path containing NUL")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("truncated path: stat error = %v, want file not to exist", err)
	}
}

func TestValidName(t *testing.T) {
	for _, name := range []string{"A", "_", "API_KEY", "_0", "A1"} {
		if !ValidName(name) {
			t.Errorf("ValidName(%q) = false", name)
		}
	}
	for _, name := range []string{"", "1A", "lowercase", "A-B", "A B", "Ä", "X\n", "X\x1b", "X\x00", "X=1; touch /tmp/x; #"} {
		if ValidName(name) {
			t.Errorf("ValidName(%q) = true", name)
		}
	}
}

func TestStoreInvalidNames(t *testing.T) {
	for label, name := range map[string]string{"shell": "X=1; touch /tmp/x; #", "newline": "X\n", "escape": "X\x1b"} {
		t.Run(label, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), ".envmagic"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if _, err := store.db.Exec(`INSERT INTO env_vars (namespace, name, value) VALUES ('default', 'A_VALID', X'00'), ('default', ?, X'00')`, name); err != nil {
				t.Fatal(err)
			}

			want := fmt.Sprintf("invalid variable name %q in store", name)
			entries, err := store.GetAll("default")
			if err == nil || err.Error() != want || entries != nil {
				t.Errorf("GetAll: entries=%v err=%v, want nil and %q", entries, err, want)
			}
		})
	}
}

func TestOpenStoreRejectsTriggersAndViews(t *testing.T) {
	for _, schema := range []struct {
		name string
		sql  string
	}{
		{"trigger", `CREATE TRIGGER plant_name AFTER INSERT ON env_vars BEGIN INSERT OR IGNORE INTO env_vars (namespace, name, value) VALUES (NEW.namespace, 'X=1; touch /tmp/x; #', NEW.value); END`},
		{"view", `CREATE VIEW stored_names AS SELECT name FROM env_vars`},
		{"uppercase trigger", `PRAGMA writable_schema=ON; INSERT INTO sqlite_master VALUES('TRIGGER','t','env_vars',0,'CREATE TRIGGER t AFTER INSERT ON env_vars BEGIN INSERT OR REPLACE INTO env_vars(namespace,name,value) VALUES(NEW.namespace,''PROMPT_COMMAND'',NEW.value); END'); PRAGMA writable_schema=OFF`},
		{"mixed-case view", `PRAGMA writable_schema=ON; INSERT INTO sqlite_master VALUES('View','stored_names','stored_names',0,'CREATE VIEW stored_names AS SELECT name FROM env_vars'); PRAGMA writable_schema=OFF`},
		{"NUL uppercase trigger", `PRAGMA writable_schema=ON; INSERT INTO sqlite_master VALUES('TRIGGER'||char(0),'t','env_vars',0,'CREATE TRIGGER t AFTER INSERT ON env_vars BEGIN INSERT OR REPLACE INTO env_vars(namespace,name,value) VALUES(NEW.namespace,''PROMPT_COMMAND'',NEW.value); END'); PRAGMA writable_schema=OFF`},
		{"NUL view", `PRAGMA writable_schema=ON; INSERT INTO sqlite_master VALUES('view'||char(0),'stored_names','stored_names',0,'CREATE VIEW stored_names AS SELECT name FROM env_vars'); PRAGMA writable_schema=OFF`},
		{"NUL table with trigger SQL", `PRAGMA writable_schema=ON; INSERT INTO sqlite_master VALUES('table'||char(0),'t','env_vars',0,'CREATE TRIGGER t AFTER INSERT ON env_vars BEGIN INSERT OR REPLACE INTO env_vars(namespace,name,value) VALUES(NEW.namespace,''PROMPT_COMMAND'',NEW.value); END'); PRAGMA writable_schema=OFF`},
	} {
		t.Run(schema.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".envmagic")
			store, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if _, err := store.db.Exec(schema.sql); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := OpenStore(path)
			if reopened != nil {
				_ = reopened.Close()
				t.Fatal("OpenStore returned a store containing " + schema.name)
			}
			if schema.name == "NUL table with trigger SQL" {
				// SQLite rejects the conflicting type and SQL before the schema guard.
				if err == nil {
					t.Fatal("OpenStore returned no error for " + schema.name)
				}
				return
			}
			want := fmt.Sprintf("store %s contains triggers, views or other schema objects; refusing to open", path)
			if err == nil || err.Error() != want {
				t.Fatalf("OpenStore: err=%v, want %q", err, want)
			}
		})
	}
}

func TestOpenStoreAllowsTablesAndIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".envmagic")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var indexes int
	if err := store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = 'env_vars'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 1 {
		t.Fatalf("env_vars indexes = %d, want 1 primary key index", indexes)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
}
