package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/peteraba/envmagic/internal"
)

func checkNoExportTemps(t *testing.T, dir string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, ".*.tmp*"))
	if err != nil || len(paths) != 0 {
		t.Errorf("temporary files left behind: %v, err=%v", paths, err)
	}
}

func TestExportExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions")
	}
	permissiveUmask(t)
	run := setup(t)
	if r := run("set", "TOKEN", "secret"); r.code() != 0 {
		t.Fatal(r.err)
	}
	path := filepath.Join(t.TempDir(), "output.env")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := run("export", path); r.code() != 0 {
		t.Fatal(r.err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "TOKEN=\"secret\"\n" {
		t.Errorf("exported content=%q err=%v", content, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("permissions=%o, want 600", info.Mode().Perm())
	}
	checkNoExportTemps(t, filepath.Dir(path))
}

func TestExportRefusesStore(t *testing.T) {
	for _, path := range []string{".envmagic", "./.envmagic", "store-alias"} {
		t.Run(path, func(t *testing.T) {
			run := setup(t)
			if r := run("set", "TOKEN", "secret"); r.code() != 0 {
				t.Fatal(r.err)
			}
			if path == "store-alias" {
				if err := os.Link(".envmagic", path); err != nil {
					t.Skipf("hard links unavailable: %v", err)
				}
			}
			r := run("export", path)
			if r.code() != 1 || r.stdout != "" || r.stderr != "" || r.err.Error() != "envmagic: refusing to export over the store "+path {
				t.Errorf("export: exit=%d stdout=%q stderr=%q err=%v", r.code(), r.stdout, r.stderr, r.err)
			}
			if r := run("get", "TOKEN"); r.code() != 0 || r.stdout != "secret\n" {
				t.Errorf("store after export: stdout=%q err=%v", r.stdout, r.err)
			}
			checkNoExportTemps(t, ".")
		})
	}
}

func TestExportRefusesKey(t *testing.T) {
	run := setup(t)
	if r := run("set", "TOKEN", "secret"); r.code() != 0 {
		t.Fatal(r.err)
	}
	path, err := internal.KeyPath()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := run("export", path)
	if r.code() != 1 || r.stdout != "" || r.stderr != "" || r.err.Error() != "envmagic: refusing to export over the key file "+path {
		t.Errorf("export: exit=%d stdout=%q stderr=%q err=%v", r.code(), r.stdout, r.stderr, r.err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Errorf("key changed: err=%v", err)
	}
	checkNoExportTemps(t, filepath.Dir(path))
}

func TestExportWrongKeyPreservesFile(t *testing.T) {
	run := setup(t)
	if r := run("set", "TOKEN", "secret"); r.code() != 0 {
		t.Fatal(r.err)
	}
	keyPath, err := internal.KeyPath()
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	key[0] ^= 1
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "output.env")
	if err := os.WriteFile(path, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := run("export", path)
	if r.code() != 1 || !strings.Contains(r.err.Error(), "decrypt TOKEN:") {
		t.Errorf("export: exit=%d err=%v", r.code(), r.err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "unchanged" {
		t.Errorf("failed export changed target: content=%q err=%v", content, err)
	}
	checkNoExportTemps(t, filepath.Dir(path))
}

func TestExportRenameFailureCleanup(t *testing.T) {
	run := setup(t)
	if r := run("set", "TOKEN", "secret"); r.code() != 0 {
		t.Fatal(r.err)
	}
	path := filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	r := run("export", path)
	if r.code() != 1 || !strings.Contains(r.err.Error(), "rename ") || r.stderr != "" {
		t.Errorf("export: exit=%d stderr=%q err=%v", r.code(), r.stderr, r.err)
	}
	checkNoExportTemps(t, filepath.Dir(path))
}
