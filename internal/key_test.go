package internal

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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

func TestLoadOrCreateKeyConcurrent(t *testing.T) {
	path := isolateKeyPath(t)
	assertConcurrentKeyCreation(t, path, func(_ int) ([]byte, bool, error) { return LoadOrCreateKey() })
}

func TestLoadOrCreateKeyFallbackConcurrent(t *testing.T) {
	path := isolateKeyPath(t)
	assertConcurrentKeyCreation(t, path, func(i int) ([]byte, bool, error) {
		return createKey(path, bytes.Repeat([]byte{byte(i + 1)}, 32), func(_, _ string) error {
			return errors.ErrUnsupported
		})
	})
}

func assertConcurrentKeyCreation(t *testing.T, path string, load func(int) ([]byte, bool, error)) {
	t.Helper()
	const n = 32
	keys := make([][]byte, n)
	created := make([]bool, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			ready.Done()
			<-start
			keys[i], created[i], errs[i] = load(i)
		})
	}
	ready.Wait()
	close(start)
	wg.Wait()
	creators := 0
	for i := range n {
		if errs[i] != nil || len(keys[i]) != 32 || !bytes.Equal(keys[i], keys[0]) {
			t.Errorf("caller %d: key=%x err=%v, want same 32-byte key %x", i, keys[i], errs[i], keys[0])
		}
		if created[i] {
			creators++
		}
	}
	if creators != 1 {
		t.Errorf("created=true from %d callers, want exactly one", creators)
	}
	assertKeyFile(t, path, keys[0])
}

func TestLoadOrCreateKeyExisting(t *testing.T) {
	path := isolateKeyPath(t)
	want := bytes.Repeat([]byte{1}, 32)
	if err := WriteKey(path, want); err != nil {
		t.Fatal(err)
	}
	key, created, err := LoadOrCreateKey()
	if err != nil || created || !bytes.Equal(key, want) {
		t.Fatalf("key=%x created=%t err=%v, want existing key %x", key, created, err, want)
	}
	assertKeyFile(t, path, want)
}

func TestLoadOrCreateKeyStaleTemp(t *testing.T) {
	path := isolateKeyPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(filepath.Dir(path), ".key-stale")
	if err := os.WriteFile(stale, []byte("incomplete"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, created, err := LoadOrCreateKey()
	if err != nil || !created || len(key) != 32 {
		t.Fatalf("key=%x created=%t err=%v, want new 32-byte key", key, created, err)
	}
	if got, err := os.ReadFile(stale); err != nil || string(got) != "incomplete" {
		t.Fatalf("stale temporary file changed: data=%q err=%v", got, err)
	}
	if err := os.Remove(stale); err != nil {
		t.Fatal(err)
	}
	assertKeyFile(t, path, key)
}

func TestLoadOrCreateKeyPublishRace(t *testing.T) {
	path := isolateKeyPath(t)
	want := bytes.Repeat([]byte{1}, 32)
	key, created, err := createKey(path, bytes.Repeat([]byte{2}, 32), func(tmp, path string) error {
		if filepath.Dir(tmp) != filepath.Dir(path) {
			t.Errorf("temp file %s is not in the key directory %s", tmp, filepath.Dir(path))
		}
		assertKeyFile(t, tmp, bytes.Repeat([]byte{2}, 32))
		if err := WriteKey(path, want); err != nil {
			t.Fatal(err)
		}
		return os.Link(tmp, path)
	})
	if err != nil || created || !bytes.Equal(key, want) {
		t.Fatalf("key=%x created=%t err=%v, want existing key %x", key, created, err, want)
	}
	assertKeyFile(t, path, want)
}

func TestCreateKeyKeepsExistingKey(t *testing.T) {
	path := isolateKeyPath(t)
	want := bytes.Repeat([]byte{1}, 32)
	if err := WriteKey(path, want); err != nil {
		t.Fatal(err)
	}
	key, created, err := createKey(path, bytes.Repeat([]byte{2}, 32), os.Link)
	if err != nil || created || !bytes.Equal(key, want) {
		t.Fatalf("key=%x created=%t err=%v, want existing key %x", key, created, err, want)
	}
	assertKeyFile(t, path, want)
}

func TestLoadOrCreateKeyUnwritableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix permission checks as a non-root user")
	}
	path := isolateKeyPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o500); err != nil {
		t.Fatal(err)
	}
	key, created, err := LoadOrCreateKey()
	if err == nil || !strings.HasPrefix(err.Error(), "failed to write key file "+path+":") || created || key != nil {
		t.Fatalf("key=%x created=%t err=%v, want write error for %s", key, created, err, path)
	}
}

func TestCreateKeyTempRemovalFailure(t *testing.T) {
	for _, published := range []bool{true, false} {
		t.Run(map[bool]string{true: "after-publish", false: "before-publish"}[published], func(t *testing.T) {
			path := isolateKeyPath(t)
			want := bytes.Repeat([]byte{1}, 32)
			key, created, err := createKey(path, want, func(tmp, path string) error {
				var linkErr error = syscall.EIO
				if published {
					linkErr = os.Link(tmp, path)
				}
				// A non-empty directory in place of the temp file makes its removal fail on every OS.
				if err := os.Remove(tmp); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(tmp, "busy"), 0o700); err != nil {
					t.Fatal(err)
				}
				return linkErr
			})
			if published {
				if err != nil || !created || !bytes.Equal(key, want) {
					t.Fatalf("key=%x created=%t err=%v, want published key %x", key, created, err, want)
				}
				if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
					t.Fatalf("key on disk=%x err=%v, want=%x", got, err, want)
				}
			} else if !errors.Is(err, syscall.EIO) || created || key != nil || !strings.Contains(err.Error(), "failed to remove temporary key file") {
				t.Fatalf("key=%x created=%t err=%v, want publish and removal errors", key, created, err)
			}
		})
	}
}

func TestLoadOrCreateKeyDanglingSymlink(t *testing.T) {
	path := isolateKeyPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(path), "target")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	key, created, err := LoadOrCreateKey()
	if err == nil || !strings.Contains(err.Error(), "failed to write key file") || created || key != nil {
		t.Fatalf("key=%x created=%t err=%v, want write error without following the symlink", key, created, err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink target was created: err=%v", err)
	}
	if entries, err := os.ReadDir(filepath.Dir(path)); err != nil || len(entries) != 1 {
		t.Fatalf("leftover files: entries=%v err=%v, want only the symlink", entries, err)
	}
}

func TestWriteNewKeyWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod needs write access to the handle on Windows")
	}
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeNewKey(f, bytes.Repeat([]byte{1}, 32)); err == nil || !strings.Contains(err.Error(), "failed to write key file") {
		t.Fatalf("writeNewKey error=%v, want write error", err)
	}
}

func TestLoadOrCreateKeyLinkFailures(t *testing.T) {
	type linkCase struct {
		name     string
		err      error
		fallback bool
	}
	cases := []linkCase{
		{"unsupported", errors.ErrUnsupported, true},
		{"permission", syscall.EPERM, true},
		{"cross-device", syscall.EXDEV, true},
		{"not-supported", syscall.ENOTSUP, true},
		{"io", syscall.EIO, false},
		{"no-space", syscall.ENOSPC, false},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases,
			linkCase{"invalid-function", syscall.Errno(1), true},
			linkCase{"not-same-device", syscall.Errno(17), true},
			linkCase{"win32-not-supported", syscall.Errno(50), true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := isolateKeyPath(t)
			want := bytes.Repeat([]byte{1}, 32)
			key, created, err := createKey(path, want, func(tmp, path string) error {
				return &os.LinkError{Op: "link", Old: tmp, New: path, Err: tc.err}
			})
			if tc.fallback {
				if err != nil || !created || !bytes.Equal(key, want) {
					t.Fatalf("key=%x created=%t err=%v, want new key %x", key, created, err, want)
				}
				assertKeyFile(t, path, want)
			} else {
				if !errors.Is(err, tc.err) || created || key != nil || !strings.Contains(err.Error(), "failed to publish key file "+path) {
					t.Fatalf("key=%x created=%t err=%v, want link failure", key, created, err)
				}
				if entries, err := os.ReadDir(filepath.Dir(path)); err != nil || len(entries) != 0 {
					t.Fatalf("leftover files after failure: entries=%v err=%v", entries, err)
				}
			}
		})
	}
}

func TestLoadOrCreateKeyWaitsForIncompleteKey(t *testing.T) {
	for _, mode := range []string{"initial-read", "fallback-collision"} {
		t.Run(mode, func(t *testing.T) {
			path := isolateKeyPath(t)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			want := bytes.Repeat([]byte{1}, 32)
			prefix := 0
			if mode == "fallback-collision" {
				prefix = 5
				if _, err := f.Write(want[:prefix]); err != nil {
					t.Fatal(err)
				}
			}
			var key []byte
			var created bool
			done := make(chan struct{})
			go func() {
				defer close(done)
				if mode == "fallback-collision" {
					key, created, err = createKey(path, bytes.Repeat([]byte{2}, 32), func(_, _ string) error { return errors.ErrUnsupported })
				} else {
					key, created, err = LoadOrCreateKey()
				}
			}()
			select {
			case <-done:
				t.Fatalf("returned before writer finished: key=%x created=%t err=%v", key, created, err)
			case <-time.After(75 * time.Millisecond):
			}
			if _, writeErr := f.Write(want[prefix:]); writeErr != nil {
				t.Error(writeErr)
			}
			<-done
			if err != nil || created || !bytes.Equal(key, want) {
				t.Fatalf("key=%x created=%t err=%v, want completed key %x", key, created, err, want)
			}
			assertKeyFile(t, path, want)
		})
	}
}

func assertKeyFile(t *testing.T, path string, want []byte) {
	t.Helper()
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
		t.Errorf("key on disk=%x err=%v, want=%x", got, err, want)
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
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Errorf("leftover files: entries=%v err=%v, want only key", entries, err)
	}
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
	start := time.Now()
	key, created, err := LoadOrCreateKey()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("rejecting a short key took %v, want a bounded wait", elapsed)
	}
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

func TestLoadKeyRejectsLongKeyWithoutWaiting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, bytes.Repeat([]byte{1}, 33), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	key, err := LoadKey(path)
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Errorf("rejecting a long key took %v, want no retry delay", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "invalid length 33 (expected 32)") || key != nil {
		t.Fatalf("key=%x err=%v; want invalid length error and no key", key, err)
	}
}

func TestWriteKeyReplacesLongFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, bytes.Repeat([]byte{1}, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte{2}, 32)
	if err := WriteKey(path, want); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("key=%x err=%v, want=%x", got, err, want)
	}
}

func TestWriteKeyTruncateFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	setupKeyFIFO(t, path)
	if err := WriteKey(path, bytes.Repeat([]byte{1}, 32)); err == nil || !strings.Contains(err.Error(), "failed to truncate key file") {
		t.Fatalf("WriteKey error=%v, want truncate error", err)
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
			want := "failed to write key file"
			if operation == "directory" {
				want = "failed to create key file directory"
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(path, "key")
			} else if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := WriteKey(path, bytes.Repeat([]byte{1}, 32)); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("WriteKey error=%v, want %q", err, want)
			}
		})
	}
}

func TestWriteKeyChmodFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix permission checks as a non-root user")
	}
	before, err := os.Stat("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "key")
	if err := os.Symlink("/dev/null", path); err != nil {
		t.Fatal(err)
	}
	// Ordering is bound by ftruncate(/dev/null) failing with EINVAL; the untouched check alone is weak.
	if err := WriteKey(path, bytes.Repeat([]byte{1}, 32)); err == nil || !strings.Contains(err.Error(), "failed to set key file permissions") {
		t.Fatalf("WriteKey error=%v, want permission error", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() {
		t.Fatal("/dev/null changed after chmod failure")
	}
}
