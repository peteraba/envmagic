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
	if uid, known := internal.OwnerUID(info); !known || uid != os.Getuid() {
		t.Errorf("OwnerUID=(%d, %t), want (%d, true)", uid, known, os.Getuid())
	}
}

func TestOwnerUIDRootDirectory(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("requires a non-root process")
	}
	info, err := os.Stat("/")
	if err != nil {
		t.Fatal(err)
	}
	if uid, known := internal.OwnerUID(info); !known || uid != 0 {
		t.Fatalf("OwnerUID(/)=(%d, %t), want (0, true)", uid, known)
	}
}

func TestRealRootOwnedSymlinkUsesParentStore(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("requires a non-root process")
	}
	var target string
	for _, path := range []string{"/etc/hostname", "/etc/hosts", "/etc/passwd"} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if uid, known := internal.OwnerUID(info); known && uid == 0 {
			target = path
			break
		}
	}
	if target == "" {
		t.Skip("no root-owned file available")
	}
	run := setup(t)
	if r := run("set", "TOKEN", "parent"); r.code() != 0 {
		t.Fatal(r.err)
	}
	if err := os.Mkdir("child", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir("child")
	if err := os.Symlink(target, ".envmagic"); err != nil {
		t.Fatal(err)
	}
	path, err := filepath.Abs(".envmagic")
	if err != nil {
		t.Fatal(err)
	}
	warning := fmt.Sprintf("envmagic: skipping %s: owned by uid 0, not by you (uid %d)\n", path, os.Getuid())
	if r := run("get", "TOKEN"); r.code() != 0 || r.stdout != "parent\n" || r.stderr != warning {
		t.Fatalf("get through root-owned symlink: %+v", r)
	}
}

func TestPipedSetWarnsOnce(t *testing.T) {
	for _, parentStore := range []bool{false, true} {
		t.Run(fmt.Sprintf("parent=%t", parentStore), func(t *testing.T) {
			run := setupBare(t)
			if parentStore {
				if r := run("--yes", "set", "TOKEN", "parent"); r.code() != 0 {
					t.Fatal(r.err)
				}
			}
			if err := os.MkdirAll(filepath.Join("foreign", "child"), 0o700); err != nil {
				t.Fatal(err)
			}
			path, err := filepath.Abs(filepath.Join("foreign", ".envmagic"))
			if err != nil {
				t.Fatal(err)
			}
			s, err := internal.OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			warning := mockForeignOwner(t, path)
			t.Chdir(filepath.Join("foreign", "child"))
			setTestStdin(t, "stdin-value\n")
			args := []string{"set", "TOKEN"}
			if !parentStore {
				args = append([]string{"--yes"}, args...)
			}
			r := run(args...)
			if r.code() != 0 || strings.Count(r.stderr, warning) != 1 || strings.Count(r.stderr, "skipping") != 1 {
				t.Fatalf("piped set: %+v", r)
			}
			if r := run("get", "TOKEN"); r.code() != 0 || r.stdout != "stdin-value\n" {
				t.Fatalf("get after piped set: %+v", r)
			}
		})
	}
}

func TestPipedSetRefusesForeignStoreInCWD(t *testing.T) {
	for _, yes := range []bool{false, true} {
		t.Run(fmt.Sprintf("yes=%t", yes), func(t *testing.T) {
			run := setup(t)
			path, err := filepath.Abs(".envmagic")
			if err != nil {
				t.Fatal(err)
			}
			warning := mockForeignOwner(t, path)
			setTestStdin(t, "updated\n")
			args := []string{"set", "TOKEN"}
			if yes {
				args = append([]string{"--yes"}, args...)
			}
			r := run(args...)
			if r.code() != 1 || r.stderr != warning || r.err == nil || !strings.Contains(r.err.Error(), "refusing to overwrite skipped store "+path) {
				t.Fatalf("piped set: %+v", r)
			}
		})
	}
}

func TestStoreSwapWhileOpening(t *testing.T) {
	for _, args := range [][]string{
		{"get", "TOKEN"},
		{"set", "TOKEN", "updated"},
		{"set", "TOKEN"},
		{"import"},
		{"rm", "TOKEN"},
		{"load"},
		{"list"},
		{"export"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			run := setup(t)
			if r := run("set", "TOKEN", "original"); r.code() != 0 {
				t.Fatal(r.err)
			}
			owned, err := filepath.Abs(".envmagic")
			if err != nil {
				t.Fatal(err)
			}
			ownedInfo, err := os.Stat(owned)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			target := filepath.Join(dir, "swapped.db")
			s, err := internal.OpenStore(target)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Set("default", "TOKEN", []byte("unchanged")); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(dir, ".envmagic")
			if err := os.Symlink(owned, link); err != nil {
				t.Fatal(err)
			}
			t.Chdir(dir)
			setTestStdin(t, "TOKEN=updated\n")
			originalOwner := fileOwner
			t.Cleanup(func() { fileOwner = originalOwner })
			checks := 0
			swapAt := 1
			if len(args) == 2 && args[0] == "set" {
				swapAt = 2 // Piped set has a silent preflight search.
			}
			fileOwner = func(info os.FileInfo) (int, bool) {
				if os.SameFile(ownedInfo, info) {
					checks++
					if checks == swapAt {
						if err := os.Remove(link); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(target, link); err != nil {
							t.Fatal(err)
						}
					}
				}
				return originalOwner(info)
			}
			// Both targets have the same owner; only the inode check detects this swap.
			r := run(args...)
			if r.code() != 1 || r.stdout != "" || r.err == nil || !strings.Contains(r.err.Error(), "store "+link+" changed while opening; refusing to use it") {
				t.Errorf("swap: %+v", r)
			}
			s, err = internal.OpenStore(target)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			value, err := s.Get("default", "TOKEN")
			if err != nil || string(value) != "unchanged" {
				t.Fatalf("swapped target changed: value=%q err=%v", value, err)
			}
		})
	}
}

func TestCreateRefusesUnacceptedEntry(t *testing.T) {
	for _, kind := range []string{"dangling", "loop", "directory"} {
		t.Run(kind, func(t *testing.T) {
			run := setupBare(t)
			target := filepath.Join(t.TempDir(), "missing.db")
			var err error
			switch kind {
			case "dangling":
				err = os.Symlink(target, ".envmagic")
			case "loop":
				err = os.Symlink(".envmagic", ".envmagic")
			case "directory":
				err = os.Mkdir(".envmagic", 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"--yes", "set", "TOKEN", "updated"}, {"--yes", "set", "TOKEN"}, {"--yes", "import"}} {
				setTestStdin(t, "TOKEN=updated\n")
				r := run(args...)
				if r.code() != 1 || r.err == nil || !strings.Contains(r.err.Error(), "refusing to overwrite skipped store") {
					t.Errorf("%v: %+v", args, r)
				}
				if _, err := os.Lstat(target); !os.IsNotExist(err) {
					t.Fatalf("dangling target created: %v", err)
				}
			}
		})
	}
}
