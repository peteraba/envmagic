package internal

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func isolateKeyPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"XDG_CONFIG_HOME", "HOME", "AppData"} {
		t.Setenv(name, dir)
	}
	path, err := KeyPath()
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(dir, path); err != nil || !filepath.IsLocal(rel) {
		t.Fatalf("key path %q is outside test directory %q: %v", path, dir, err)
	}
	return path
}

func TestLoadOrCreateKeyRejectsInvalidLength(t *testing.T) {
	path := isolateKeyPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	want := []byte("short")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	key, created, err := LoadOrCreateKey()
	if err == nil || !strings.Contains(err.Error(), "invalid length") || created || key != nil {
		t.Errorf("key=%x created=%t err=%v; want invalid length error without creation", key, created, err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
		t.Errorf("existing key changed: got=%x err=%v, want=%x", got, err, want)
	}
}

func TestLoadOrCreateKeyRejectsDirectory(t *testing.T) {
	path := isolateKeyPath(t)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	key, created, err := LoadOrCreateKey()
	if err == nil || created || key != nil {
		t.Errorf("key=%x created=%t err=%v; want error without creation", key, created, err)
	}
	if entries, err := os.ReadDir(path); err != nil || len(entries) != 0 {
		t.Errorf("key directory changed: entries=%v err=%v", entries, err)
	}
}

func TestLoadKeyRejectsAES128Key(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, bytes.Repeat([]byte{1}, 16), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := LoadKey(path)
	if err == nil || !strings.Contains(err.Error(), "invalid length 16 (expected 32)") || key != nil {
		t.Fatalf("key=%x err=%v; want invalid length error and no key", key, err)
	}
}

func TestWriteKeyPermissions(t *testing.T) {
	permissiveUmask(t)
	path := filepath.Join(t.TempDir(), "envmagic", "key")
	if err := WriteKey(path, bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != want {
			t.Errorf("%s: mode=%#o, want %#o", path, got, want)
		}
	}
}

func TestWriteKeyErrors(t *testing.T) {
	for _, operation := range []string{"directory", "write"} {
		t.Run(operation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "blocked")
			if operation == "directory" {
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(path, "key")
			} else if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := WriteKey(path, bytes.Repeat([]byte{1}, 32)); err == nil {
				t.Fatal("WriteKey accepted a blocked path")
			}
		})
	}
}
