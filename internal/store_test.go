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
			want := fmt.Sprintf("store %s contains triggers or views; refusing to open", path)
			if err == nil || err.Error() != want {
				t.Fatalf("OpenStore: err=%v, want %q", err, want)
			}
		})
	}
}
