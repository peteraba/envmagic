package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/peteraba/envmagic/internal"
)

type result struct {
	stdout string
	stderr string
	err    error
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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	return func(args ...string) result {
		rOut, wOut, _ := os.Pipe()
		rErr, wErr, _ := os.Pipe()
		origOut, origErr := os.Stdout, os.Stderr
		os.Stdout, os.Stderr = wOut, wErr

		app := newApp()
		// Prevent urfave/cli's error handler from calling os.Exit during tests.
		app.ExitErrHandler = func(_ context.Context, _ *cli.Command, _ error) {}

		appErr := app.Run(context.Background(), append([]string{"envmagic"}, args...))

		_ = wOut.Close()
		_ = wErr.Close()
		os.Stdout, os.Stderr = origOut, origErr

		var bufOut, bufErr bytes.Buffer
		_, _ = io.Copy(&bufOut, rOut)
		_, _ = io.Copy(&bufErr, rErr)

		return result{bufOut.String(), bufErr.String(), appErr}
	}
}

// TestSetAndGet covers explicit and implicit syntax, raw values, and load exports.
func TestSetAndGet(t *testing.T) {
	run := setup(t)

	r := run("api_key", "sk-test-abc")
	if r.code() != 0 {
		t.Fatalf("set: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	if !strings.Contains(r.stderr, "stored API_KEY") {
		t.Errorf("set: expected confirmation in stderr, got %q", r.stderr)
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
	if r.code() != 0 || r.stdout != "" || !strings.Contains(r.stderr, "stored API_KEY") {
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
			for _, path := range []string{".envmagic", filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "envmagic", "key")} {
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

// TestImportAndExport covers importing a .env file, verifying the stored
// values, and exporting them back to both stdout and a file.
func TestImportAndExport(t *testing.T) {
	run := setup(t)

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
	if !strings.Contains(r.stderr, "imported 3 variable(s)") {
		t.Errorf("import: unexpected confirmation %q", r.stderr)
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
	for _, name := range []string{"DB_HOST", "API_SECRET", "PORT"} {
		if !strings.Contains(r.stdout, name+"=") {
			t.Errorf("export stdout: missing %s in %q", name, r.stdout)
		}
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
	for _, name := range []string{"DB_HOST", "API_SECRET", "PORT"} {
		if !strings.Contains(string(exported), name+"=") {
			t.Errorf("exported file: missing %s", name)
		}
	}
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
				t.Skip(shell + " is not on PATH")
			}
			run := setup(t)
			home := t.TempDir()
			value := `raw "quotes" $cash`
			for _, args := range [][]string{
				{"set", "name", value},
				{"-n", "staging", "set", "name", "staging value"},
				{"-n", "staging", "set", "other", "second value"},
				{"-n", "ro", "set", "PWD", "readonly value"},
			} {
				if r := run(args...); r.code() != 0 {
					t.Fatalf("seed %v: %v", args, r.err)
				}
			}
			init := `eval "$(envmagic shell-init ` + shell + `)"` + "\n"
			status := "$?"
			evalFailure := `readonly NAME; envmagic load; echo "rc=$?"`
			evalError := "readonly variable"
			if shell == "zsh" {
				evalError = "read-only variable"
			}
			if shell == "fish" {
				init = "envmagic shell-init fish | source\n"
				status = "$status"
				evalFailure = `envmagic -n ro load; echo "rc=$status"`
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
				{evalFailure, "rc=1\n", evalError},
				{`envmagic load A B; echo "rc=` + status + `"`, "rc=2\n", "usage: envmagic load [-n NS] [NAME]\n"},
				{`envmagic -n empty load`, "", ""},
				{`envmagic`, string(help), ""},
				{`envmagic -n staging`, string(help), ""},
				{`envmagic >/dev/null; printf %s "$NAME"`, "", ""},
				{`envmagic -n staging >/dev/null; printf %s "$NAME"`, "", ""},
				{`envmagic load NAME; printf %s "$NAME"`, value, ""},
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
				}
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				if err != nil || string(out) != tc.want {
					t.Errorf("%s: err=%v stdout=%q want=%q stderr=%q", tc.command, err, out, tc.want, stderr.String())
				}
				if tc.command == evalFailure {
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
	for _, want := range []string{`export DB_HOST="localhost"`, `export PORT="5432"`} {
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
	for _, want := range []string{`export DB_HOST="staging-host"`, `export API_KEY="stg-secret"`} {
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
	want := "export DB_HOST=\"localhost\"\nexport PORT=\"5432\"\n"
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
	for _, args := range [][]string{nil, {"-n", "staging"}, {"--debug"}} {
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

	for _, badName := range []string{"123start", "has-hyphen", "has space", "has.dot"} {
		for _, args := range [][]string{{badName, "value"}, {"get", badName}, {"load", badName}, {"set", badName, "value"}} {
			r := run(args...)
			if r.code() != 2 {
				t.Errorf("invalid name %v: expected exit 2, got %d", args, r.code())
			}
		}
	}
	for _, args := range [][]string{
		{"get"},
		{"get", "name", "extra"},
		{"load", "A", "B"},
		{"set"},
		{"set", "name"},
		{"set", "name", "value", "extra"},
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
	if r.code() == 0 {
		t.Error("list with args: expected non-zero exit")
	}

	r = run("export", "file1.env", "file2.env")
	if r.code() == 0 {
		t.Error("export two paths: expected non-zero exit")
	}

	r = run("import", "file1.env", "file2.env")
	if r.code() == 0 {
		t.Error("import two paths: expected non-zero exit")
	}
}
