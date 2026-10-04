package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/urfave/cli/v3"

	"github.com/peteraba/envmagic/internal"
)

type result struct {
	stdout string
	stderr string
	err    error
}

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

func (r result) code() int {
	if r.err == nil {
		return 0
	}
	if ec, ok := r.err.(cli.ExitCoder); ok {
		return ec.ExitCode()
	}
	return 1
}

// setup creates an isolated store and key directory, changes to the store
// directory for the duration of the test, and returns a helper that runs the
// CLI with the given arguments and captures its stdout/stderr.
func setup(t *testing.T) func(args ...string) result {
	t.Helper()

	run := setupBare(t)

	// Pre-create the store so no invocation hits the interactive "create?" prompt.
	s, err := internal.OpenStore(".envmagic")
	if err != nil {
		t.Fatalf("setup: create store: %v", err)
	}
	_ = s.Close()

	return run
}

// setupBare is like setup but does not create .envmagic (for testing create flows).
func setupBare(t *testing.T) func(args ...string) result {
	t.Helper()

	dir := t.TempDir()
	t.Chdir(dir)
	isolateKeyPath(t)

	return func(args ...string) result {
		rOut, wOut, _ := os.Pipe()
		rErr, wErr, _ := os.Pipe()
		origOut, origErr := os.Stdout, os.Stderr
		os.Stdout, os.Stderr = wOut, wErr

		var bufOut, bufErr bytes.Buffer
		var drains sync.WaitGroup
		drains.Go(func() { _, _ = io.Copy(&bufOut, rOut) })
		drains.Go(func() { _, _ = io.Copy(&bufErr, rErr) })

		app := newApp()
		// Prevent urfave/cli's error handler from calling os.Exit during tests.
		app.ExitErrHandler = func(_ context.Context, _ *cli.Command, _ error) {}

		appErr := runApp(app, append([]string{"envmagic"}, args...))

		_ = wOut.Close()
		_ = wErr.Close()
		os.Stdout, os.Stderr = origOut, origErr

		drains.Wait()

		return result{bufOut.String(), bufErr.String(), appErr}
	}
}

func TestCommandsWithoutKey(t *testing.T) {
	for _, args := range [][]string{
		{"get", "TOKEN"},
		{"load", "TOKEN"},
		{"load"},
		{"list"},
		{"rm", "TOKEN"},
		{"export"},
		{"key"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			run := setup(t)
			path, err := internal.KeyPath()
			if err != nil {
				t.Fatal(err)
			}
			s, err := internal.OpenStore(".envmagic")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Set("default", "TOKEN", []byte("ciphertext")); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}

			r := run(args...)
			switch args[0] {
			case "list":
				if r.code() != 0 || r.stdout != "TOKEN\n" || r.stderr != "" {
					t.Errorf("list: %+v", r)
				}
			case "rm":
				if r.code() != 0 || r.stdout != "" || r.stderr != "envmagic: removed TOKEN from namespace \"default\"\n" {
					t.Errorf("rm: %+v", r)
				}
				s, err := internal.OpenStore(".envmagic")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = s.Close() }()
				if _, err := s.Get("default", "TOKEN"); !errors.Is(err, internal.ErrEntryNotFound) {
					t.Errorf("entry not removed: %v", err)
				}
			default:
				want := fmt.Sprintf("envmagic: no key at %s; restore it with envmagic key --set, or run envmagic set to create one", path)
				if r.code() != 1 || r.stdout != "" || r.stderr != "" || r.err == nil || r.err.Error() != want {
					t.Errorf("result=%+v; want exit 1 and error %q", r, want)
				}
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("command created a key: %v", err)
			}
		})
	}
}

func TestKeyLoadErrors(t *testing.T) {
	for _, kind := range []string{"invalid-length", "directory", "no-config-dir"} {
		for _, args := range [][]string{{"get", "TOKEN"}, {"--yes", "set", "TOKEN", "x"}} {
			t.Run(kind+"/"+strings.Join(args, " "), func(t *testing.T) {
				if kind == "directory" && runtime.GOOS == "windows" {
					t.Skip("Unix directory read error")
				}
				if kind == "no-config-dir" && runtime.GOOS == "windows" {
					t.Skip("Unix user config directory error")
				}
				run := setup(t)
				if kind == "no-config-dir" {
					t.Setenv("XDG_CONFIG_HOME", "")
					t.Setenv("HOME", "")
					want := "envmagic: load key: failed to get user config dir: "
					r := run(args...)
					if r.code() != 1 || r.stdout != "" || r.stderr != "" || r.err == nil || !strings.HasPrefix(r.err.Error(), want) {
						t.Errorf("result=%+v; want exit 1 and error starting with %q", r, want)
					}
					return
				}
				path, err := internal.KeyPath()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				key := []byte("short")
				want := fmt.Sprintf("envmagic: load key: key file %s has invalid length 5 (expected 32)", path)
				if kind == "directory" {
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
					_, err := os.ReadFile(path)
					if err == nil {
						t.Fatal("expected directory read error")
					}
					want = fmt.Sprintf("envmagic: load key: failed to read key file %s: %v", path, err)
				} else if err := os.WriteFile(path, key, 0o600); err != nil {
					t.Fatal(err)
				}
				before, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				r := run(args...)
				if r.code() != 1 || r.stdout != "" || r.stderr != "" || r.err == nil || r.err.Error() != want {
					t.Errorf("result=%+v; want exit 1 and error %q", r, want)
				}
				after, err := os.Stat(path)
				if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
					t.Fatalf("key path changed: before=%v after=%v err=%v", before, after, err)
				}
				if kind == "directory" {
					if entries, err := os.ReadDir(path); err != nil || len(entries) != 0 {
						t.Errorf("key directory changed: entries=%v err=%v", entries, err)
					}
				} else if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, key) {
					t.Errorf("key file changed: key=%q err=%v", got, err)
				}
			})
		}
	}
}

// TestSetAndGet covers explicit and implicit syntax, raw values, and load exports.
func TestSetAndGet(t *testing.T) {
	run := setup(t)
	if _, _, err := internal.LoadOrCreateKey(); err != nil {
		t.Fatal(err)
	}
	dbPath, err := filepath.Abs(".envmagic")
	if err != nil {
		t.Fatal(err)
	}
	wantStderr := fmt.Sprintf("envmagic: stored API_KEY (namespace %q) in %s\n", "default", dbPath)

	r := run("api_key", "sk-test-abc")
	if r.code() != 0 {
		t.Fatalf("set: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	if r.stderr != wantStderr {
		t.Errorf("set: stderr=%q, want %q", r.stderr, wantStderr)
	}

	r = run("api_key")
	if r.code() != 0 {
		t.Fatalf("get: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	want := "sk-test-abc\n"
	if r.stdout != want {
		t.Errorf("get: stdout %q, want %q", r.stdout, want)
	}

	value := `spaces "quotes" $cash`
	r = run("set", "api_key", value)
	if r.code() != 0 || r.stdout != "" || r.stderr != wantStderr {
		t.Fatalf("set: exit %d stdout=%q stderr=%q", r.code(), r.stdout, r.stderr)
	}
	for _, args := range [][]string{
		{"get", "api_key"},
		{"api_key"},
		{"--debug", "get", "api_key"},
		{"--debug", "api_key"},
	} {
		r = run(args...)
		if r.code() != 0 || r.stdout != value+"\n" || r.stderr != "" {
			t.Errorf("%v: exit=%d stdout=%q stderr=%q", args, r.code(), r.stdout, r.stderr)
		}
	}

	want = "export API_KEY=\"spaces \\\"quotes\\\" \\$cash\"\n"
	for _, debug := range []bool{false, true} {
		args := []string{"load", "api_key"}
		wantErr := ""
		if debug {
			args = append([]string{"--debug"}, args...)
			wantErr = want
		}
		r = run(args...)
		if r.code() != 0 || r.stdout != want || r.stderr != wantErr {
			t.Errorf("%v: exit=%d stdout=%q stderr=%q", args, r.code(), r.stdout, r.stderr)
		}
	}

	run("api_key", "sk-updated")
	r = run("api_key")
	if !strings.Contains(r.stdout, "sk-updated") {
		t.Errorf("overwrite: expected updated value, stdout=%q", r.stdout)
	}

	for _, args := range [][]string{{"no_such_var"}, {"get", "no_such_var"}, {"load", "no_such_var"}} {
		r = run(args...)
		if r.code() != 1 || r.stdout != "" || r.err.Error() != `envmagic: NO_SUCH_VAR not found in namespace "default"` {
			t.Errorf("%v: exit=%d stdout=%q err=%v", args, r.code(), r.stdout, r.err)
		}
	}

	for _, name := range []string{"get", "set", "load", "list", "key"} {
		if r = run("set", name, "reserved"); r.code() != 0 {
			t.Fatalf("set %s: %v", name, r.err)
		}
		if r = run("get", name); r.code() != 0 || r.stdout != "reserved\n" {
			t.Errorf("get %s: stdout=%q err=%v", name, r.stdout, r.err)
		}
	}
}

func TestSetPreservesWhitespace(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"padded", "  padded  "},
		{"leading newline", "\nleading newline"},
		{"trailing newline", "trailing newline\n"},
		{"tabs and spaces", "\t tab and space \t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, form := range []string{"explicit", "implicit"} {
				t.Run(form, func(t *testing.T) {
					run := setup(t)
					args := []string{"TOKEN", tc.value}
					if form == "explicit" {
						args = append([]string{"set"}, args...)
					}
					if r := run(args...); r.code() != 0 {
						t.Fatalf("set: exit=%d stderr=%q err=%v", r.code(), r.stderr, r.err)
					}
					if r := run("get", "TOKEN"); r.code() != 0 || r.stdout != tc.value+"\n" || r.stderr != "" {
						t.Errorf("get: exit=%d stdout=%q stderr=%q err=%v; want stdout=%q", r.code(), r.stdout, r.stderr, r.err, tc.value+"\n")
					}
				})
			}
		})
	}
}

func setTestStdin(t *testing.T, value string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = input
	t.Cleanup(func() {
		os.Stdin = original
		_ = input.Close()
	})
	return input
}

func TestSetStdin(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		value string
	}{
		{"spaces and inner newline", "  first \nsecond  \n", "  first \nsecond  "},
		{"two trailing newlines", "a\n\n", "a\n"},
		{"CRLF", "a\r\n", "a"},
		{"two trailing CRLFs", "a\r\n\r\n", "a\r\n"},
		{"no newline", " \tsecret \t", " \tsecret \t"},
		{"carriage return only", "a\r", "a\r"},
		{"non-UTF-8", "\xff\xfe\n", "\xff\xfe"},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := setup(t)
			setTestStdin(t, test.input)
			if r := run("-n", "other", "set", "token"); r.code() != 0 || r.stdout != "" {
				t.Fatalf("set: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
			}
			for _, args := range [][]string{{"-n", "other", "get", "TOKEN"}, {"-n", "other", "TOKEN"}} {
				if r := run(args...); r.code() != 0 || r.stdout != test.value+"\n" || r.stderr != "" {
					t.Errorf("%v: exit=%d stdout=%q stderr=%q err=%v; want stdout=%q", args, r.code(), r.stdout, r.stderr, r.err, test.value+"\n")
				}
			}
			if r := run("get", "TOKEN"); r.code() != 1 || r.stdout != "" || !strings.Contains(r.err.Error(), "not found") {
				t.Errorf("default namespace: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
			}
		})
	}
}

func TestSetStdinReadError(t *testing.T) {
	run := setupBare(t)
	keyPath, err := internal.KeyPath()
	if err != nil {
		t.Fatal(err)
	}
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = input
	t.Cleanup(func() {
		os.Stdin = original
		_ = output.Close()
	})
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if r := run("--yes", "set", "TOKEN"); r.code() != 1 || r.stdout != "" || !strings.Contains(r.err.Error(), "read stdin:") {
		t.Fatalf("set: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
	}
	for _, path := range []string{".envmagic", keyPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("failed read created %s: %v", path, err)
		}
	}
}

func setTestTerminal(t *testing.T) {
	t.Helper()
	original := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	t.Cleanup(func() { stdinIsTerminal = original })
}

func TestSetStdinTerminal(t *testing.T) {
	run := setup(t)
	input := setTestStdin(t, "secret\n")
	setTestTerminal(t)
	r := run("set", "TOKEN")
	want := "usage: envmagic set [-n NS] NAME [VALUE]; without VALUE, pipe the value on stdin (e.g. printf '%s' \"$SECRET\" | envmagic set NAME)"
	if r.code() != 2 || r.stdout != "" || r.err.Error() != want {
		t.Errorf("set: exit=%d stdout=%q err=%v; want exit 2 and %q", r.code(), r.stdout, r.err, want)
	}
	remaining, err := io.ReadAll(input)
	if err != nil || string(remaining) != "secret\n" {
		t.Errorf("stdin consumed: remaining=%q err=%v", remaining, err)
	}
}

func TestCreateStorePrompt(t *testing.T) {
	for _, args := range [][]string{{"set", "TOKEN", "v"}, {"TOKEN", "v"}, {"import", "input.env"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			run := setupBare(t)
			t.Setenv("ENVMAGIC_NONINTERACTIVE", "")
			if args[0] == "import" {
				if err := os.WriteFile("input.env", []byte("TOKEN=v\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			setTestTerminal(t)
			setTestStdin(t, "y\n")
			r := run(args...)
			if r.code() != 0 || !strings.Contains(r.stderr, "No .envmagic file found. Create ") || !strings.Contains(r.stderr, "? [y/N]: ") {
				t.Fatalf("exit=%d stderr=%q err=%v; want creation prompt", r.code(), r.stderr, r.err)
			}
			if _, err := os.Stat(".envmagic"); err != nil {
				t.Fatalf("store not created: %v", err)
			}
			if r := run("get", "TOKEN"); r.code() != 0 || r.stdout != "v\n" {
				t.Errorf("get: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
			}
		})
	}
}

func TestCreateStoreWithoutTerminal(t *testing.T) {
	for _, args := range [][]string{{"import"}, {"set", "TOKEN", "v"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			run := setupBare(t)
			t.Setenv("ENVMAGIC_NONINTERACTIVE", "")
			var input *os.File
			var err error
			if args[0] == "import" {
				var output *os.File
				input, output, err = os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = output.Close() })
				if _, err := output.WriteString("TOKEN=v\n"); err != nil {
					t.Fatal(err)
				}
				_ = output.Close()
			} else {
				input, err = os.Open(os.DevNull)
				if err != nil {
					t.Fatal(err)
				}
			}
			original := os.Stdin
			os.Stdin = input
			t.Cleanup(func() {
				os.Stdin = original
				_ = input.Close()
			})
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("envmagic: no .envmagic in %s or any parent; rerun with --yes (or ENVMAGIC_NONINTERACTIVE=1) to create one", cwd)
			if r := run(args...); r.code() != 1 || r.stdout != "" || r.stderr != "" || r.err.Error() != want {
				t.Fatalf("exit=%d stdout=%q stderr=%q err=%v; want %q", r.code(), r.stdout, r.stderr, r.err, want)
			}
			if _, err := os.Stat(".envmagic"); !os.IsNotExist(err) {
				t.Fatalf("store created without consent: %v", err)
			}
		})
	}
}

func TestCreateStorePromptBufferedAnswers(t *testing.T) {
	run := setupBare(t)
	t.Setenv("ENVMAGIC_NONINTERACTIVE", "")
	setTestTerminal(t)
	setTestStdin(t, "maybe\ny\n")
	r := run("set", "TOKEN", "v")
	if r.code() != 0 || r.stdout != "" || strings.Count(r.stderr, "? [y/N]: ") != 2 {
		t.Fatalf("exit=%d stdout=%q stderr=%q err=%v; want two prompts", r.code(), r.stdout, r.stderr, r.err)
	}
	if _, err := os.Stat(".envmagic"); err != nil {
		t.Fatalf("store not created: %v", err)
	}
	if r := run("get", "TOKEN"); r.code() != 0 || r.stdout != "v\n" {
		t.Errorf("get: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
	}
}

func TestSetStdinRejectsEmptyOrOversized(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", "no value on stdin; to store an empty value use: envmagic set NAME ''"},
		{"LF", "\n", "no value on stdin; to store an empty value use: envmagic set NAME ''"},
		{"CRLF", "\r\n", "no value on stdin; to store an empty value use: envmagic set NAME ''"},
		{"oversized", strings.Repeat("x", 1<<20+1), "value on stdin is larger than 1 MiB"},
	} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%t", tc.name, existing), func(t *testing.T) {
				run := setupBare(t)
				if existing {
					if r := run("--yes", "set", "TOKEN", "original"); r.code() != 0 {
						t.Fatal(r.err)
					}
				}
				setTestStdin(t, tc.input)
				if r := run("--yes", "set", "TOKEN"); r.code() != 2 || r.stdout != "" || r.err.Error() != tc.want {
					t.Fatalf("set: exit=%d stdout bytes=%d err=%v; want exit 2 and %q", r.code(), len(r.stdout), r.err, tc.want)
				}
				if existing {
					if r := run("get", "TOKEN"); r.code() != 0 || r.stdout != "original\n" {
						t.Errorf("existing value changed: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
					}
					return
				}
				keyPath, err := internal.KeyPath()
				if err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{".envmagic", keyPath} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Errorf("rejected set created %s: %v", path, err)
					}
				}
			})
		}
	}
}

func TestSetStdinSizeLimit(t *testing.T) {
	run := setupBare(t)
	value := strings.Repeat("x", 1<<20)
	setTestStdin(t, value)
	if r := run("--yes", "set", "TOKEN"); r.code() != 0 || r.stdout != "" {
		t.Fatalf("set: exit=%d stdout bytes=%d err=%v", r.code(), len(r.stdout), r.err)
	}
	s, key, _, err := openActiveStore(true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	enc, err := s.Get("default", "TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := internal.Decrypt(key, enc, internal.AD("default", "TOKEN"))
	if err != nil || string(plain) != value {
		t.Errorf("stored value: bytes=%d err=%v; want exactly 1 MiB unchanged", len(plain), err)
	}
}

func TestSetStdinReadLimit(t *testing.T) {
	run := setupBare(t)
	input := setTestStdin(t, strings.Repeat("x", 1<<20+2))
	if r := run("--yes", "set", "TOKEN"); r.code() != 2 || r.err.Error() != "value on stdin is larger than 1 MiB" {
		t.Fatalf("set: exit=%d err=%v", r.code(), r.err)
	}
	remaining, err := io.ReadAll(input)
	if err != nil || string(remaining) != "x" {
		t.Errorf("read past limit: remaining bytes=%d err=%v; want one unread byte", len(remaining), err)
	}
}

func TestSetStdinUsesParentStore(t *testing.T) {
	run := setup(t)
	t.Setenv("ENVMAGIC_NONINTERACTIVE", "")
	if _, _, err := internal.LoadOrCreateKey(); err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.Abs(".envmagic")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("child", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir("child")
	setTestStdin(t, "secret\n")
	wantStderr := fmt.Sprintf("envmagic: using %s\nenvmagic: stored TOKEN (namespace %q) in %s\n", parent, "default", parent)
	if r := run("set", "TOKEN"); r.code() != 0 || r.stdout != "" || r.stderr != wantStderr {
		t.Fatalf("set: %+v; want stderr=%q", r, wantStderr)
	}
	if r := run("get", "TOKEN"); r.code() != 0 || r.stdout != "secret\n" {
		t.Errorf("get: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
	}
	if _, err := os.Stat(".envmagic"); !os.IsNotExist(err) {
		t.Errorf("unexpected child store: %v", err)
	}
}

func TestWriteStoreNotice(t *testing.T) {
	for _, location := range []string{"local", "parent", "symlink"} {
		for _, args := range [][]string{{"set", "TOKEN", "updated"}, {"TOKEN", "updated"}, {"import"}} {
			t.Run(location+"/"+strings.Join(args, " "), func(t *testing.T) {
				run := setup(t)
				if r := run("set", "TOKEN", "original"); r.code() != 0 {
					t.Fatal(r.err)
				}
				parent, err := filepath.Abs(".envmagic")
				if err != nil {
					t.Fatal(err)
				}
				dbPath := parent
				if location != "local" {
					if err := os.Mkdir("child", 0o700); err != nil {
						t.Fatal(err)
					}
					t.Chdir("child")
					if location == "symlink" {
						dbPath = filepath.Join(filepath.Dir(parent), "child", ".envmagic")
						if err := os.Symlink(parent, dbPath); err != nil {
							t.Fatal(err)
						}
					}
				}
				setTestStdin(t, "TOKEN=updated\nOTHER=also\n")
				want := fmt.Sprintf("envmagic: stored TOKEN (namespace %q) in %s\n", "default", dbPath)
				if args[0] == "import" {
					want = "envmagic: imported 2 variable(s) from stdin into namespace \"default\"\n"
				}
				if location == "parent" {
					want = "envmagic: using " + parent + "\n" + want
				}
				if r := run(args...); r.code() != 0 || r.stdout != "" || r.stderr != want {
					t.Fatalf("write: %+v; want stderr=%q", r, want)
				}
				if location == "parent" {
					if _, err := os.Lstat(".envmagic"); !os.IsNotExist(err) {
						t.Fatalf("unexpected local store: %v", err)
					}
				}
				t.Chdir(filepath.Dir(parent))
				if r := run("get", "TOKEN"); r.code() != 0 || r.stdout != "updated\n" || r.stderr != "" {
					t.Fatalf("parent store: %+v", r)
				}
			})
		}
	}
}

func TestHereWriteStore(t *testing.T) {
	for _, mode := range []string{"yes", "noninteractive", "prompt", "no terminal", "existing", "symlink"} {
		for _, args := range [][]string{{"set", "TOKEN", "updated"}, {"TOKEN", "updated"}, {"set", "TOKEN"}, {"import", "input.env"}} {
			t.Run(mode+"/"+strings.Join(args, " "), func(t *testing.T) {
				run := setup(t)
				t.Setenv("ENVMAGIC_NONINTERACTIVE", "")
				if r := run("set", "TOKEN", "parent"); r.code() != 0 {
					t.Fatal(r.err)
				}
				parent, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir("child", 0o700); err != nil {
					t.Fatal(err)
				}
				t.Chdir("child")
				local := filepath.Join(parent, "child", ".envmagic")
				if mode == "existing" || mode == "symlink" {
					target := local
					if mode == "symlink" {
						target = filepath.Join(t.TempDir(), "store")
						if err := os.Symlink(target, local); err != nil {
							t.Fatal(err)
						}
					}
					s, err := internal.OpenStore(target)
					if err != nil {
						t.Fatal(err)
					}
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile("input.env", []byte("TOKEN=updated\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				stdinSet := len(args) == 2 && args[0] == "set"
				inputValue := ""
				if mode == "prompt" && !stdinSet {
					setTestTerminal(t)
					inputValue = "y\n"
				} else if stdinSet {
					inputValue = "updated\n"
				}
				input := setTestStdin(t, inputValue)
				flags := []string{"--here"}
				switch mode {
				case "yes":
					flags = append(flags, "--yes")
				case "noninteractive":
					t.Setenv("ENVMAGIC_NONINTERACTIVE", "1")
				}
				r := run(append(flags, args...)...)
				fails := mode == "no terminal" || (mode == "prompt" && stdinSet)
				if fails {
					wantErr := fmt.Sprintf("envmagic: no .envmagic in %s or any parent; rerun with --yes (or ENVMAGIC_NONINTERACTIVE=1) to create one", filepath.Dir(local))
					wantStderr := ""
					if stdinSet {
						wantErr = "envmagic: no .envmagic file found; reading a value from stdin requires --yes or ENVMAGIC_NONINTERACTIVE=1 to create a store"
						wantStderr = ""
						remaining, err := io.ReadAll(input)
						if err != nil || string(remaining) != inputValue {
							t.Fatalf("stdin consumed before consent: %q, %v", remaining, err)
						}
					}
					if r.code() != 1 || r.stdout != "" || r.stderr != wantStderr || r.err.Error() != wantErr {
						t.Fatalf("write: %+v; want stderr=%q err=%q", r, wantStderr, wantErr)
					}
					if _, err := os.Lstat(local); !os.IsNotExist(err) {
						t.Fatalf("store created without consent: %v", err)
					}
				} else {
					if r.code() != 0 || r.stdout != "" || strings.Contains(r.stderr, "envmagic: using ") {
						t.Fatalf("write: %+v", r)
					}
					if _, err := os.Stat(local); err != nil {
						t.Fatalf("local store missing: %v", err)
					}
					if r := run("get", "TOKEN"); r.code() != 0 || r.stdout != "updated\n" || r.stderr != "" {
						t.Fatalf("local store: %+v", r)
					}
				}
				t.Chdir(parent)
				if r := run("get", "TOKEN"); r.code() != 0 || r.stdout != "parent\n" || r.stderr != "" {
					t.Fatalf("parent store changed: %+v", r)
				}
			})
		}
	}
}

func TestHereSetStdinIgnoresForeignParent(t *testing.T) {
	run := setup(t)
	t.Setenv("ENVMAGIC_NONINTERACTIVE", "")
	info, err := os.Stat(".envmagic")
	if err != nil {
		t.Fatal(err)
	}
	original := fileOwner
	t.Cleanup(func() { fileOwner = original })
	fileOwner = func(candidate os.FileInfo) (int, bool) {
		if os.SameFile(info, candidate) {
			return currentUID() + 1, true
		}
		return original(candidate)
	}
	if err := os.Mkdir("child", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir("child")
	setTestStdin(t, "secret\n")
	r := run("--here", "set", "TOKEN")
	wantErr := "envmagic: no .envmagic file found; reading a value from stdin requires --yes or ENVMAGIC_NONINTERACTIVE=1 to create a store"
	if r.code() != 1 || r.stderr != "" || r.err.Error() != wantErr {
		t.Fatalf("write: %+v; want stderr=%q err=%q", r, "", wantErr)
	}
}

func TestHereRefusesLocalStore(t *testing.T) {
	for _, kind := range []string{"foreign", "directory", "dangling", "loop"} {
		for _, yes := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/yes=%t", kind, yes), func(t *testing.T) {
				run := setup(t)
				t.Setenv("ENVMAGIC_NONINTERACTIVE", "")
				if r := run("set", "TOKEN", "parent"); r.code() != 0 {
					t.Fatal(r.err)
				}
				parent, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir("child", 0o700); err != nil {
					t.Fatal(err)
				}
				t.Chdir("child")
				local := filepath.Join(parent, "child", ".envmagic")
				warning := ""
				switch kind {
				case "foreign":
					if err := os.WriteFile(local, []byte("untouched"), 0o600); err != nil {
						t.Fatal(err)
					}
					info, err := os.Stat(local)
					if err != nil {
						t.Fatal(err)
					}
					original := fileOwner
					t.Cleanup(func() { fileOwner = original })
					fileOwner = func(candidate os.FileInfo) (int, bool) {
						if os.SameFile(info, candidate) {
							return currentUID() + 1, true
						}
						return original(candidate)
					}
					warning = fmt.Sprintf("envmagic: skipping %s: owned by uid %d, not by you (uid %d)\n", local, currentUID()+1, currentUID())
				case "directory":
					err = os.Mkdir(local, 0o700)
				case "dangling":
					err = os.Symlink("missing", local)
				case "loop":
					err = os.Symlink(".envmagic", local)
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, args := range [][]string{{"set", "TOKEN", "updated"}, {"set", "TOKEN"}, {"import"}} {
					setTestStdin(t, "TOKEN=updated\n")
					flags := []string{"--here"}
					if yes {
						flags = append(flags, "--yes")
					}
					if r := run(append(flags, args...)...); r.code() != 1 || r.stdout != "" || r.stderr != warning || r.err.Error() != "envmagic: refusing to overwrite skipped store "+local {
						t.Fatalf("%v: %+v", args, r)
					}
				}
				if kind == "foreign" {
					if data, err := os.ReadFile(local); err != nil || string(data) != "untouched" {
						t.Fatalf("foreign file changed: %q, %v", data, err)
					}
				}
				t.Chdir(parent)
				if r := run("get", "TOKEN"); r.code() != 0 || r.stdout != "parent\n" || r.stderr != "" {
					t.Fatalf("parent store changed: %+v", r)
				}
			})
		}
	}
}

func TestHereIgnoredByReads(t *testing.T) {
	run := setup(t)
	if r := run("set", "TOKEN", "parent"); r.code() != 0 {
		t.Fatal(r.err)
	}
	if err := os.Mkdir("child", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir("child")
	for _, args := range [][]string{{"get", "TOKEN"}, {"TOKEN"}, {"load", "TOKEN"}, {"load"}, {"ls"}, {"export"}, {"rm", "TOKEN"}} {
		want := run(args...)
		if args[0] == "rm" {
			if r := run("set", "TOKEN", "parent"); r.code() != 0 {
				t.Fatal(r.err)
			}
		}
		if r := run(append([]string{"--here"}, args...)...); want.code() != 0 || r.code() != want.code() || r.stdout != want.stdout || r.stderr != want.stderr {
			t.Fatalf("%v with --here: %+v; want %+v", args, r, want)
		}
	}
}

func TestSetValueDoesNotReadStdin(t *testing.T) {
	for _, args := range [][]string{{"set", "TOKEN", "value"}, {"TOKEN", "value"}, {"set", "TOKEN", "-"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			run := setup(t)
			input := setTestStdin(t, "unused\n")
			if r := run(args...); r.code() != 0 {
				t.Fatal(r.err)
			}
			if r := run("TOKEN"); r.code() != 0 || r.stdout != args[len(args)-1]+"\n" {
				t.Errorf("implicit get: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
			}
			remaining, err := io.ReadAll(input)
			if err != nil || string(remaining) != "unused\n" {
				t.Errorf("stdin consumed: remaining=%q err=%v", remaining, err)
			}
		})
	}
}

func TestCheckSetArgs(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		for _, count := range []int{0, 1, 2, 3} {
			t.Run(fmt.Sprintf("terminal=%t/count=%d", terminal, count), func(t *testing.T) {
				r := result{err: checkSetArgs(count, terminal)}
				if count == 2 || (count == 1 && !terminal) {
					if r.err != nil {
						t.Fatalf("valid args: %v", r.err)
					}
					return
				}
				want := "usage: envmagic set [-n NS] NAME [VALUE]; without VALUE, pipe the value on stdin (e.g. printf '%s' \"$SECRET\" | envmagic set NAME)"
				if r.code() != 2 || r.err.Error() != want {
					t.Errorf("exit=%d err=%v; want exit 2 and %q", r.code(), r.err, want)
				}
			})
		}
	}
}

func TestSetStdinRejectsNUL(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			run := setupBare(t)
			if existing {
				if r := run("--yes", "set", "GOOD", "valid"); r.code() != 0 {
					t.Fatal(r.err)
				}
			}
			setTestStdin(t, "a\x00b\n")
			r := run("--yes", "set", "name")
			if r.code() != 1 || r.stdout != "" || r.err.Error() != "envmagic: value for NAME contains a NUL byte" {
				t.Fatalf("set: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
			}
			if existing {
				if r := run("list"); r.code() != 0 || r.stdout != "GOOD\n" {
					t.Errorf("list after rejected set: stdout=%q err=%v", r.stdout, r.err)
				}
				return
			}
			keyPath, err := internal.KeyPath()
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{".envmagic", keyPath} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("rejected set created %s: %v", path, err)
				}
			}
		})
	}
}

func TestSetStdinRequiresYesBeforeReading(t *testing.T) {
	run := setupBare(t)
	t.Setenv("ENVMAGIC_NONINTERACTIVE", "")
	input := setTestStdin(t, "yes\n")
	r := run("set", "TOKEN")
	want := "envmagic: no .envmagic file found; reading a value from stdin requires --yes or ENVMAGIC_NONINTERACTIVE=1 to create a store"
	if r.code() != 1 || r.stdout != "" || r.err.Error() != want || r.stderr != "" {
		t.Errorf("set: exit=%d stdout=%q stderr=%q err=%v; want %q and no prompt", r.code(), r.stdout, r.stderr, r.err, want)
	}
	remaining, err := io.ReadAll(input)
	if err != nil || string(remaining) != "yes\n" {
		t.Errorf("stdin consumed before creation check: remaining=%q err=%v", remaining, err)
	}
	keyPath, err := internal.KeyPath()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".envmagic", keyPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("rejected set created %s: %v", path, err)
		}
	}
}

func TestSetStdinCreatesStore(t *testing.T) {
	for _, mode := range []string{"yes", "noninteractive"} {
		t.Run(mode, func(t *testing.T) {
			run := setupBare(t)
			t.Setenv("ENVMAGIC_NONINTERACTIVE", "")
			setTestStdin(t, "  secret  \n")
			args := []string{"set", "TOKEN"}
			if mode == "yes" {
				args = append([]string{"--yes"}, args...)
			} else {
				t.Setenv("ENVMAGIC_NONINTERACTIVE", "1")
			}
			if r := run(args...); r.code() != 0 || r.stdout != "" {
				t.Fatalf("set: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
			}
			if r := run("get", "TOKEN"); r.code() != 0 || r.stdout != "  secret  \n" {
				t.Errorf("get: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
			}
		})
	}
}

func TestSetFlagParsing(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		value     string
		namespace string
	}{
		{"explicit terminator", []string{"set", "--", "TOKEN", "--help"}, "--help", "default"},
		{"implicit terminator", []string{"--", "TOKEN", "--help"}, "--help", "default"},
		{"lone dash", []string{"set", "TOKEN", "-", "-n", "other"}, "-", "other"},
		{"non-ASCII dash value", []string{"set", "TOKEN", "-€"}, "-€", "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := setup(t)
			if r := run(tc.args...); r.code() != 0 || r.stdout != "" {
				t.Fatalf("set: exit=%d stdout=%q stderr=%q err=%v", r.code(), r.stdout, r.stderr, r.err)
			}
			if r := run("-n", tc.namespace, "get", "TOKEN"); r.code() != 0 || r.stdout != tc.value+"\n" || r.stderr != "" {
				t.Errorf("get: exit=%d stdout=%q stderr=%q err=%v; want stdout=%q", r.code(), r.stdout, r.stderr, r.err, tc.value+"\n")
			}
			if tc.namespace != "default" {
				if r := run("get", "TOKEN"); r.code() != 1 || r.stdout != "" || r.err == nil || !strings.Contains(r.err.Error(), "not found") {
					t.Errorf("default namespace: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
				}
			}
		})
	}
}

func TestFlagSourceError(t *testing.T) {
	run := setup(t)
	t.Setenv("ENVMAGIC_NONINTERACTIVE", "yes")
	r := run("list")
	if r.code() != 1 || r.stdout != "" || r.err == nil || r.stderr != "envmagic: "+r.err.Error()+"\n" || !strings.Contains(r.stderr, "parse error") {
		t.Errorf("exit=%d stdout=%q stderr=%q err=%v; want exit 1 and one parse diagnostic", r.code(), r.stdout, r.stderr, r.err)
	}
}

func TestUsageErrorsNoStdout(t *testing.T) {
	argsList := [][]string{
		{"--bogus"},
		{"get", "X", "-q"},
		{"load", "-x"},
		{"set", "X", "-abc"},
		{"set", "X", "-n"},
		{"key", "--set"},
		{"import", "-ie"},
		{"help", "--bogus"},
	}
	for _, cmd := range newApp().Commands {
		argsList = append(argsList, []string{cmd.Name, "--bogus"})
	}
	for _, args := range argsList {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			run := setupBare(t)
			r := run(args...)
			// Runtime-added help bypasses the usage hook and adds a trailing blank line.
			if args[0] == "help" {
				if r.code() != 1 || r.stdout != "" || r.err == nil || r.stderr != "Incorrect Usage: "+r.err.Error()+"\n\n" {
					t.Errorf("exit=%d stdout=%q stderr=%q err=%v; want non-zero exit, empty stdout and an unknown-flag diagnostic", r.code(), r.stdout, r.stderr, r.err)
				}
				return
			}
			if r.code() != 1 || r.stdout != "" || r.err == nil || r.stderr != "Incorrect Usage: "+r.err.Error()+"\n" {
				t.Errorf("exit=%d stdout=%q stderr=%q err=%v; want exit 1, empty stdout and a one-line usage diagnostic", r.code(), r.stdout, r.stderr, r.err)
			}
		})
	}
}

func TestNamespacePreservesWhitespace(t *testing.T) {
	run := setup(t)
	if r := run("--namespace=x ", "set", "TOKEN", "v"); r.code() != 0 || r.stdout != "" {
		t.Fatalf("set: exit=%d stdout=%q stderr=%q err=%v", r.code(), r.stdout, r.stderr, r.err)
	}
	if r := run("--namespace=x ", "get", "TOKEN"); r.code() != 0 || r.stdout != "v\n" || r.stderr != "" {
		t.Errorf("get padded namespace: exit=%d stdout=%q stderr=%q err=%v", r.code(), r.stdout, r.stderr, r.err)
	}
	if r := run("-n", "x", "get", "TOKEN"); r.code() != 1 || r.stdout != "" || r.err == nil || !strings.Contains(r.err.Error(), "not found") {
		t.Errorf("get unpadded namespace: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
	}
}

func TestStoreWriteErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a read-only store")
	}
	run := setup(t)
	if r := run("set", "API_KEY", "original"); r.code() != 0 {
		t.Fatal(r.err)
	}
	envFile := filepath.Join(t.TempDir(), "input.env")
	if err := os.WriteFile(envFile, []byte("API_KEY=updated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(".envmagic", 0o444); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args   []string
		prefix string
	}{
		{[]string{"set", "API_KEY", "updated"}, "envmagic: write: failed to set entry,"},
		{[]string{"API_KEY", "updated"}, "envmagic: write: failed to set entry,"},
		{[]string{"import", envFile}, "envmagic: write: failed to set entry,"},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			r := run(tc.args...)
			if r.code() != 1 || r.stdout != "" || r.stderr != "" || r.err == nil || !strings.HasPrefix(r.err.Error(), tc.prefix) || !strings.Contains(r.err.Error(), "readonly") {
				t.Errorf("%v: exit=%d stdout=%q stderr=%q err=%v; want %q and readonly error", tc.args, r.code(), r.stdout, r.stderr, r.err, tc.prefix)
			}
		})
	}
	if r := run("get", "API_KEY"); r.code() != 0 || r.stdout != "original\n" {
		t.Errorf("rejected writes changed value: stdout=%q err=%v", r.stdout, r.err)
	}
}

func TestCiphertextBinding(t *testing.T) {
	for _, tc := range []struct {
		label     string
		namespace string
		name      string
	}{
		{"name", "dev", "Z_OTHER"},
		{"namespace", "prd", "Z_TOKEN"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			run := setup(t)
			for _, args := range [][]string{
				{"-n", "dev", "set", "Z_TOKEN", "source secret"},
				{"-n", tc.namespace, "set", "A_VALID", "safe"},
				{"-n", tc.namespace, "set", tc.name, "target secret"},
			} {
				if r := run(args...); r.code() != 0 {
					t.Fatal(r.err)
				}
			}
			commands := [][]string{
				{"get", tc.name},
				{"load", tc.name},
				{"load"},
				{"--debug", "load"},
				{"export"},
			}
			for _, command := range commands {
				args := append([]string{"-n", tc.namespace}, command...)
				if r := run(args...); r.code() != 0 || r.stdout == "" {
					t.Fatalf("before swap %v: stdout=%q err=%v", args, r.stdout, r.err)
				}
			}

			db, err := sql.Open("sqlite", ".envmagic")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			res, err := db.Exec(`UPDATE env_vars SET value =
				(SELECT value FROM env_vars WHERE namespace = 'dev' AND name = 'Z_TOKEN')
				WHERE namespace = ? AND name = ?`, tc.namespace, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := res.RowsAffected(); err != nil || n != 1 {
				t.Fatalf("swap: affected=%d err=%v", n, err)
			}

			for _, command := range commands {
				args := append([]string{"-n", tc.namespace}, command...)
				r := run(args...)
				if r.code() != 1 || r.stdout != "" || r.stderr != "" || r.err == nil || !strings.Contains(r.err.Error(), "message authentication failed") {
					t.Errorf("after swap %v: exit=%d stdout=%q stderr=%q err=%v", args, r.code(), r.stdout, r.stderr, r.err)
				}
			}
		})
	}
}

func TestLegacyCiphertext(t *testing.T) {
	run := setup(t)
	if r := run("-n", "dev", "set", "A_VALID", "safe"); r.code() != 0 {
		t.Fatal(r.err)
	}
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
	db, err := sql.Open("sqlite", ".envmagic")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO env_vars (namespace, name, value) VALUES ('dev', 'Z_LEGACY', ?)`, enc); err != nil {
		t.Fatal(err)
	}

	for _, command := range [][]string{{"get", "Z_LEGACY"}, {"load", "Z_LEGACY"}, {"load"}, {"export"}} {
		args := append([]string{"-n", "dev"}, command...)
		r := run(args...)
		if r.code() != 1 || r.stdout != "" || r.err == nil {
			t.Errorf("%v: exit=%d stdout=%q err=%v", args, r.code(), r.stdout, r.err)
			continue
		}
		for _, hint := range []string{"wrong key", "or stored by an older envmagic; re-import it (see README)"} {
			if !strings.Contains(r.err.Error(), hint) {
				t.Errorf("%v: err=%v, want hint %q", args, r.err, hint)
			}
		}
		if strings.ContainsAny(r.err.Error(), "\r\n") {
			t.Errorf("%v: error is not one line: %q", args, r.err)
		}
	}
}

func TestSetRejectsNUL(t *testing.T) {
	run := setup(t)

	r := run("set", "name", "a\x00b")
	if r.code() != 1 || r.stdout != "" || r.err.Error() != "envmagic: value for NAME contains a NUL byte" {
		t.Fatalf("set: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
	}
	if r = run("list"); r.code() != 0 || r.stdout != "" {
		t.Errorf("list after rejected set: stdout=%q err=%v", r.stdout, r.err)
	}
}

func TestImportRejectsNUL(t *testing.T) {
	for _, source := range []string{"file", "stdin"} {
		t.Run(source, func(t *testing.T) {
			run := setup(t)
			path := filepath.Join(t.TempDir(), "input.env")
			if err := os.WriteFile(path, []byte("GOOD=valid\nBAD=a\x00b\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"import", path}
			if source == "stdin" {
				input, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				origIn := os.Stdin
				os.Stdin = input
				t.Cleanup(func() {
					os.Stdin = origIn
					_ = input.Close()
				})
				args = []string{"import"}
			}
			r := run(args...)
			if r.code() != 1 || r.stdout != "" || r.err.Error() != "envmagic: value for BAD contains a NUL byte" {
				t.Fatalf("import: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
			}
			if r = run("list"); r.code() != 0 || r.stdout != "" {
				t.Errorf("list after rejected import: stdout=%q err=%v", r.stdout, r.err)
			}
		})
	}
}

func TestNULRejectedBeforeCreation(t *testing.T) {
	for _, command := range []string{"set", "import", "import without yes"} {
		t.Run(command, func(t *testing.T) {
			run := setupBare(t)
			args := []string{"--yes", "set", "NAME", "a\x00b"}
			if command != "set" {
				path := filepath.Join(t.TempDir(), "input.env")
				if err := os.WriteFile(path, []byte("NAME=a\x00b\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				args = []string{"--yes", "import", path}
				if command == "import without yes" {
					args = []string{"import", path}
					t.Setenv("ENVMAGIC_NONINTERACTIVE", "")
					input, err := os.Open(os.DevNull)
					if err != nil {
						t.Fatal(err)
					}
					origIn := os.Stdin
					os.Stdin = input
					t.Cleanup(func() {
						os.Stdin = origIn
						_ = input.Close()
					})
				}
			}
			r := run(args...)
			if r.code() != 1 || r.stdout != "" || r.err.Error() != "envmagic: value for NAME contains a NUL byte" {
				t.Errorf("%s: exit=%d stdout=%q err=%v", command, r.code(), r.stdout, r.err)
			}
			keyPath, err := internal.KeyPath()
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{".envmagic", keyPath} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("rejected %s created %s: stat err=%v", command, path, err)
				}
			}
		})
	}
}

func TestImportEmptyDiscardsNUL(t *testing.T) {
	run := setup(t)
	path := filepath.Join(t.TempDir(), "template.env")
	if err := os.WriteFile(path, []byte("BAD=\"a\x00b\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := run("import", "--empty", path); r.code() != 0 {
		t.Fatalf("import --empty: %v", r.err)
	}
	if r := run("get", "BAD"); r.code() != 0 || r.stdout != "\n" {
		t.Errorf("get BAD: stdout=%q err=%v", r.stdout, r.err)
	}
}

func TestExportRejectsCorruptCiphertext(t *testing.T) {
	run := setup(t)
	for _, name := range []string{"A_GOOD", "Z_BAD"} {
		if r := run("set", name, "valid"); r.code() != 0 {
			t.Fatal(r.err)
		}
	}
	db, err := sql.Open("sqlite", ".envmagic")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("UPDATE env_vars SET value = ? WHERE namespace = ? AND name = ?", []byte{0x93, 0x27, 0xea}, "default", "Z_BAD"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "output.env")
	previous := []byte("unchanged\x00\xff\n")
	if err := os.WriteFile(path, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := run("export", path); r.code() == 0 || r.stdout != "" {
		t.Errorf("export: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
	}
	if content, err := os.ReadFile(path); err != nil || !bytes.Equal(content, previous) {
		t.Errorf("rejected export changed file: content=%q err=%v", content, err)
	}
}

func TestEmitRejectsStoredNUL(t *testing.T) {
	run := setup(t)
	if r := run("set", "A_GOOD", "valid"); r.code() != 0 {
		t.Fatal(r.err)
	}
	key, _, err := internal.LoadOrCreateKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := internal.Encrypt(key, []byte("a\x00b"), internal.AD("default", "Z_BAD"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", ".envmagic")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("INSERT INTO env_vars (namespace, name, value) VALUES (?, ?, ?)", "default", "Z_BAD", enc); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"load"},
		{"load", "Z_BAD"},
		{"--debug", "load"},
		{"--debug", "load", "Z_BAD"},
		{"export"},
	} {
		r := run(args...)
		if r.code() != 1 || r.stdout != "" || r.err.Error() != "envmagic: value for Z_BAD contains a NUL byte" {
			t.Errorf("%v: exit=%d stdout=%q err=%v", args, r.code(), r.stdout, r.err)
		}
		if r.stderr != "" {
			t.Errorf("%v: stderr=%q, want no partial exports", args, r.stderr)
		}
	}
	if r := run("get", "Z_BAD"); r.code() != 0 || r.stdout != "a\x00b\n" {
		t.Errorf("raw get: stdout=%q err=%v", r.stdout, r.err)
	}

	path := filepath.Join(t.TempDir(), "output.env")
	if err := os.WriteFile(path, []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := run("export", path); r.code() != 1 || r.stdout != "" || r.err.Error() != "envmagic: value for Z_BAD contains a NUL byte" {
		t.Errorf("export file: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "unchanged\n" {
		t.Errorf("rejected export changed file: content=%q err=%v", content, err)
	}
}

// TestListAndRemove covers list, its ls alias, rm, and the remove/delete aliases.
func TestListAndRemove(t *testing.T) {
	run := setup(t)

	run("alpha", "1")
	run("beta", "2")
	run("gamma", "3")

	r := run("list")
	if r.code() != 0 {
		t.Fatalf("list: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	for _, name := range []string{"ALPHA", "BETA", "GAMMA"} {
		if !strings.Contains(r.stdout, name) {
			t.Errorf("list: missing %s in output %q", name, r.stdout)
		}
	}

	r2 := run("ls")
	if r2.stdout != r.stdout {
		t.Errorf("ls alias output differs from list\nlist=%q\nls=%q", r.stdout, r2.stdout)
	}

	r = run("rm", "beta")
	if r.code() != 0 {
		t.Fatalf("rm: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	r = run("list")
	if strings.Contains(r.stdout, "BETA") {
		t.Error("rm: BETA still present after rm")
	}
	if !strings.Contains(r.stdout, "ALPHA") || !strings.Contains(r.stdout, "GAMMA") {
		t.Error("rm: ALPHA/GAMMA unexpectedly missing after rm beta")
	}

	run("remove", "alpha")
	r = run("list")
	if strings.Contains(r.stdout, "ALPHA") {
		t.Error("remove alias: ALPHA still present")
	}

	run("delete", "gamma")
	r = run("list")
	if strings.Contains(r.stdout, "GAMMA") {
		t.Error("delete alias: GAMMA still present")
	}

	r = run("rm", "nonexistent")
	if r.code() == 0 {
		t.Error("rm nonexistent: expected non-zero exit")
	}
}

func TestImportRollback(t *testing.T) {
	run := setupBare(t)
	db, err := sql.Open("sqlite", ".envmagic")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE env_vars (
		namespace TEXT NOT NULL,
		name TEXT NOT NULL CHECK (name <> 'B'),
		value BLOB NOT NULL,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (namespace, name)
	)`)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if r := run("set", "A", "old"); r.code() != 0 {
		t.Fatal(r.err)
	}
	setTestStdin(t, "A=new\nB=x\n")
	r := run("import")
	if r.code() != 1 || r.stdout != "" || r.stderr != "" || r.err == nil || !strings.Contains(r.err.Error(), "write:") || !strings.Contains(r.err.Error(), "name: B") {
		t.Fatalf("import: exit=%d stdout=%q stderr=%q err=%v", r.code(), r.stdout, r.stderr, r.err)
	}
	if r := run("get", "A"); r.code() != 0 || r.stdout != "old\n" {
		t.Errorf("get A after failed import: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
	}
	if r := run("get", "B"); r.code() != 1 || r.stdout != "" {
		t.Errorf("get B after failed import: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
	}
}

// TestImportAndExport covers importing a .env file, verifying the stored
// values, and exporting them back to both stdout and a file.
func TestImportAndExport(t *testing.T) {
	run := setup(t)
	if _, _, err := internal.LoadOrCreateKey(); err != nil {
		t.Fatal(err)
	}

	envContent := strings.Join([]string{
		"# this is a comment",
		"",
		"DB_HOST=localhost",
		`API_SECRET="tok-abc-123"`,
		"export PORT=5432",
	}, "\n") + "\n"

	envFile := filepath.Join(t.TempDir(), "input.env")
	if err := os.WriteFile(envFile, []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}

	r := run("import", envFile)
	if r.code() != 0 {
		t.Fatalf("import: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	wantStderr := fmt.Sprintf("envmagic: imported 3 variable(s) from %s into namespace %q\n", envFile, "default")
	if r.stderr != wantStderr {
		t.Errorf("import: stderr=%q, want %q", r.stderr, wantStderr)
	}

	for varName, wantVal := range map[string]string{
		"db_host":    "localhost",
		"api_secret": "tok-abc-123",
		"port":       "5432",
	} {
		r = run(varName)
		if r.code() != 0 {
			t.Errorf("get %s after import: exit %d\nstderr: %s", varName, r.code(), r.stderr)
			continue
		}
		if !strings.Contains(r.stdout, wantVal) {
			t.Errorf("get %s: stdout %q does not contain %q", varName, r.stdout, wantVal)
		}
	}

	r = run("export")
	if r.code() != 0 {
		t.Fatalf("export stdout: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	wantExport := "API_SECRET=\"tok-abc-123\"\nDB_HOST=\"localhost\"\nPORT=\"5432\"\n"
	if r.stdout != wantExport {
		t.Errorf("export stdout: got %q, want %q", r.stdout, wantExport)
	}

	outFile := filepath.Join(t.TempDir(), "output.env")
	r = run("export", outFile)
	if r.code() != 0 {
		t.Fatalf("export file: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	if !strings.Contains(r.stderr, "exported 3 variable(s)") {
		t.Errorf("export file: unexpected confirmation %q", r.stderr)
	}
	exported, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read exported file: %v", err)
	}
	if string(exported) != wantExport {
		t.Errorf("exported file: got %q, want %q", exported, wantExport)
	}
}

func TestLongValueExportImport(t *testing.T) {
	run := setup(t)
	value := strings.Repeat("a\"$\n", 17500)
	if r := run("set", "LONG", value); r.code() != 0 {
		t.Fatal(r.err)
	}
	path := filepath.Join(t.TempDir(), "export.env")
	if r := run("export", path); r.code() != 0 {
		t.Fatal(r.err)
	}
	if r := run("-n", "imported", "import", path); r.code() != 0 {
		t.Fatal(r.err)
	}

	r := run("-n", "imported", "get", "LONG")
	if r.code() != 0 {
		t.Fatal(r.err)
	}
	if r.stdout != value+"\n" {
		t.Fatalf("get: got %d bytes, want exact value plus newline", len(r.stdout))
	}
	r = run("-n", "imported", "--debug", "load")
	if r.code() != 0 {
		t.Fatal(r.err)
	}
	if r.stderr != r.stdout || len(r.stderr) <= 64<<10 {
		t.Fatalf("debug load: stdout %d bytes, stderr %d bytes, want identical output over 64 KiB", len(r.stdout), len(r.stderr))
	}
}

func TestDotenvLineTooLong(t *testing.T) {
	_, err := parseDotenv(strings.NewReader("# comment\n\nLONG=" + strings.Repeat("a", 16<<20)))
	if err == nil || err.Error() != "line 3: bufio.Scanner: token too long" {
		t.Fatalf("got %v, want scanner error on line 3", err)
	}
	entries, err := parseDotenv(strings.NewReader("LONG=" + strings.Repeat("a", 16<<20-6) + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || len(entries[0][1]) != 16<<20-6 {
		t.Fatal("want one entry with the largest accepted value")
	}
}

func TestDotenvReaderError(t *testing.T) {
	want := errors.New("reader failed")
	_, err := parseDotenv(io.MultiReader(strings.NewReader("A=1\nB=2\n"), iotest.ErrReader(want)))
	if !errors.Is(err, want) || strings.HasPrefix(err.Error(), "line ") {
		t.Fatalf("got %v, want reader error without a line prefix", err)
	}
}

func TestRawByteRoundTrips(t *testing.T) {
	bash, bashErr := exec.LookPath("bash")
	binDir := t.TempDir()
	if bashErr == nil {
		build := exec.Command("go", "build", "-o", filepath.Join(binDir, "envmagic"), "./")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build envmagic: %v\n%s", err, out)
		}
	}
	run := setup(t)
	for i := 1; i <= 255; i++ {
		name := fmt.Sprintf("BYTE_%03d", i)
		if r := run("-n", "bytes", "set", "--", name, string([]byte{byte(i)})); r.code() != 0 {
			t.Fatalf("set %s: %v", name, r.err)
		}
	}

	t.Run("export-import", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "export.env")
		if r := run("-n", "bytes", "export", path); r.code() != 0 {
			t.Fatal(r.err)
		}
		exported, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range []string{`BYTE_009="\t"`, `BYTE_013="\r"`, `BYTE_036="\$"`, "BYTE_096=\"\\`\""} {
			if !strings.Contains("\n"+string(exported), "\n"+line+"\n") {
				t.Errorf("export missing line %q", line)
			}
		}
		if r := run("-n", "imported", "import", path); r.code() != 0 {
			t.Fatal(r.err)
		}
		for i := 1; i <= 255; i++ {
			name := fmt.Sprintf("BYTE_%03d", i)
			want := string([]byte{byte(i), '\n'})
			if r := run("-n", "imported", "get", name); r.code() != 0 || r.stdout != want {
				t.Errorf("byte 0x%02x: stdout=%q err=%v, want %q", i, r.stdout, r.err, want)
			}
		}
	})

	t.Run("load-bash", func(t *testing.T) {
		if bashErr != nil {
			t.Skip("bash is not on PATH")
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("LC_ALL", "C")
		var script strings.Builder
		script.WriteString(`eval "$(command envmagic -n bytes load)"` + "\n")
		var want []byte
		for i := 1; i <= 255; i++ {
			fmt.Fprintf(&script, "printf %%s \"$BYTE_%03d\"\n", i)
			want = append(want, byte(i))
		}
		out, err := exec.Command(bash, "--noprofile", "--norc", "-c", script.String()).CombinedOutput()
		if err != nil || !bytes.Equal(out, want) {
			t.Fatalf("load: combined output=%q err=%v, want %q", out, err, want)
		}
	})
}

func TestImportNamespaceBinding(t *testing.T) {
	run := setup(t)
	const value = "staging secret with spaces"
	path := filepath.Join(t.TempDir(), "input.env")
	if err := os.WriteFile(path, []byte("TOKEN="+value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := run("-n", "staging", "import", path); r.code() != 0 {
		t.Fatal(r.err)
	}
	if r := run("-n", "staging", "get", "TOKEN"); r.code() != 0 || r.stdout != value+"\n" {
		t.Errorf("get imported value: stdout=%q err=%v", r.stdout, r.err)
	}

	db, err := sql.Open("sqlite", ".envmagic")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	res, err := db.Exec(`UPDATE env_vars SET namespace = 'default' WHERE namespace = 'staging' AND name = 'TOKEN'`)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("move: affected=%d err=%v", n, err)
	}
	if r := run("-n", "default", "get", "TOKEN"); r.code() != 1 || r.stdout != "" || r.err == nil || !strings.Contains(r.err.Error(), "message authentication failed") {
		t.Errorf("get moved ciphertext: stdout=%q err=%v", r.stdout, r.err)
	}
}

// TestImportTemplate covers the --empty and --interactive import modes used to
// populate the store from an example .env file.
func TestImportTemplate(t *testing.T) {
	run := setup(t)

	tmplContent := strings.Join([]string{
		"# example env file",
		"API_KEY=",
		"DB_PORT=5432",
		`DB_URL="postgres://localhost/dev"`,
	}, "\n") + "\n"

	tmplFile := filepath.Join(t.TempDir(), ".env.example")
	if err := os.WriteFile(tmplFile, []byte(tmplContent), 0o600); err != nil {
		t.Fatal(err)
	}

	r := run("import", "--empty", tmplFile)
	if r.code() != 0 {
		t.Fatalf("import --empty: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	if !strings.Contains(r.stderr, "imported 3 variable(s)") {
		t.Errorf("import --empty: unexpected confirmation %q", r.stderr)
	}
	r = run("load")
	for _, name := range []string{"API_KEY", "DB_PORT", "DB_URL"} {
		want := `export ` + name + `=""`
		if !strings.Contains(r.stdout, want) {
			t.Errorf("import --empty: stdout %q does not contain %q", r.stdout, want)
		}
	}

	r = run("import", "--interactive", "--empty", tmplFile)
	if r.code() != 2 {
		t.Errorf("import -i --empty: expected exit 2, got %d", r.code())
	}

	r = run("import", "--interactive")
	if r.code() != 2 {
		t.Errorf("import -i without FILE: expected exit 2, got %d", r.code())
	}

	r = run("import", "-i", tmplFile)
	if r.code() == 0 {
		t.Error("import -i without TTY: expected non-zero exit")
	}
	if !strings.Contains(r.err.Error(), "--empty") {
		t.Errorf("import -i without TTY: expected hint about --empty, got %q", r.err)
	}

	badFile := filepath.Join(t.TempDir(), "bad.env")
	if err := os.WriteFile(badFile, []byte("GOOD=1\nbroken line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = run("import", "--empty", badFile)
	if r.code() == 0 {
		t.Error("import --empty malformed: expected non-zero exit")
	}
	if !strings.Contains(r.err.Error(), "line 2") {
		t.Errorf("import --empty malformed: expected line number in error, got %q", r.err)
	}
}

func TestDefaultDescription(t *testing.T) {
	if got := defaultDescription("secret-value", true); got != "(default set)" {
		t.Fatalf("secret default: got %q", got)
	}
	if got := defaultDescription("5432", false); got != "default: 5432" {
		t.Fatalf("ordinary default: got %q", got)
	}
	if got := defaultDescription("", false); got != "(no default)" {
		t.Fatalf("empty default: got %q", got)
	}
	if !looksSecret("DB_PASSWORD") {
		t.Fatal("PASSWORD should still be recognized via PASS")
	}
}

// TestNamespaces verifies that entries in different namespaces are fully
// isolated from each other.
func TestNamespaces(t *testing.T) {
	run := setup(t)

	run("-n", "dev", "set", "db_url", "postgres://dev-host/devdb")
	run("-n", "prod", "db_url", "postgres://prod-host/proddb")

	r := run("-n", "dev", "get", "db_url")
	if r.code() != 0 || r.stdout != "postgres://dev-host/devdb\n" {
		t.Errorf("dev get: expected dev-host, stdout=%q", r.stdout)
	}
	r = run("-n", "dev", "load", "db_url")
	if r.code() != 0 || r.stdout != "export DB_URL=\"postgres://dev-host/devdb\"\n" {
		t.Errorf("dev load: stdout=%q err=%v", r.stdout, r.err)
	}

	r = run("-n", "prod", "db_url")
	if !strings.Contains(r.stdout, "prod-host") {
		t.Errorf("prod get: expected prod-host, stdout=%q", r.stdout)
	}

	run("-n", "dev", "dev_secret", "only-in-dev")
	run("-n", "prod", "prod_secret", "only-in-prod")

	r = run("-n", "dev", "list")
	if strings.Contains(r.stdout, "PROD_SECRET") {
		t.Error("dev list: PROD_SECRET leaked into dev namespace")
	}
	if !strings.Contains(r.stdout, "DEV_SECRET") {
		t.Error("dev list: DEV_SECRET missing from dev namespace")
	}

	r = run("-n", "prod", "list")
	if strings.Contains(r.stdout, "DEV_SECRET") {
		t.Error("prod list: DEV_SECRET leaked into prod namespace")
	}
	if !strings.Contains(r.stdout, "PROD_SECRET") {
		t.Error("prod list: PROD_SECRET missing from prod namespace")
	}

	run("-n", "dev", "rm", "db_url")
	r = run("-n", "prod", "db_url")
	if r.code() != 0 {
		t.Error("prod db_url should survive rm in dev namespace")
	}
}

// TestShellInit verifies that each supported shell produces non-empty output,
// that bash and zsh share the same POSIX script, and that unknown shells fail.
func TestShellInit(t *testing.T) {
	run := setup(t)

	bash := run("shell-init", "bash")
	zsh := run("shell-init", "zsh")

	if bash.code() != 0 {
		t.Fatalf("shell-init bash: exit %d", bash.code())
	}
	if zsh.code() != 0 {
		t.Fatalf("shell-init zsh: exit %d", zsh.code())
	}
	if bash.stdout != zsh.stdout {
		t.Error("bash and zsh should produce identical POSIX init scripts")
	}
	if !strings.Contains(bash.stdout, "envmagic()") {
		t.Errorf("posix init: expected 'envmagic()' function definition, got %q", bash.stdout)
	}
	if !strings.Contains(bash.stdout, "envmagic: environment variables set") {
		t.Errorf("posix init: expected load confirmation, got %q", bash.stdout)
	}
	for _, want := range []string{`for _envmagic_arg in "$@"`, "-n|--namespace)", "-h|--help|-v|--version)", `"$_envmagic_command" != load`} {
		if !strings.Contains(bash.stdout, want) {
			t.Errorf("posix init: missing %q", want)
		}
	}

	fish := run("shell-init", "fish")
	if fish.code() != 0 {
		t.Fatalf("shell-init fish: exit %d", fish.code())
	}
	if !strings.Contains(fish.stdout, "function envmagic") {
		t.Errorf("fish init: expected 'function envmagic', got %q", fish.stdout)
	}
	if !strings.Contains(fish.stdout, "envmagic: environment variables set") {
		t.Errorf("fish init: expected load confirmation, got %q", fish.stdout)
	}
	if fish.stdout == bash.stdout {
		t.Error("fish init should differ from POSIX init")
	}
	for _, want := range []string{"for _envmagic_arg in $argv", "case -n --namespace", "case -h --help -v --version", `"$_envmagic_command" != load`} {
		if !strings.Contains(fish.stdout, want) {
			t.Errorf("fish init: missing %q", want)
		}
	}

	r := run("shell-init", "powershell")
	if r.code() == 0 {
		t.Error("unknown shell: expected non-zero exit")
	}

	r = run("shell-init")
	if r.code() == 0 {
		t.Error("shell-init no args: expected non-zero exit")
	}
}

func TestShellWrapper(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("bash is required in CI")
		}
		t.Skip("bash is not on PATH")
	}
	binDir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(binDir, "envmagic"), "./")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build envmagic: %v\n%s", err, out)
	}
	help, err := exec.Command(filepath.Join(binDir, "envmagic"), "--help").Output()
	if err != nil {
		t.Fatal(err)
	}
	loadHelp, err := exec.Command(filepath.Join(binDir, "envmagic"), "load", "--help").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	hostileHome := t.TempDir()
	hostileFile := filepath.Join(hostileHome, ".zshenv")
	if err := os.WriteFile(hostileFile, []byte("envmagic() { echo hijacked; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASH_ENV", hostileFile)
	t.Setenv("ENV", hostileFile)
	t.Setenv("HOME", hostileHome)
	t.Setenv("ZDOTDIR", hostileHome)

	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			path, err := exec.LookPath(shell)
			if err != nil {
				if os.Getenv("CI") != "" {
					t.Fatalf("%s is required in CI", shell)
				}
				t.Skip(shell + " is not on PATH")
			}
			run := setup(t)
			home := os.Getenv("HOME")
			value := `raw "quotes" $cash`
			multiline := "-----BEGIN KEY-----\n  abc\ndef\n-----END KEY-----\n"
			for _, args := range [][]string{
				{"set", "name", value},
				{"-n", "staging", "set", "name", "staging value"},
				{"-n", "staging", "set", "other", "second value"},
				{"-n", "multiline", "set", "--", "name", multiline},
				{"-n", "multiline", "set", "other", "second value"},
				{"-n", "ro", "set", "PWD", "readonly value"},
				{"-n", "ro-early", "set", "PWD", "readonly value"},
				{"-n", "ro-early", "set", "ZZZ", "later value"},
			} {
				if r := run(args...); r.code() != 0 {
					t.Fatalf("seed %v: %v", args, r.err)
				}
			}
			init := `eval "$(envmagic shell-init ` + shell + `)"` + "\n"
			status := "$?"
			checkExport := ""
			evalFailure := `readonly NAME; envmagic load; echo "rc=$?"`
			earlyEvalFailure := `readonly PWD; envmagic -n ro-early load; echo "rc=$?"; printf %s "$ZZZ"`
			plainEvalFailure := `readonly PWD; eval "$(command envmagic -n ro-early load)"; echo "rc=$?"; printf %s "$ZZZ"`
			evalError := "readonly variable"
			if shell == "zsh" {
				evalError = "read-only variable"
			}
			if shell == "fish" {
				init = "envmagic shell-init fish | source\n"
				status = "$status"
				checkExport = `; set -q -g export; and echo stray; true`
				evalFailure = `envmagic -n ro load; echo "rc=$status"`
				earlyEvalFailure = `envmagic -n ro-early load; echo "rc=$status"; printf %s "$ZZZ"`
				plainEvalFailure = `eval (command envmagic -n ro-early load); echo "rc=$status"; printf %s "$ZZZ"`
				evalError = "read-only variable"
			}
			confirm := "envmagic: environment variables set\n"
			for _, tc := range []struct {
				command string
				want    string
				wantErr string
			}{
				{`envmagic -n staging list`, "NAME\nOTHER\n", ""},
				{`envmagic --namespace staging list`, "NAME\nOTHER\n", ""},
				{`envmagic --namespace=staging list`, "NAME\nOTHER\n", ""},
				{`envmagic get NAME`, value + "\n", ""},
				{`envmagic NAME`, value + "\n", ""},
				{`envmagic load MISSING; echo "rc=` + status + `"`, "rc=1\n", "envmagic: MISSING not found in namespace \"default\"\n"},
				{`envmagic get MISSING; echo "rc=` + status + `"`, "rc=1\n", "envmagic: MISSING not found in namespace \"default\"\n"},
				{`ENVMAGIC_NONINTERACTIVE=yes envmagic list; echo "rc=` + status + `"`, "rc=1\n", "envmagic: could not parse \"yes\" as bool value from environment variable \"ENVMAGIC_NONINTERACTIVE\" for flag yes: parse error\n"},
				{`envmagic load -x; echo "rc=` + status + `"`, "rc=1\n", "Incorrect Usage: flag provided but not defined: -x\n"},
				{evalFailure, "rc=1\n", evalError},
				{earlyEvalFailure, "rc=1\n", evalError},
				{plainEvalFailure, "rc=1\n", evalError},
				{`envmagic load A B; echo "rc=` + status + `"`, "rc=2\n", "usage: envmagic load [-n NS] [NAME]\n"},
				{`envmagic -n empty load`, "", ""},
				{`envmagic -n empty load; echo "rc=` + status + `"`, "rc=0\n", ""},
				{`envmagic`, string(help), ""},
				{`envmagic -n staging`, string(help), ""},
				{`envmagic >/dev/null; printf %s "$NAME"`, "", ""},
				{`envmagic -n staging >/dev/null; printf %s "$NAME"`, "", ""},
				{`envmagic load NAME; printf %s "$NAME"`, value, ""},
				{`envmagic -n multiline load NAME; printf %s "$NAME"` + checkExport, multiline, ""},
				{`envmagic -n multiline load; printf %s "$NAME"; printf %s "$OTHER"` + checkExport, multiline + "second value", confirm},
				{`envmagic -n staging load NAME; printf %s "$NAME"`, "staging value", ""},
				{`envmagic load NAME -n staging; printf %s "$NAME"`, "staging value", ""},
				{`envmagic load; printf %s "$NAME"`, value, confirm},
				{`envmagic -n staging load; printf '%s/%s' "$NAME" "$OTHER"`, "staging value/second value", confirm},
				{`envmagic load -n staging; printf '%s/%s' "$NAME" "$OTHER"`, "staging value/second value", confirm},
				{`envmagic --namespace staging load; printf %s "$NAME"`, "staging value", confirm},
				{`envmagic --namespace=staging load; printf %s "$NAME"`, "staging value", confirm},
				{`envmagic -n staging --version`, "envmagic version v0.5.0\n", ""},
				{`envmagic -n staging -v`, "envmagic version v0.5.0\n", ""},
				{`envmagic -n staging --help`, string(help), ""},
				{`envmagic -n staging -h`, string(help), ""},
				{`envmagic load --help`, string(loadHelp), ""},
			} {
				cmd := exec.Command(path, "-c", init+tc.command)
				cmd.Env = []string{
					"HOME=" + home,
					"ZDOTDIR=" + home,
					"PATH=" + os.Getenv("PATH"),
					"XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"),
					"AppData=" + os.Getenv("AppData"),
				}
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				if err != nil || string(out) != tc.want {
					t.Errorf("%s: err=%v stdout=%q want=%q stderr=%q", tc.command, err, out, tc.want, stderr.String())
				}
				if tc.command == evalFailure || tc.command == earlyEvalFailure || tc.command == plainEvalFailure {
					if got := stderr.String(); !strings.Contains(got, tc.wantErr) || strings.Contains(got, confirm) {
						t.Errorf("%s: stderr=%q, want %q without confirmation", tc.command, got, tc.wantErr)
					}
				} else if got := stderr.String(); got != tc.wantErr {
					t.Errorf("%s: stderr=%q, want %q", tc.command, got, tc.wantErr)
				}
			}
		})
	}
}

// TestSourceAll verifies that calling envmagic load with no name
// exports all variables in the active namespace as shell-sourceable export lines.
func TestSourceAll(t *testing.T) {
	run := setup(t)

	run("db_host", "localhost")
	run("port", "5432")
	run("-n", "staging", "db_host", "staging-host")
	run("-n", "staging", "api_key", "stg-secret")

	r := run("load")
	if r.code() != 0 {
		t.Fatalf("source-all default: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	for _, want := range []string{"export DB_HOST=\"localhost\" &&\n", "export PORT=\"5432\"\n"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("source-all default: stdout %q does not contain %q", r.stdout, want)
		}
	}
	if strings.Contains(r.stdout, "staging") {
		t.Error("source-all default: staging values leaked into default output")
	}

	r = run("-n", "staging", "load")
	if r.code() != 0 {
		t.Fatalf("source-all staging: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	for _, want := range []string{"export DB_HOST=\"staging-host\"\n", "export API_KEY=\"stg-secret\" &&\n"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("source-all staging: stdout %q does not contain %q", r.stdout, want)
		}
	}
	if strings.Contains(r.stdout, "localhost") || strings.Contains(r.stdout, "5432") {
		t.Error("source-all staging: default values leaked into staging output")
	}

	r = run("--debug", "load")
	if r.code() != 0 {
		t.Fatalf("source-all --debug: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	want := "export DB_HOST=\"localhost\" &&\nexport PORT=\"5432\"\n"
	if r.stdout != want || r.stderr != r.stdout {
		t.Errorf("source-all --debug: stdout=%q stderr=%q, want %q on both", r.stdout, r.stderr, want)
	}

	r = run("-n", "empty-ns", "load")
	if r.code() != 0 {
		t.Fatalf("source-all empty namespace: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	if strings.TrimSpace(r.stdout) != "" {
		t.Errorf("source-all empty namespace: expected no stdout, got %q", r.stdout)
	}
}

func TestDefaultHelp(t *testing.T) {
	run := setupBare(t)
	for _, args := range [][]string{nil, {"-n", "staging"}, {"--debug"}, {"--help"}, {"-h"}, {"help"}, {"help", "set"}, {"set", "--help"}, {"set", "-h"}} {
		r := run(args...)
		if r.code() != 0 || !strings.Contains(r.stdout, "USAGE:") || r.stderr != "" {
			t.Errorf("%v: exit=%d stdout=%q stderr=%q", args, r.code(), r.stdout, r.stderr)
		}
	}
}

// TestImportCreatesStoreWithYes verifies import can create .envmagic when none exists.
func TestImportCreatesStoreWithYes(t *testing.T) {
	run := setupBare(t)
	envContent := "ONLY_KEY=only-val\n"
	envFile := filepath.Join(t.TempDir(), "in.env")
	if err := os.WriteFile(envFile, []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	r := run("import", "--yes", envFile)
	if r.code() != 0 {
		t.Fatalf("import --yes: exit %d stderr=%q", r.code(), r.stderr)
	}
	if _, err := os.Stat(".envmagic"); err != nil {
		t.Fatalf("expected .envmagic in cwd: %v", err)
	}
	r = run("only_key")
	if r.code() != 0 || !strings.Contains(r.stdout, "only-val") {
		t.Fatalf("get after import: exit=%d stdout=%q stderr=%q", r.code(), r.stdout, r.stderr)
	}
}

func TestImportCreatesStoreWithEnvNonInteractive(t *testing.T) {
	run := setupBare(t)
	t.Setenv("ENVMAGIC_NONINTERACTIVE", "1")
	envFile := filepath.Join(t.TempDir(), "in.env")
	if err := os.WriteFile(envFile, []byte("X=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := run("import", envFile)
	if r.code() != 0 {
		t.Fatalf("import with ENVMAGIC_NONINTERACTIVE: exit %d stderr=%q", r.code(), r.stderr)
	}
	if _, err := os.Stat(".envmagic"); err != nil {
		t.Fatalf("expected .envmagic: %v", err)
	}
}

func TestNewEncryptionKeyOutput(t *testing.T) {
	for _, command := range []string{"set", "import"} {
		for _, terminal := range []bool{false, true} {
			name := "non-terminal"
			if terminal {
				name = "terminal"
			}
			t.Run(command+"/"+name, func(t *testing.T) {
				run := setupBare(t)
				if terminal {
					original := stderrIsTerminal
					stderrIsTerminal = func() bool { return true }
					t.Cleanup(func() { stderrIsTerminal = original })
				}

				args := []string{"--yes", "set", "TOKEN", "secret"}
				if command == "import" {
					setTestStdin(t, "TOKEN=secret\n")
					args = []string{"--yes", "import"}
				}
				r := run(args...)
				if r.code() != 0 {
					t.Fatalf("%s: exit=%d stderr=%q", command, r.code(), r.stderr)
				}
				path, err := internal.KeyPath()
				if err != nil {
					t.Fatal(err)
				}
				key, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				encoded := base64.StdEncoding.EncodeToString(key)
				if !terminal && (strings.Contains(r.stdout, encoded) || strings.Contains(r.stderr, encoded)) {
					t.Error("key leaked into non-terminal output")
				}
				want := fmt.Sprintf("envmagic: generated new encryption key at %s\n", path)
				if terminal {
					want += fmt.Sprintf("envmagic: key (base64): %s\n", encoded)
					want += "envmagic: You can display the key again later by running `envmagic key`.\n"
				} else {
					want += "envmagic: run `envmagic key` on a terminal to see the key\n"
				}
				want += "envmagic: BACK THIS FILE UP - without it, stored values cannot be decrypted.\n"
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				if command == "import" {
					want += "envmagic: imported 1 variable(s) from stdin into namespace \"default\"\n"
				} else {
					want += fmt.Sprintf("envmagic: stored TOKEN (namespace %q) in %s\n", "default", filepath.Join(cwd, ".envmagic"))
				}
				if r.stdout != "" || r.stderr != want {
					t.Errorf("%s: stdout=%q stderr=%q, want empty stdout and stderr=%q", command, r.stdout, r.stderr, want)
				}
			})
		}
	}
}

func TestSetCreatesStoreWithYes(t *testing.T) {
	for _, args := range [][]string{
		{"--yes", "foo_key", "bar"},
		{"--yes", "set", "foo_key", "bar"},
		{"set", "foo_key", "bar"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			run := setupBare(t)
			if args[0] == "set" {
				t.Setenv("ENVMAGIC_NONINTERACTIVE", "1")
			}
			r := run(args...)
			if r.code() != 0 {
				t.Fatalf("set: exit %d stderr=%q", r.code(), r.stderr)
			}
			if !strings.Contains(r.stderr, "BACK THIS FILE UP") {
				t.Fatalf("set: missing key backup warning in stderr=%q", r.stderr)
			}
			if _, err := os.Stat(".envmagic"); err != nil {
				t.Fatalf("expected .envmagic: %v", err)
			}
			r = run("get", "foo_key")
			if r.code() != 0 || r.stdout != "bar\n" {
				t.Fatalf("get: exit=%d stdout=%q", r.code(), r.stdout)
			}
		})
	}
}

func TestInvalidStoredNames(t *testing.T) {
	for label, name := range map[string]string{"shell": "X=1; touch /tmp/x; #", "newline": "X\n", "escape": "X\x1b"} {
		t.Run(label, func(t *testing.T) {
			run := setup(t)
			if result := run("set", "A_VALID", "safe"); result.code() != 0 {
				t.Fatalf("set: %v", result.err)
			}
			db, err := sql.Open("sqlite", ".envmagic")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if _, err := db.Exec(`INSERT INTO env_vars (namespace, name, value) SELECT namespace, ?, value FROM env_vars WHERE name = 'A_VALID'`, name); err != nil {
				t.Fatal(err)
			}

			for _, command := range []string{"load", "list", "export"} {
				result := run(command)
				if result.code() == 0 || result.stdout != "" {
					t.Errorf("%s: exit=%d stdout=%q err=%v", command, result.code(), result.stdout, result.err)
				}
				want := fmt.Sprintf("invalid variable name %q in store", name)
				if result.err == nil || !strings.Contains(result.err.Error(), want) {
					t.Errorf("%s: err=%v, want %q", command, result.err, want)
				}
			}
		})
	}
}

func TestRemoveInvalidName(t *testing.T) {
	run := setup(t)
	if r := run("set", "A_VALID", "safe"); r.code() != 0 {
		t.Fatal(r.err)
	}
	db, err := sql.Open("sqlite", ".envmagic")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO env_vars (namespace, name, value) SELECT namespace, 'HAS-HYPHEN', value FROM env_vars WHERE name = 'A_VALID'`); err != nil {
		t.Fatal(err)
	}

	r := run("rm", "has-hyphen")
	if r.code() == 0 || r.stdout != "" || r.err == nil || !strings.Contains(r.err.Error(), "invalid env var name") {
		t.Errorf("rm: exit=%d stdout=%q err=%v", r.code(), r.stdout, r.err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM env_vars WHERE namespace = 'default' AND name = 'HAS-HYPHEN'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("rm invalid name: stored rows=%d, want 1", count)
	}
}

func TestImportInvalidName(t *testing.T) {
	run := setup(t)
	envFile := filepath.Join(t.TempDir(), "invalid.env")
	if err := os.WriteFile(envFile, []byte("GOOD=1\nhas-hyphen=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := run("import", envFile)
	want := `envmagic: parse: line 2: invalid variable name "has-hyphen"`
	if r.code() == 0 || r.stdout != "" || r.err == nil || r.err.Error() != want {
		t.Errorf("import: exit=%d stdout=%q err=%v, want %q", r.code(), r.stdout, r.err, want)
	}
	db, err := sql.Open("sqlite", ".envmagic")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM env_vars`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("import invalid name: stored rows=%d, want 0", count)
	}
}

// TestInputValidation covers malformed variable names, wrong argument counts,
// and other usage errors that must produce a non-zero exit code.
func TestInputValidation(t *testing.T) {
	run := setup(t)

	for _, badName := range []string{"123start", "has-hyphen", "has space", "has.dot", " TOKEN "} {
		for _, args := range [][]string{{badName, "value"}, {"get", badName}, {"load", badName}, {"set", badName, "value"}} {
			r := run(args...)
			if r.code() != 2 || r.stdout != "" || r.err == nil || !strings.Contains(r.err.Error(), "invalid env var name") {
				t.Errorf("invalid name %v: exit=%d stdout=%q err=%v; want invalid env var name with exit 2", args, r.code(), r.stdout, r.err)
			}
		}
	}
	for _, args := range [][]string{
		{"get"},
		{"get", "name", "extra"},
		{"load", "A", "B"},
		{"set"},
		{"set", "name", "value", "extra"},
		{"set", "TOKEN", "-", "extra"},
	} {
		r := run(args...)
		if r.code() != 2 || !strings.HasPrefix(r.err.Error(), "usage: envmagic "+args[0]) {
			t.Errorf("%v: expected usage error with exit 2, got exit=%d err=%v", args, r.code(), r.err)
		}
	}

	r := run("valid_key", "value", "extra")
	if r.code() == 0 {
		t.Error("too many positional args: expected non-zero exit")
	}

	for _, args := range [][]string{
		{"rm"},
		{"rm", "key1", "key2"},
	} {
		r = run(args...)
		if r.code() == 0 {
			t.Errorf("rm %v: expected non-zero exit", args[1:])
		}
	}

	r = run("list", "unexpected")
	if r.code() != 2 {
		t.Errorf("list with args: exit=%d, want 2", r.code())
	}

	r = run("export", "file1.env", "file2.env")
	if r.code() != 2 {
		t.Errorf("export two paths: exit=%d, want 2", r.code())
	}

	r = run("import", "file1.env", "file2.env")
	if r.code() != 2 {
		t.Errorf("import two paths: exit=%d, want 2", r.code())
	}
}

func TestImportEscapes(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"newline", `KEY="a\nb"`, "a\nb\n"},
		{"backslash", `KEY="a\\b"`, "a\\b\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := setup(t)
			path := filepath.Join(t.TempDir(), "input.env")
			if err := os.WriteFile(path, []byte(tc.input+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if r := run("import", path); r.code() != 0 {
				t.Fatal(r.err)
			}
			if r := run("get", "KEY"); r.code() != 0 || r.stdout != tc.want {
				t.Fatalf("stdout=%q err=%v, want %q", r.stdout, r.err, tc.want)
			}
		})
	}
}

func TestImportQuotedValues(t *testing.T) {
	for _, tc := range []struct{ name, input, want, wantErr string }{
		{"double trailing garbage", `B="first"second`, "", "line 1: unexpected characters after quoted value"},
		{"single trailing garbage", `B='first'second`, "", "line 1: unexpected characters after quoted value"},
		{"double garbage before comment", `A="x"garbage#comment`, "", "line 1: unexpected characters after quoted value"},
		{"single garbage before comment", `A='x'garbage#comment`, "", "line 1: unexpected characters after quoted value"},
		{"second line garbage", "# comment\nA=\"x\" second", "", "line 2: unexpected characters after quoted value"},
		{"double leading space", `A= "quoted"`, "quoted", ""},
		{"single leading tab", "A=\t'quoted'", "quoted", ""},
		{"spaced comment", `A="x"   # comment`, "x", ""},
		{"adjacent comment", `A="x"#c`, "x", ""},
		{"single comment", `A='x'#c`, "x", ""},
		{"trailing whitespace", "A=\"x\" \t\r\n", "x", ""},
		{"BOM", "\uFEFFA=1", "1", ""},
		{"indented BOM", " \t\uFEFFA=1", "", `line 1: invalid variable name "\ufeffA"`},
		{"later BOM", "# comment\n\uFEFFA=1", "", "line 2: invalid variable name"},
		{"duplicate", "A=1\nA=2", "2", ""},
		{"unquoted unchanged", "A= \tx #c \t", " \tx #c", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := setup(t)
			if _, _, err := internal.LoadOrCreateKey(); err != nil {
				t.Fatal(err)
			}
			setTestStdin(t, tc.input)
			r := run("import")
			if tc.wantErr != "" {
				if r.code() != 1 || r.err == nil || !strings.Contains(r.err.Error(), tc.wantErr) {
					t.Fatalf("exit=%d err=%v, want %q", r.code(), r.err, tc.wantErr)
				}
				return
			}
			wantSummary := "envmagic: imported 1 variable(s) from stdin into namespace \"default\"\n"
			if r.code() != 0 || r.stderr != wantSummary {
				t.Fatalf("exit=%d stderr=%q err=%v, want %q", r.code(), r.stderr, r.err, wantSummary)
			}
			if r := run("get", "A"); r.code() != 0 || r.stdout != tc.want+"\n" {
				t.Fatalf("stdout=%q err=%v, want %q", r.stdout, r.err, tc.want+"\n")
			}
		})
	}
}

func TestImportUnterminatedQuotes(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"single", "KEY='value"},
		{"double", `KEY="value`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := setup(t)
			path := filepath.Join(t.TempDir(), "input.env")
			if err := os.WriteFile(path, []byte(tc.input+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			r := run("import", path)
			want := "envmagic: parse: line 1: unterminated " + tc.name + "-quoted value"
			if r.code() != 1 || r.stdout != "" || r.err == nil || r.err.Error() != want {
				t.Fatalf("exit=%d stdout=%q err=%v, want %q", r.code(), r.stdout, r.err, want)
			}
		})
	}
}

func TestFindEnvmagicSkipsDirectory(t *testing.T) {
	run := setup(t)
	if r := run("set", "KEY", "parent value"); r.code() != 0 {
		t.Fatal(r.err)
	}
	if err := os.MkdirAll(filepath.Join("child", ".envmagic"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir("child")
	if r := run("get", "KEY"); r.code() != 0 || r.stdout != "parent value\n" {
		t.Fatalf("stdout=%q err=%v; want value from parent store", r.stdout, r.err)
	}
}

func TestKeySetPermissions(t *testing.T) {
	permissiveUmask(t)
	run := setupBare(t)
	key := bytes.Repeat([]byte{1}, 32)
	if r := run("key", "--set", base64.StdEncoding.EncodeToString(key)); r.code() != 0 {
		t.Fatal(r.err)
	}
	path, err := internal.KeyPath()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("key=%x err=%v, want %x", got, err, key)
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
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	key = bytes.Repeat([]byte{2}, 32)
	if r := run("key", "--set", base64.StdEncoding.EncodeToString(key)); r.code() != 0 {
		t.Fatal(r.err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("key=%x err=%v, want %x", got, err, key)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("overwritten key: mode=%#o, want 0600", got)
	}
}

func TestKeyPermissionsWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not report Unix permission bits")
	}
	for _, kind := range []string{"file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			run := setup(t)
			path := isolateKeyPath(t)
			if r := run("set", "TOKEN", "secret"); r.code() != 0 {
				t.Fatal(r.err)
			}
			if kind == "symlink" {
				target := filepath.Join(t.TempDir(), "key")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o620, 0o602, 0o610, 0o601, 0o600, 0o700} {
				t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
					if err := os.Chmod(path, mode); err != nil {
						t.Fatal(err)
					}
					wantStderr := ""
					if mode != 0o600 && mode != 0o700 {
						wantStderr = fmt.Sprintf("envmagic: warning: key file %s is readable by other users (mode %04o); run chmod 600 %s\n", path, mode, path)
					}
					r := run("get", "TOKEN")
					if r.code() != 0 || r.stdout != "secret\n" || r.stderr != wantStderr {
						t.Fatalf("exit=%d stdout=%q stderr=%q err=%v, want stderr=%q", r.code(), r.stdout, r.stderr, r.err, wantStderr)
					}
					dbPath, err := filepath.Abs(".envmagic")
					if err != nil {
						t.Fatal(err)
					}
					wantSetStderr := wantStderr + fmt.Sprintf("envmagic: stored TOKEN (namespace %q) in %s\n", "default", dbPath)
					r = run("--yes", "set", "TOKEN", "secret")
					if r.code() != 0 || r.stdout != "" || r.stderr != wantSetStderr {
						t.Fatalf("set: exit=%d stdout=%q stderr=%q err=%v, want stderr=%q", r.code(), r.stdout, r.stderr, r.err, wantSetStderr)
					}
				})
			}
		})
	}
}

func TestKeySetRejectsInvalidKey(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"base64", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)) + "!", "invalid base64:"},
		{"length", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 16)), "key must be 32 bytes (got 16 after decoding)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := setupBare(t)
			path, err := internal.KeyPath()
			if err != nil {
				t.Fatal(err)
			}
			key := bytes.Repeat([]byte{1}, 32)
			if err := internal.WriteKey(path, key); err != nil {
				t.Fatal(err)
			}
			r := run("key", "--set", tc.input)
			if r.code() != 1 || r.stdout != "" || r.err == nil || !strings.Contains(r.err.Error(), tc.want) || strings.Contains(r.stderr, "key restored") {
				t.Fatalf("exit=%d stdout=%q stderr=%q err=%v, want %q", r.code(), r.stdout, r.stderr, r.err, tc.want)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, key) {
				t.Fatalf("existing key changed: key=%x err=%v", got, err)
			}
		})
	}
}

func TestKeyWriteErrors(t *testing.T) {
	for _, command := range []string{"set-create", "set"} {
		for _, operation := range []string{"directory", "write"} {
			t.Run(command+"/"+operation, func(t *testing.T) {
				run := setupBare(t)
				path, err := internal.KeyPath()
				if err != nil {
					t.Fatal(err)
				}
				blocked := filepath.Dir(path)
				if err := os.MkdirAll(filepath.Dir(blocked), 0o700); err != nil {
					t.Fatal(err)
				}
				want := "failed to create key file directory"
				if operation == "write" {
					if err := os.Mkdir(blocked, 0o700); err != nil {
						t.Fatal(err)
					}
					blocked = path
					want = "failed to write key file"
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing", "key"), blocked); err != nil {
					t.Fatal(err)
				}
				args := []string{"key"}
				if command == "set" {
					args = append(args, "--set", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
				} else {
					args = []string{"--yes", "set", "TOKEN", "secret"}
				}
				r := run(args...)
				if r.code() != 1 || r.stdout != "" || r.err == nil || !strings.Contains(r.err.Error(), want) || strings.Contains(r.stderr, "generated new encryption key") || strings.Contains(r.stderr, "key restored") {
					t.Fatalf("exit=%d stdout=%q stderr=%q err=%v, want %q", r.code(), r.stdout, r.stderr, r.err, want)
				}
			})
		}
	}
}

func TestLooksSecret(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"API_KEY", true},
		{"CLIENT_SECRET", true},
		{"AUTH_TOKEN", true},
		{"DB_PASSWORD", true},
		{"DB_PORT", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksSecret(tc.name); got != tc.want {
				t.Fatalf("looksSecret(%q)=%t, want %t", tc.name, got, tc.want)
			}
		})
	}
}

func TestUnknownStoreOwnerAllowed(t *testing.T) {
	run := setup(t)
	originalOwner, originalUID := fileOwner, currentUID
	t.Cleanup(func() { fileOwner, currentUID = originalOwner, originalUID })
	currentUID = func() int { return 1234 }
	fileOwner = func(os.FileInfo) (int, bool) { return 0, false }
	if r := run("set", "TOKEN", "unknown"); r.code() != 0 || strings.Contains(r.stderr, "skipping") {
		t.Fatalf("set: %+v", r)
	}
	for _, read := range []struct {
		args []string
		want string
	}{
		{[]string{"get", "TOKEN"}, "unknown\n"},
		{[]string{"load", "TOKEN"}, "export TOKEN=\"unknown\"\n"},
	} {
		if r := run(read.args...); r.code() != 0 || r.stdout != read.want || r.stderr != "" {
			t.Fatalf("%v: %+v", read.args, r)
		}
	}
}

func TestRootSkipsForeignStore(t *testing.T) {
	run := setup(t)
	originalOwner, originalUID := fileOwner, currentUID
	t.Cleanup(func() { fileOwner, currentUID = originalOwner, originalUID })
	currentUID = func() int { return 0 }
	fileOwner = func(os.FileInfo) (int, bool) { return 1000, true }
	path, err := filepath.Abs(".envmagic")
	if err != nil {
		t.Fatal(err)
	}
	r := run("get", "TOKEN")
	warning := fmt.Sprintf("envmagic: skipping %s: owned by uid 1000, not by you (uid 0)\n", path)
	if r.code() != 1 || r.stdout != "" || r.stderr != warning || r.err == nil || !strings.Contains(r.err.Error(), "no .envmagic file found") {
		t.Fatalf("get as root: %+v", r)
	}
}

func TestStoreOwnerChangedWhileOpening(t *testing.T) {
	for _, args := range [][]string{{"get", "TOKEN"}, {"set", "TOKEN", "updated"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			run := setup(t)
			if r := run("set", "TOKEN", "original"); r.code() != 0 {
				t.Fatal(r.err)
			}
			originalOwner, originalUID := fileOwner, currentUID
			t.Cleanup(func() { fileOwner, currentUID = originalOwner, originalUID })
			currentUID = func() int { return 1000 }
			checks := 0
			fileOwner = func(os.FileInfo) (int, bool) {
				checks++
				if checks == 1 {
					return 1000, true
				}
				return 2000, true
			}
			r := run(args...)
			if r.code() != 1 || r.stdout != "" || r.err == nil || !strings.Contains(r.err.Error(), "changed while opening; refusing to use it") {
				t.Fatalf("owner change: %+v", r)
			}
			fileOwner, currentUID = originalOwner, originalUID
			if r := run("get", "TOKEN"); r.code() != 0 || r.stdout != "original\n" {
				t.Fatalf("store changed: %+v", r)
			}
		})
	}
}
