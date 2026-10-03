package envmagic_test

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/peteraba/envmagic"
	"github.com/peteraba/envmagic/internal"
)

func isolateKeyPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"XDG_CONFIG_HOME", "HOME", "AppData"} {
		t.Setenv(name, dir)
	}
	path, err := internal.KeyPath()
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(dir, path); err != nil || !filepath.IsLocal(rel) {
		t.Fatalf("key path %q is outside test directory %q: %v", path, dir, err)
	}
	return path
}

func TestOpenWithPath_RelativePath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	isolateKeyPath(t)
	c, err := envmagic.OpenWithPath(".envmagic")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := os.Stat(filepath.Join(dir, ".envmagic")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(envmagic.DefaultNamespace, "MISSING_VAR"); !errors.Is(err, envmagic.ErrNotFound) {
		t.Fatalf("Get: want ErrNotFound, got %v", err)
	}
}

func TestOpenWithPath_KeyCreated(t *testing.T) {
	isolateKeyPath(t)
	storePath := filepath.Join(t.TempDir(), ".envmagic")

	c1, err := envmagic.OpenWithPath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	_ = c1.Close()
	if !c1.KeyCreated() {
		t.Fatal("first open: want KeyCreated true (new key file)")
	}

	c2, err := envmagic.OpenWithPath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	_ = c2.Close()
	if c2.KeyCreated() {
		t.Fatal("second open: want KeyCreated false")
	}
}

func TestClient_Get_ErrNotFound(t *testing.T) {
	isolateKeyPath(t)
	storePath := filepath.Join(t.TempDir(), ".envmagic")

	c, err := envmagic.OpenWithPath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Get(envmagic.DefaultNamespace, "MISSING_VAR")
	if !errors.Is(err, envmagic.ErrNotFound) {
		t.Fatalf("Get: want ErrNotFound, got %v", err)
	}
}

func TestOpen_usesDotEnvmagicInCwd(t *testing.T) {
	dir := t.TempDir()
	isolateKeyPath(t)
	store := filepath.Join(dir, ".envmagic")

	c0, err := internal.OpenStore(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c0.Close() })
	key, _, err := internal.LoadOrCreateKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := internal.Encrypt(key, []byte("cwd value"), internal.AD(envmagic.DefaultNamespace, "KEY"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c0.Set(envmagic.DefaultNamespace, "KEY", enc); err != nil {
		t.Fatal(err)
	}
	_ = c0.Close()

	t.Chdir(dir)

	c, err := envmagic.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if got, err := c.Get(envmagic.DefaultNamespace, "KEY"); err != nil || got != "cwd value" {
		t.Fatalf("Open cwd store: got=%q err=%v", got, err)
	}
}

func TestOpenWithKeyAndPath(t *testing.T) {
	isolateKeyPath(t)
	dir := t.TempDir()
	keyPath, storePath := filepath.Join(dir, "custom-key"), filepath.Join(dir, "custom-store")
	key := bytes.Repeat([]byte{1}, 32)
	if err := internal.WriteKey(keyPath, key); err != nil {
		t.Fatal(err)
	}
	store, err := internal.OpenStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	enc, err := internal.Encrypt(key, []byte("custom value"), internal.AD("dev", "KEY"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("dev", "KEY", enc); err != nil {
		t.Fatal(err)
	}
	c, err := envmagic.OpenWithKeyAndPath(keyPath, storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if got, err := c.Get("dev", "KEY"); err != nil || got != "custom value" {
		t.Fatalf("Get: got=%q err=%v", got, err)
	}
}

func TestClient_Load(t *testing.T) {
	isolateKeyPath(t)
	storePath := filepath.Join(t.TempDir(), ".envmagic")
	client, err := envmagic.OpenWithPath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	key, _, err := internal.LoadOrCreateKey()
	if err != nil {
		t.Fatal(err)
	}
	store, err := internal.OpenStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	values := map[string]string{
		"A_ENVMAGIC_LOAD_TEST": "first value",
		"Z_ENVMAGIC_LOAD_TEST": "second value",
	}
	for name, value := range values {
		t.Setenv(name, "unchanged")
		enc, err := internal.Encrypt(key, []byte(value), internal.AD("dev", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Set("dev", name, enc); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := client.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(loaded)
	if want := []string{"A_ENVMAGIC_LOAD_TEST", "Z_ENVMAGIC_LOAD_TEST"}; !slices.Equal(loaded, want) {
		t.Errorf("Load names=%v, want %v", loaded, want)
	}
	for name, want := range values {
		if got := os.Getenv(name); got != want {
			t.Errorf("Load %s=%q, want %q", name, got, want)
		}
	}
}

func TestClient_CiphertextBinding(t *testing.T) {
	for _, tc := range []struct {
		label     string
		namespace string
		name      string
	}{
		{"name", "dev", "ENVMAGIC_OTHER"},
		{"namespace", "prd", "ENVMAGIC_TOKEN"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			isolateKeyPath(t)
			t.Setenv("ENVMAGIC_TOKEN", "unchanged")
			t.Setenv(tc.name, "unchanged")
			storePath := filepath.Join(t.TempDir(), ".envmagic")
			client, err := envmagic.OpenWithPath(storePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			key, _, err := internal.LoadOrCreateKey()
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", storePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			for _, row := range [][3]string{
				{"dev", "ENVMAGIC_TOKEN", "source secret"},
				{tc.namespace, tc.name, "target secret"},
			} {
				enc, err := internal.Encrypt(key, []byte(row[2]), internal.AD(row[0], row[1]))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO env_vars (namespace, name, value) VALUES (?, ?, ?)`, row[0], row[1], enc); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := client.Get(tc.namespace, tc.name); err != nil || got != "target secret" {
				t.Fatalf("Get before swap: got=%q err=%v", got, err)
			}
			if loaded, err := client.Load(tc.namespace); err != nil || len(loaded) == 0 || os.Getenv(tc.name) != "target secret" {
				t.Fatalf("Load before swap: loaded=%v err=%v value=%q", loaded, err, os.Getenv(tc.name))
			}
			t.Setenv(tc.name, "unchanged")
			res, err := db.Exec(`UPDATE env_vars SET value =
				(SELECT value FROM env_vars WHERE namespace = 'dev' AND name = 'ENVMAGIC_TOKEN')
				WHERE namespace = ? AND name = ?`, tc.namespace, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := res.RowsAffected(); err != nil || n != 1 {
				t.Fatalf("swap: affected=%d err=%v", n, err)
			}

			if got, err := client.Get(tc.namespace, tc.name); err == nil || got != "" || !strings.Contains(err.Error(), "message authentication failed") {
				t.Errorf("Get after swap: got=%q err=%v", got, err)
			}
			if loaded, err := client.Load(tc.namespace); err == nil || loaded != nil || !strings.Contains(err.Error(), "message authentication failed") {
				t.Errorf("Load after swap: loaded=%v err=%v", loaded, err)
			}
			if got := os.Getenv(tc.name); got != "unchanged" {
				t.Errorf("Load after swap changed target environment variable to %q", got)
			}
		})
	}
}

func TestClient_LegacyCiphertext(t *testing.T) {
	isolateKeyPath(t)
	const name = "ENVMAGIC_LEGACY"
	t.Setenv(name, "unchanged")
	storePath := filepath.Join(t.TempDir(), ".envmagic")
	client, err := envmagic.OpenWithPath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	key, _, err := internal.LoadOrCreateKey()
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	enc := gcm.Seal(nonce, nonce, []byte("legacy secret"), nil)
	db, err := sql.Open("sqlite", storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO env_vars (namespace, name, value) VALUES (?, ?, ?)`, envmagic.DefaultNamespace, name, enc); err != nil {
		t.Fatal(err)
	}

	const hint = "or stored by an older envmagic; re-import it (see README)"
	if got, err := client.Get(envmagic.DefaultNamespace, name); err == nil || got != "" || !strings.Contains(err.Error(), hint) {
		t.Errorf("Get legacy: got=%q err=%v, want no value and hint %q", got, err, hint)
	}
	if got := os.Getenv(name); got != "unchanged" {
		t.Errorf("Get legacy changed environment variable to %q", got)
	}
	if loaded, err := client.Load(envmagic.DefaultNamespace); err == nil || loaded != nil || !strings.Contains(err.Error(), hint) {
		t.Errorf("Load legacy: loaded=%v err=%v, want no names and hint %q", loaded, err, hint)
	}
	if got := os.Getenv(name); got != "unchanged" {
		t.Errorf("Load legacy changed environment variable to %q", got)
	}
}

func TestClient_Load_InvalidStoredName(t *testing.T) {
	isolateKeyPath(t)
	storePath := filepath.Join(t.TempDir(), ".envmagic")
	client, err := envmagic.OpenWithPath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	key, _, err := internal.LoadOrCreateKey()
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	names := []string{"A_ENVMAGIC_LOAD_TEST", "Z_ENVMAGIC_LOAD_TEST\n"}
	for _, name := range names {
		encrypted, err := internal.Encrypt(key, []byte("loaded"), internal.AD(envmagic.DefaultNamespace, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO env_vars (namespace, name, value) VALUES (?, ?, ?)`, envmagic.DefaultNamespace, name, encrypted); err != nil {
			t.Fatal(err)
		}
	}

	loaded, err := client.Load(envmagic.DefaultNamespace)
	want := fmt.Sprintf("invalid variable name %q in store", names[1])
	if err == nil || !strings.Contains(err.Error(), want) || loaded != nil {
		t.Errorf("Load: loaded=%v err=%v, want nil and %q", loaded, err, want)
	}
	for _, name := range names {
		if value, exists := os.LookupEnv(name); exists {
			t.Errorf("Load set %q to %q", name, value)
		}
	}
}
