//go:build unix

package internal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenStoreRejectsDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".envmagic")
	target := filepath.Join(dir, "missing")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if store != nil {
		_ = store.Close()
		t.Error("OpenStore returned a store through a dangling symlink")
	}
	if err == nil {
		t.Error("OpenStore returned no error for a dangling symlink")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("symlink target: stat error=%v, want file not to exist", err)
	}
}

func TestOpenStoreRejectsRootOwnedFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("requires a non-root process")
	}
	for _, path := range []string{"/etc/hostname", "/etc/hosts", "/etc/passwd"} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if uid, known := OwnerUID(info); !known || uid != 0 {
			continue
		}
		for _, name := range []string{"direct", "symlink"} {
			t.Run(name, func(t *testing.T) {
				storePath := path
				if name == "symlink" {
					storePath = filepath.Join(t.TempDir(), "store")
					if err := os.Symlink(path, storePath); err != nil {
						t.Fatal(err)
					}
				}
				store, err := OpenStore(storePath)
				if store != nil {
					_ = store.Close()
					t.Fatal("OpenStore returned a root-owned store")
				}
				if err == nil || !strings.Contains(err.Error(), "owned by uid 0") {
					t.Fatalf("OpenStore: err=%v, want root ownership error", err)
				}
			})
		}
		return
	}
	t.Skip("no root-owned file available")
}
