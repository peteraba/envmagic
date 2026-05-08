package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

// result holds the captured output from a single CLI invocation.
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

	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Pre-create the store so no invocation hits the interactive "create?" prompt.
	s, err := openStore(filepath.Join(dir, ".envmagic"))
	if err != nil {
		t.Fatalf("setup: create store: %v", err)
	}
	_ = s.Close()

	return func(args ...string) result {
		rOut, wOut, _ := os.Pipe()
		rErr, wErr, _ := os.Pipe()
		origOut, origErr := os.Stdout, os.Stderr
		os.Stdout, os.Stderr = wOut, wErr

		app := newApp()
		// Prevent urfave/cli's error handler from calling os.Exit during tests.
		app.ExitErrHandler = func(_ context.Context, _ *cli.Command, _ error) {}

		appErr := app.Run(context.Background(), append([]string{"envmagic"}, args...))

		wOut.Close()
		wErr.Close()
		os.Stdout, os.Stderr = origOut, origErr

		var bufOut, bufErr bytes.Buffer
		io.Copy(&bufOut, rOut)
		io.Copy(&bufErr, rErr)

		return result{bufOut.String(), bufErr.String(), appErr}
	}
}

// TestSetAndGet covers the implicit get/set syntax, name uppercasing, the
// --debug flag, overwrites, and the error case for a missing variable.
func TestSetAndGet(t *testing.T) {
	run := setup(t)

	// Set stores the value and prints a confirmation to stderr.
	r := run("api_key", "sk-test-abc")
	if r.code() != 0 {
		t.Fatalf("set: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	if !strings.Contains(r.stderr, "stored API_KEY") {
		t.Errorf("set: expected confirmation in stderr, got %q", r.stderr)
	}

	// Get emits an eval-ready export statement on stdout.
	r = run("api_key")
	if r.code() != 0 {
		t.Fatalf("get: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	want := `export API_KEY="sk-test-abc"`
	if !strings.Contains(r.stdout, want) {
		t.Errorf("get: stdout %q does not contain %q", r.stdout, want)
	}

	// --debug additionally echoes the export line to stderr.
	r = run("--debug", "api_key")
	if r.code() != 0 {
		t.Fatalf("get --debug: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	if !strings.Contains(r.stderr, "export API_KEY=") {
		t.Errorf("get --debug: expected export in stderr, got %q", r.stderr)
	}

	// Overwriting a key replaces the stored value.
	run("api_key", "sk-updated")
	r = run("api_key")
	if !strings.Contains(r.stdout, "sk-updated") {
		t.Errorf("overwrite: expected updated value, stdout=%q", r.stdout)
	}

	// Getting a variable that was never set is an error.
	r = run("no_such_var")
	if r.code() == 0 {
		t.Error("get missing: expected non-zero exit")
	}
}

// TestListAndRemove covers list, its ls alias, rm, and the remove/delete aliases.
func TestListAndRemove(t *testing.T) {
	run := setup(t)

	run("alpha", "1")
	run("beta", "2")
	run("gamma", "3")

	// list prints all stored names to stdout.
	r := run("list")
	if r.code() != 0 {
		t.Fatalf("list: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	for _, name := range []string{"ALPHA", "BETA", "GAMMA"} {
		if !strings.Contains(r.stdout, name) {
			t.Errorf("list: missing %s in output %q", name, r.stdout)
		}
	}

	// ls is an alias and must produce identical output.
	r2 := run("ls")
	if r2.stdout != r.stdout {
		t.Errorf("ls alias output differs from list\nlist=%q\nls=%q", r.stdout, r2.stdout)
	}

	// rm removes one entry; the others remain.
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

	// 'remove' is an alias for 'rm'.
	run("remove", "alpha")
	r = run("list")
	if strings.Contains(r.stdout, "ALPHA") {
		t.Error("remove alias: ALPHA still present")
	}

	// 'delete' is also an alias for 'rm'.
	run("delete", "gamma")
	r = run("list")
	if strings.Contains(r.stdout, "GAMMA") {
		t.Error("delete alias: GAMMA still present")
	}

	// Removing a non-existent entry is an error.
	r = run("rm", "nonexistent")
	if r.code() == 0 {
		t.Error("rm nonexistent: expected non-zero exit")
	}
}

// TestImportAndExport covers importing a .env file, verifying the stored
// values, and exporting them back to both stdout and a file.
func TestImportAndExport(t *testing.T) {
	run := setup(t)

	// The .env file exercises: plain value, double-quoted value, 'export' prefix,
	// comment lines, and blank lines.
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

	// Import should succeed and report the variable count.
	r := run("import", envFile)
	if r.code() != 0 {
		t.Fatalf("import: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	if !strings.Contains(r.stderr, "imported 3 variable(s)") {
		t.Errorf("import: unexpected confirmation %q", r.stderr)
	}

	// Each imported value must be retrievable with the correct content.
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

	// Export to stdout produces key=value lines for every stored variable.
	r = run("export")
	if r.code() != 0 {
		t.Fatalf("export stdout: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	for _, name := range []string{"DB_HOST", "API_SECRET", "PORT"} {
		if !strings.Contains(r.stdout, name+"=") {
			t.Errorf("export stdout: missing %s in %q", name, r.stdout)
		}
	}

	// Export to a file; read it back and verify the round-trip.
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

// TestNamespaces verifies that entries in different namespaces are fully
// isolated from each other.
func TestNamespaces(t *testing.T) {
	run := setup(t)

	// Same key name in two namespaces holds independent values.
	run("-n", "dev", "db_url", "postgres://dev-host/devdb")
	run("-n", "prod", "db_url", "postgres://prod-host/proddb")

	r := run("-n", "dev", "db_url")
	if !strings.Contains(r.stdout, "dev-host") {
		t.Errorf("dev get: expected dev-host, stdout=%q", r.stdout)
	}

	r = run("-n", "prod", "db_url")
	if !strings.Contains(r.stdout, "prod-host") {
		t.Errorf("prod get: expected prod-host, stdout=%q", r.stdout)
	}

	// Keys unique to one namespace must not appear in the other's list.
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

	// Removing a key from one namespace must not affect the other.
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

	fish := run("shell-init", "fish")
	if fish.code() != 0 {
		t.Fatalf("shell-init fish: exit %d", fish.code())
	}
	if !strings.Contains(fish.stdout, "function envmagic") {
		t.Errorf("fish init: expected 'function envmagic', got %q", fish.stdout)
	}
	if fish.stdout == bash.stdout {
		t.Error("fish init should differ from POSIX init")
	}

	// Unknown shell is an error.
	r := run("shell-init", "powershell")
	if r.code() == 0 {
		t.Error("unknown shell: expected non-zero exit")
	}

	// Missing shell argument is also an error.
	r = run("shell-init")
	if r.code() == 0 {
		t.Error("shell-init no args: expected non-zero exit")
	}
}

// TestSourceAll verifies that calling envmagic with no positional arguments
// exports all variables in the active namespace as shell-sourceable export lines.
func TestSourceAll(t *testing.T) {
	run := setup(t)

	// Populate default and staging namespaces.
	run("db_host", "localhost")
	run("port", "5432")
	run("-n", "staging", "db_host", "staging-host")
	run("-n", "staging", "api_key", "stg-secret")

	// No args → export default namespace.
	r := run()
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

	// -n staging → export staging namespace only.
	r = run("-n", "staging")
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

	// --debug echoes the export lines to stderr as well.
	r = run("--debug")
	if r.code() != 0 {
		t.Fatalf("source-all --debug: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	if !strings.Contains(r.stderr, "export DB_HOST=") {
		t.Errorf("source-all --debug: expected export in stderr, got %q", r.stderr)
	}

	// Empty namespace produces no output and exits 0.
	r = run("-n", "empty-ns")
	if r.code() != 0 {
		t.Fatalf("source-all empty namespace: exit %d\nstderr: %s", r.code(), r.stderr)
	}
	if strings.TrimSpace(r.stdout) != "" {
		t.Errorf("source-all empty namespace: expected no stdout, got %q", r.stdout)
	}
}

// TestInputValidation covers malformed variable names, wrong argument counts,
// and other usage errors that must produce a non-zero exit code.
func TestInputValidation(t *testing.T) {
	run := setup(t)

	// Variable names must match [A-Z_][A-Z0-9_]*.
	for _, badName := range []string{"123start", "has-hyphen", "has space", "has.dot"} {
		r := run(badName, "value")
		if r.code() == 0 {
			t.Errorf("invalid name %q: expected non-zero exit", badName)
		}
	}

	// More than two positional arguments is an error.
	r := run("valid_key", "value", "extra")
	if r.code() == 0 {
		t.Error("too many positional args: expected non-zero exit")
	}

	// rm requires exactly one name.
	for _, args := range [][]string{
		{"rm"},
		{"rm", "key1", "key2"},
	} {
		r = run(args...)
		if r.code() == 0 {
			t.Errorf("rm %v: expected non-zero exit", args[1:])
		}
	}

	// list rejects extra arguments.
	r = run("list", "unexpected")
	if r.code() == 0 {
		t.Error("list with args: expected non-zero exit")
	}

	// export rejects more than one file path.
	r = run("export", "file1.env", "file2.env")
	if r.code() == 0 {
		t.Error("export two paths: expected non-zero exit")
	}

	// import rejects more than one file path.
	r = run("import", "file1.env", "file2.env")
	if r.code() == 0 {
		t.Error("import two paths: expected non-zero exit")
	}
}
