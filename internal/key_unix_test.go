//go:build unix

package internal

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func setupKeyFIFO(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
}

func TestCreateKeyModeIgnoresUmask(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	links := map[string]func(string, string) error{
		"link":     os.Link,
		"fallback": func(_, _ string) error { return errors.ErrUnsupported },
	}
	paths := map[string]string{}
	for name := range links {
		dir := filepath.Join(t.TempDir(), "envmagic")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		paths[name] = filepath.Join(dir, "key")
	}
	previous := syscall.Umask(0o277)
	t.Cleanup(func() { syscall.Umask(previous) })
	for name, link := range links {
		t.Run(name, func(t *testing.T) {
			if _, created, err := createKey(paths[name], key, link); err != nil || !created {
				t.Fatalf("created=%t err=%v", created, err)
			}
			assertKeyFile(t, paths[name], key)
		})
	}
}
