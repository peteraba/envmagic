//go:build !windows

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExportFIFO(t *testing.T) {
	run := setup(t)
	if r := run("set", "TOKEN", "secret"); r.code() != 0 {
		t.Fatal(r.err)
	}
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	type readResult struct {
		content []byte
		err     error
	}
	reads := make(chan readResult, 1)
	go func() {
		f, err := os.Open(path)
		if err != nil {
			reads <- readResult{err: err}
			return
		}
		defer func() { _ = f.Close() }()
		content, err := io.ReadAll(f)
		reads <- readResult{content, err}
	}()
	if r := run("export", path); r.code() != 0 {
		t.Fatal(r.err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("target mode=%v, want FIFO", info.Mode())
	}
	select {
	case read := <-reads:
		if read.err != nil || string(read.content) != "TOKEN=\"secret\"\n" {
			t.Errorf("FIFO content=%q err=%v", read.content, read.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("FIFO reader did not finish")
	}
}

func TestExportReadOnlyFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write read-only files")
	}
	run := setup(t)
	if r := run("set", "TOKEN", "secret"); r.code() != 0 {
		t.Fatal(r.err)
	}
	path := filepath.Join(t.TempDir(), "output.env")
	if err := os.WriteFile(path, []byte("unchanged"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	r := run("export", path)
	if r.code() != 1 || r.err == nil || !strings.Contains(r.err.Error(), "envmagic: open "+path+":") || r.stderr != "" {
		t.Errorf("export: exit=%d stderr=%q err=%v", r.code(), r.stderr, r.err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "unchanged" {
		t.Errorf("read-only content=%q err=%v", content, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o444 {
		t.Errorf("read-only permissions=%o, want 444", info.Mode().Perm())
	}
}

func TestExportSymlink(t *testing.T) {
	permissiveUmask(t)
	run := setup(t)
	if r := run("set", "TOKEN", "secret"); r.code() != 0 {
		t.Fatal(r.err)
	}
	if err := os.WriteFile("real.env", []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.env", "link"); err != nil {
		t.Fatal(err)
	}
	if r := run("export", "link"); r.code() != 0 {
		t.Fatal(r.err)
	}
	info, err := os.Lstat("link")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link mode=%v, want symlink", info.Mode())
	}
	content, err := os.ReadFile("real.env")
	if err != nil || string(content) != "TOKEN=\"secret\"\n" {
		t.Errorf("symlink target content=%q err=%v", content, err)
	}
	info, err = os.Stat("real.env")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("symlink target permissions=%o, want 600", info.Mode().Perm())
	}
}

func TestExportHardLink(t *testing.T) {
	run := setup(t)
	if r := run("set", "TOKEN", "secret"); r.code() != 0 {
		t.Fatal(r.err)
	}
	if err := os.WriteFile("a", []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link("a", "b"); err != nil {
		t.Fatal(err)
	}
	if r := run("export", "a"); r.code() != 0 {
		t.Fatal(r.err)
	}
	content, err := os.ReadFile("b")
	if err != nil || string(content) != "TOKEN=\"secret\"\n" {
		t.Errorf("hard link content=%q err=%v", content, err)
	}
}
