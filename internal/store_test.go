package internal

import (
	"fmt"
	"path/filepath"
	"testing"
)

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
			names, err := store.List("default")
			if err == nil || err.Error() != want || names != nil {
				t.Errorf("List: names=%v err=%v, want nil and %q", names, err, want)
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
