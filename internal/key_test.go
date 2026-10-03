package internal

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateKeyRejectsInvalidLength(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := KeyPath()
	if err != nil {
		t.Fatal(err)
	}
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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := KeyPath()
	if err != nil {
		t.Fatal(err)
	}
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
