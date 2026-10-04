package main

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/urfave/cli/v3"
)

func TestExportDevFull(t *testing.T) {
	run := setup(t)
	if r := run("set", "TOKEN", "secret"); r.code() != 0 {
		t.Fatal(r.err)
	}
	if r := run("export", "/dev/full"); r.code() != 1 || r.err == nil || !strings.Contains(r.err.Error(), "envmagic: write /dev/full:") || r.stderr != "" {
		t.Errorf("export /dev/full: exit=%d stderr=%q err=%v", r.code(), r.stderr, r.err)
	}
	info, err := os.Stat("/dev/full")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		t.Errorf("/dev/full mode=%v, want character device", info.Mode())
	}
	full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = full.Close() }()
	original := os.Stdout
	os.Stdout = full
	defer func() { os.Stdout = original }()
	app := newApp()
	app.ExitErrHandler = func(_ context.Context, _ *cli.Command, _ error) {}
	err = runApp(app, []string{"envmagic", "export"})
	if (result{err: err}).code() != 1 || err == nil || !strings.Contains(err.Error(), "envmagic: write stdout:") {
		t.Errorf("export stdout: err=%v", err)
	}
}

func TestExportWriteFailure(t *testing.T) {
	if path := os.Getenv("ENVMAGIC_TEST_WRITE_FAILURE"); path != "" {
		signal.Ignore(syscall.SIGXFSZ)
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 0, Max: 0}); err != nil {
			t.Fatal(err)
		}
		err := writeExportFile(path, "TOKEN=\"secret\"\n")
		if (result{err: err}).code() != 1 || err == nil || !strings.Contains(err.Error(), "envmagic: write ") {
			t.Errorf("export write failure: err=%v", err)
		}
		checkNoExportTemps(t, filepath.Dir(path))
		return
	}
	path := filepath.Join(t.TempDir(), "output.env")
	if err := os.WriteFile(path, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Limit writes in a child process so the rest of the test suite is unaffected.
	cmd := exec.Command(os.Args[0], "-test.run=^TestExportWriteFailure$")
	cmd.Env = append(os.Environ(), "ENVMAGIC_TEST_WRITE_FAILURE="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write failure test: %v\n%s", err, output)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "unchanged" {
		t.Errorf("failed write changed target: content=%q err=%v", content, err)
	}
	checkNoExportTemps(t, filepath.Dir(path))
}
