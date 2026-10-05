package internal

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStoreSetAll(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), ".envmagic"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	entries := []Entry{{Name: "A", Enc: []byte("new")}, {Name: "B", Enc: []byte("x")}}
	if err := store.SetAll("default", entries); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		got, err := store.Get("default", entry.Name)
		if err != nil || !bytes.Equal(got, entry.Enc) {
			t.Errorf("Get(%s) = %q, %v, want %q", entry.Name, got, err, entry.Enc)
		}
	}
}

func TestStoreSetAllRollback(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".envmagic")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`CREATE TABLE env_vars (
				namespace TEXT NOT NULL,
				name TEXT NOT NULL CHECK (name <> 'B'),
				value BLOB NOT NULL,
				updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (namespace, name)
			)`)
			_ = db.Close()
			if err != nil {
				t.Fatal(err)
			}
			store, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if existing {
				if err := store.Set("default", "A", []byte("old")); err != nil {
					t.Fatal(err)
				}
			}
			err = store.SetAll("default", []Entry{{Name: "A", Enc: []byte("new")}, {Name: "B", Enc: []byte("x")}})
			if err == nil || !strings.Contains(err.Error(), "name: B") {
				t.Fatalf("SetAll error = %v, want failing entry B", err)
			}
			got, err := store.Get("default", "A")
			if existing {
				if err != nil || string(got) != "old" {
					t.Errorf("Get(A) = %q, %v, want old", got, err)
				}
			} else if err != ErrEntryNotFound {
				t.Errorf("Get(A) = %q, %v, want ErrEntryNotFound", got, err)
			}
			if _, err := store.Get("default", "B"); err != ErrEntryNotFound {
				t.Errorf("Get(B) error = %v, want ErrEntryNotFound", err)
			}
			if err := store.Set("default", "C", []byte("c")); err != nil {
				t.Fatalf("Set(C) after rollback: %v", err)
			}
		})
	}
}

func TestStoreSetAllErrors(t *testing.T) {
	t.Run("commit", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".envmagic")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`CREATE TABLE parent (id TEXT PRIMARY KEY);
			CREATE TABLE env_vars (
				namespace TEXT NOT NULL,
				name TEXT NOT NULL REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED,
				value BLOB NOT NULL,
				updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (namespace, name)
			)`)
		_ = db.Close()
		if err != nil {
			t.Fatal(err)
		}
		store, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		if err := store.SetAll("default", []Entry{{Name: "A", Enc: []byte("new")}}); err == nil || !strings.Contains(err.Error(), "failed to commit") {
			t.Errorf("SetAll error = %v, want failed to commit", err)
		}
		if _, err := store.Get("default", "A"); err != ErrEntryNotFound {
			t.Errorf("Get(A) error = %v, want ErrEntryNotFound", err)
		}
	})
	t.Run("begin", func(t *testing.T) {
		store, err := OpenStore(filepath.Join(t.TempDir(), ".envmagic"))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if err := store.SetAll("default", []Entry{{Name: "A", Enc: []byte("new")}}); err == nil || !strings.Contains(err.Error(), "failed to begin") {
			t.Errorf("SetAll error = %v, want failed to begin", err)
		}
	})
}

func TestOpenStorePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions")
	}
	permissiveUmask(t)
	path := filepath.Join(t.TempDir(), ".envmagic")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Set("default", "KEY", []byte("value")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("%s: mode=%#o, want 0600", path, got)
	}
}

func assertNoSidecars(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Errorf("SQLite sidecar %s: stat error=%v, want file not to exist", suffix, err)
		}
	}
}

func TestOpenStoreUsesDeleteJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".envmagic")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "delete" {
		t.Errorf("journal_mode = %q, want delete", mode)
	}
	if err := store.Set("default", "KEY", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoSidecars(t, path)
}

const envVarsDDL = `CREATE TABLE env_vars (namespace TEXT NOT NULL, name TEXT NOT NULL, value BLOB NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (namespace, name))`

// walStore creates a cleanly closed WAL store at path and returns its bytes.
func walStore(t *testing.T, path, extra string) []byte {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{envVarsDDL, `INSERT INTO env_vars (namespace, name, value) VALUES ('default', 'KEY', x'0102')`, extra} {
		if q == "" {
			continue
		}
		if _, err := raw.Exec(q); err != nil {
			_ = raw.Close()
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// hotWALStore simulates a crashed writer: a db copy with a non-empty -wal and no -shm.
func hotWALStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".envmagic")
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	if _, err := raw.Exec(envVarsDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO env_vars (namespace, name, value) VALUES ('default', 'KEY', x'0102')`); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("want non-empty -wal, got %v, %v", info, err)
	}
	hot := filepath.Join(t.TempDir(), ".envmagic")
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hot+suffix, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return hot
}

func TestOpenStoreMigratesHotWAL(t *testing.T) {
	hot := hotWALStore(t)
	store, err := OpenStore(hot)
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "delete" {
		t.Errorf("journal_mode = %q, want delete", mode)
	}
	got, err := store.Get("default", "KEY")
	if err != nil || !bytes.Equal(got, []byte{1, 2}) {
		t.Errorf("Get = %v, %v, want [1 2]", got, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoSidecars(t, hot)
}

func TestOpenStoreBusyTimeout(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), ".envmagic"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var timeout int
	if err := store.db.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != 5000 {
		t.Fatalf("busy_timeout=%d, want 5000", timeout)
	}
}

func TestStoreSetWaitsForWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".envmagic")
	first, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	tx, err := first.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO env_vars (namespace, name, value) VALUES ('default', 'FIRST', 'value')`); err != nil {
		t.Fatal(err)
	}
	committed := make(chan error, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		committed <- tx.Commit()
	}()
	setErr := second.Set("default", "SECOND", []byte("value"))
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
	if setErr != nil {
		t.Fatalf("Set while another writer held the lock: %v", setErr)
	}
}

func TestOpenStoreRejectsForeignOwnerBeforeOpen(t *testing.T) {
	for _, uid := range []int{0, 1000} {
		t.Run(fmt.Sprintf("uid=%d", uid), func(t *testing.T) {
			path := hotWALStore(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			walBefore, err := os.ReadFile(path + "-wal")
			if err != nil {
				t.Fatal(err)
			}
			originalOwner, originalUID := fileOwner, currentUID
			t.Cleanup(func() { fileOwner, currentUID = originalOwner, originalUID })
			currentUID = func() int { return uid }
			fileOwner = func(os.FileInfo) (int, bool) { return 1001, true }
			store, err := OpenStore(path)
			if store != nil {
				_ = store.Close()
				t.Fatal("OpenStore returned a foreign-owned store")
			}
			want := fmt.Sprintf("store %s is owned by uid 1001, not by you (uid %d); refusing to open", path, uid)
			if err == nil || err.Error() != want {
				t.Fatalf("OpenStore: err=%v, want %q", err, want)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			walAfter, err := os.ReadFile(path + "-wal")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || !bytes.Equal(walBefore, walAfter) {
				t.Error("foreign-owned store or its -wal changed")
			}
			if _, err := os.Stat(path + "-shm"); !os.IsNotExist(err) {
				t.Errorf("-shm: stat error=%v, want file not to exist", err)
			}
		})
	}
}

func TestOpenStoreAllowsUnknownOwner(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".envmagic")
			if existing {
				store, err := OpenStore(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			originalOwner, originalUID := fileOwner, currentUID
			t.Cleanup(func() { fileOwner, currentUID = originalOwner, originalUID })
			currentUID = func() int { return 1000 }
			fileOwner = func(os.FileInfo) (int, bool) { return 1001, false }
			store, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if store == nil {
				t.Fatal("OpenStore returned no store for an unknown owner")
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenStoreRejectsForeignOwnerAfterCreate(t *testing.T) {
	for _, uid := range []int{0, 1000} {
		for _, foreignCheck := range []int{1, 2} {
			t.Run(fmt.Sprintf("uid=%d/foreignCheck=%d", uid, foreignCheck), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), ".envmagic")
				originalOwner, originalUID := fileOwner, currentUID
				t.Cleanup(func() { fileOwner, currentUID = originalOwner, originalUID })
				currentUID = func() int { return uid }
				checks := 0
				fileOwner = func(info os.FileInfo) (int, bool) {
					checks++
					storeInfo, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					if checks == foreignCheck && os.SameFile(info, storeInfo) {
						return 1001, true
					}
					return uid, true
				}
				store, err := OpenStore(path)
				if store != nil {
					_ = store.Close()
				}
				if checks != foreignCheck {
					t.Fatalf("owner checks=%d, want %d", checks, foreignCheck)
				}
				if store != nil {
					t.Fatal("OpenStore returned a foreign-owned store after creation")
				}
				want := fmt.Sprintf("store %s is owned by uid 1001, not by you (uid %d); refusing to open", path, uid)
				if err == nil || err.Error() != want {
					t.Fatalf("OpenStore: err=%v, want %q", err, want)
				}
			})
		}
	}
}

func TestOpenStoreRejectsForeignOwnerAfterOpen(t *testing.T) {
	for _, name := range []string{"direct", "symlink"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".envmagic")
			target := path
			if name == "symlink" {
				target = filepath.Join(dir, "target")
			}
			before := walStore(t, target, "")
			if name == "symlink" {
				if err := os.Symlink(target, path); err != nil {
					if runtime.GOOS == "windows" {
						t.Skipf("symlinks unavailable: %v", err)
					}
					t.Fatal(err)
				}
			}
			originalOwner, originalUID := fileOwner, currentUID
			t.Cleanup(func() { fileOwner, currentUID = originalOwner, originalUID })
			currentUID = func() int { return 1000 }
			checks := 0
			fileOwner = func(info os.FileInfo) (int, bool) {
				checks++
				storeInfo, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if checks == 2 && os.SameFile(info, storeInfo) {
					return 1001, true
				}
				return 1000, true
			}
			store, err := OpenStore(path)
			if store != nil {
				_ = store.Close()
				t.Fatal("OpenStore returned a foreign-owned store after opening")
			}
			want := fmt.Sprintf("store %s is owned by uid 1001, not by you (uid 1000); refusing to open", path)
			if err == nil || err.Error() != want {
				t.Fatalf("OpenStore: err=%v, want %q", err, want)
			}
			if checks != 2 {
				t.Fatalf("owner checks=%d, want 2", checks)
			}
			if after, err := os.ReadFile(target); err != nil || !bytes.Equal(before, after) {
				t.Errorf("store rejected after open was rewritten (read err=%v)", err)
			}
		})
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
	for _, dirName := range []string{"a?b", "a#b", "pct%41", "sp ace"} {
		t.Run(dirName, func(t *testing.T) {
			if runtime.GOOS == "windows" && dirName == "a?b" {
				t.Skip("? is not a valid Windows file name character")
			}
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
			if journalMode != "delete" {
				t.Errorf("journal_mode = %q, want delete", journalMode)
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
			if journalMode != "delete" {
				t.Errorf("journal_mode = %q, want delete", journalMode)
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

func TestOpenStoreRejectsViewInWALStoreUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".envmagic")
	before := walStore(t, path, `CREATE VIEW stored_names AS SELECT name FROM env_vars`)
	store, err := OpenStore(path)
	if store != nil {
		_ = store.Close()
		t.Fatal("OpenStore returned a store containing a view")
	}
	if err == nil || !strings.Contains(err.Error(), "refusing to open") {
		t.Fatalf("OpenStore: err=%v, want refusal", err)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
		t.Errorf("rejected WAL store was rewritten (read err=%v)", err)
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

func journalMode(t *testing.T, db *sql.DB) string {
	t.Helper()
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	return mode
}

func TestOpenStoreToleratesWALHeldElsewhere(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".envmagic")
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	if _, err := raw.Exec(`CREATE TABLE env_vars (namespace TEXT NOT NULL, name TEXT NOT NULL, value BLOB NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (namespace, name))`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO env_vars (namespace, name, value) VALUES ('default', 'KEY', x'0102')`); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore while another connection is open: %v", err)
	}
	if got, err := store.Get("default", "KEY"); err != nil || !bytes.Equal(got, []byte{1, 2}) {
		t.Errorf("Get = %v, %v, want [1 2]", got, err)
	}
	if mode := journalMode(t, store.db); mode != "wal" {
		t.Errorf("journal_mode = %q, want wal while held elsewhere", mode)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO env_vars (namespace, name, value) VALUES ('default', 'K2', x'03')`); err != nil {
		t.Fatalf("other connection broken: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := journalMode(t, store.db); mode != "delete" {
		t.Errorf("journal_mode = %q, want delete after later open", mode)
	}
	if got, err := store.Get("default", "K2"); err != nil || !bytes.Equal(got, []byte{3}) {
		t.Errorf("Get = %v, %v, want [3]", got, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoSidecars(t, path)
}

func TestOpenStoreConcurrentOpensFromWAL(t *testing.T) {
	const workers, rounds = 8, 25
	path := filepath.Join(t.TempDir(), ".envmagic")
	for round := 0; round < rounds; round++ {
		raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=journal_mode(WAL)")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS t (x)`); err != nil {
			t.Fatal(err)
		}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		errs := make(chan error, workers)
		for i := 0; i < workers; i++ {
			go func() {
				<-start
				store, err := OpenStore(path)
				if err == nil {
					err = store.Close()
				}
				errs <- err
			}()
		}
		close(start)
		for i := 0; i < workers; i++ {
			if err := <-errs; err != nil {
				t.Fatalf("round %d: concurrent OpenStore: %v", round, err)
			}
		}
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := journalMode(t, store.db); mode != "delete" {
		t.Errorf("journal_mode = %q, want delete", mode)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoSidecars(t, path)
}

func TestOpenStoreNewConnectionsStayDelete(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), ".envmagic"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	var mode string
	for i := 0; i < 2; i++ {
		conn, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "delete" {
			t.Errorf("connection %d: journal_mode = %q, %v, want delete", i, mode, err)
		}
	}
}

func skipUnlessChmodWorks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs enforced unix file permissions")
	}
}

func TestOpenStoreReadOnlyWALStore(t *testing.T) {
	skipUnlessChmodWorks(t)
	path := filepath.Join(t.TempDir(), ".envmagic")
	walStore(t, path, "")
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore on read-only WAL store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if got, err := store.Get("default", "KEY"); err != nil || !bytes.Equal(got, []byte{1, 2}) {
		t.Errorf("Get = %v, %v, want [1 2]", got, err)
	}
}

func TestOpenStoreReadOnlyDeleteStore(t *testing.T) {
	skipUnlessChmodWorks(t)
	path := filepath.Join(t.TempDir(), ".envmagic")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("default", "KEY", []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore on read-only DELETE store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if got, err := store.Get("default", "KEY"); err != nil || !bytes.Equal(got, []byte{1, 2}) {
		t.Errorf("Get = %v, %v, want [1 2]", got, err)
	}
}
