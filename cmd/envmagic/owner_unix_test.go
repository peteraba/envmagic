//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peteraba/envmagic/internal"
)

func mockForeignOwner(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	original := fileOwner
	t.Cleanup(func() { fileOwner = original })
	foreignUID := os.Getuid() + 1
	fileOwner = func(candidate os.FileInfo) (int, bool) {
		if os.SameFile(info, candidate) {
			return foreignUID, true
		}
		return original(candidate)
	}
	return fmt.Sprintf("envmagic: skipping %s: owned by uid %d, not by you (uid %d)\n", path, foreignUID, os.Getuid())
}

func TestFindEnvmagicSkipsForeignStore(t *testing.T) {
	for _, parentStore := range []bool{false, true} {
		for _, symlink := range []bool{false, true} {
			t.Run(fmt.Sprintf("parent=%t/symlink=%t", parentStore, symlink), func(t *testing.T) {
				run := setupBare(t)
				if parentStore {
					if r := run("--yes", "set", "TOKEN", "parent"); r.code() != 0 {
						t.Fatal(r.err)
					}
				}
				if err := os.MkdirAll(filepath.Join("foreign", "child"), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Chdir("foreign")
				store, err := internal.OpenStore(".envmagic")
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				if r := run("--yes", "set", "TOKEN", "foreign"); r.code() != 0 {
					t.Fatal(r.err)
				}
				if symlink {
					target := filepath.Join(t.TempDir(), "store")
					if err := os.Rename(".envmagic", target); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, ".envmagic"); err != nil {
						t.Fatal(err)
					}
				}
				foreignPath, err := filepath.Abs(".envmagic")
				if err != nil {
					t.Fatal(err)
				}
				original := fileOwner
				warning := mockForeignOwner(t, foreignPath)
				t.Chdir("child")
				r := run("get", "TOKEN")
				if r.stderr != warning {
					t.Errorf("stderr=%q, want %q", r.stderr, warning)
				}
				if !parentStore {
					if r.code() != 1 || r.stdout != "" || r.err == nil || !strings.Contains(r.err.Error(), "no .envmagic file found") {
						t.Fatalf("get: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
					}
					return
				}
				if r.code() != 0 || r.stdout != "parent\n" {
					t.Fatalf("get: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
				}
				setTestStdin(t, "TOKEN=imported\n")
				for _, write := range []struct {
					args  []string
					value string
				}{
					{[]string{"set", "TOKEN", "updated"}, "updated"},
					{[]string{"import"}, "imported"},
				} {
					r = run(write.args...)
					if r.code() != 0 || !strings.HasPrefix(r.stderr, warning) || strings.Count(r.stderr, warning) != 1 {
						t.Fatalf("%v: stderr=%q err=%v", write.args, r.stderr, r.err)
					}
					if r = run("get", "TOKEN"); r.code() != 0 || r.stdout != write.value+"\n" || r.stderr != warning {
						t.Fatalf("get after %v: stdout=%q stderr=%q err=%v", write.args, r.stdout, r.stderr, r.err)
					}
				}
				t.Chdir("..")
				fileOwner = original
				if r = run("get", "TOKEN"); r.code() != 0 || r.stdout != "foreign\n" {
					t.Fatalf("foreign store changed: stdout=%q err=%v", r.stdout, r.err)
				}
			})
		}
	}
}

func TestForeignStoreNotOverwritten(t *testing.T) {
	for _, args := range [][]string{
		{"--yes", "set", "TOKEN", "updated"},
		{"set", "TOKEN", "updated"},
		{"--yes", "import"},
		{"import"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			run := setup(t)
			if r := run("set", "TOKEN", "original"); r.code() != 0 {
				t.Fatal(r.err)
			}
			path, err := filepath.Abs(".envmagic")
			if err != nil {
				t.Fatal(err)
			}
			original := fileOwner
			warning := mockForeignOwner(t, path)
			setTestStdin(t, "TOKEN=updated\n")
			r := run(args...)
			if r.code() != 1 || r.stdout != "" || r.stderr != warning || r.err == nil || !strings.Contains(r.err.Error(), "refusing to overwrite skipped store") {
				t.Fatalf("write: stdout=%q stderr=%q err=%v", r.stdout, r.stderr, r.err)
			}
			fileOwner = original
			if r = run("get", "TOKEN"); r.code() != 0 || r.stdout != "original\n" {
				t.Fatalf("foreign store changed: stdout=%q err=%v", r.stdout, r.err)
			}
		})
	}
}

func TestSymlinkedStore(t *testing.T) {
	run := setup(t)
	target, err := filepath.Abs(".envmagic")
	if err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	if err := os.Symlink(target, filepath.Join(worktree, ".envmagic")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(worktree)
	if r := run("set", "TOKEN", "linked"); r.code() != 0 {
		t.Fatal(r.err)
	}
	for _, read := range []struct {
		args []string
		want string
	}{
		{[]string{"get", "TOKEN"}, "linked\n"},
		{[]string{"load", "TOKEN"}, "export TOKEN=\"linked\"\n"},
		{[]string{"load"}, "export TOKEN=\"linked\"\n"},
	} {
		if r := run(read.args...); r.code() != 0 || r.stdout != read.want || r.stderr != "" {
			t.Errorf("%v: stdout=%q stderr=%q err=%v", read.args, r.stdout, r.stderr, r.err)
		}
	}
	t.Chdir(filepath.Dir(target))
	if r := run("get", "TOKEN"); r.code() != 0 || r.stdout != "linked\n" {
		t.Errorf("target store: stdout=%q err=%v", r.stdout, r.err)
	}
}

func TestOwnerUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".envmagic")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if uid, known := ownerUID(info); !known || uid != os.Getuid() {
		t.Errorf("ownerUID=(%d, %t), want (%d, true)", uid, known, os.Getuid())
	}
}
