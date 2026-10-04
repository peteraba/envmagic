package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadFormat(t *testing.T) {
	run := setup(t)
	value := "single '‘’‚‛ double \" dollar $ backtick ` slash \\\nárvíz 雪\n"
	for _, args := range [][]string{{"set", "A", value}, {"set", "B", ""}} {
		if r := run(args...); r.code() != 0 {
			t.Fatal(r.err)
		}
	}
	wantA := "$env:A = 'single ''‘‘’’‚‚‛‛ double \" dollar $ backtick ` slash \\\nárvíz 雪\n'\n"
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--format", "pwsh", "load"}, wantA + "$env:B = ''\n"},
		{[]string{"--format", "pwsh", "load", "A"}, wantA},
		{[]string{"--format", "pwsh", "-n", "empty", "load"}, ""},
		{[]string{"--format", "pwsh", "get", "A"}, value + "\n"},
		{[]string{"--format", "pwsh", "list"}, "A\nB\n"},
		{[]string{"--format", "posix", "load"}, "export A=\"single '‘’‚‛ double \\\" dollar \\$ backtick \\` slash \\\\\nárvíz 雪\n\" &&\nexport B=\"\"\n"},
	} {
		if r := run(tc.args...); r.code() != 0 || r.stdout != tc.want || r.stderr != "" {
			t.Errorf("%v: result=%+v, want stdout=%q", tc.args, r, tc.want)
		}
	}
	for _, args := range [][]string{{"load"}, {"load", "A"}} {
		plain := run(args...)
		explicit := run(append([]string{"--format", "posix"}, args...)...)
		if plain.code() != 0 || explicit.code() != 0 || plain.stdout != explicit.stdout {
			t.Errorf("posix %v: default=%+v explicit=%+v", args, plain, explicit)
		}
		debug := run(append([]string{"--format", "pwsh", "--debug"}, args...)...)
		if debug.code() != 0 || debug.stderr != debug.stdout || !strings.HasPrefix(debug.stdout, wantA) {
			t.Errorf("debug %v: %+v", args, debug)
		}
	}
	for _, format := range []string{"invalid", "", "powershell"} {
		if r := run("--format", format, "load"); r.code() != 2 || r.stdout != "" || !strings.Contains(r.err.Error(), "expected posix or pwsh") {
			t.Errorf("invalid format %q: %+v", format, r)
		}
	}
}

func buildShellBinary(t *testing.T) string {
	t.Helper()
	name := "envmagic"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)
	if out, err := exec.Command("go", "build", "-o", binary, "./").CombinedOutput(); err != nil {
		t.Fatalf("build envmagic: %v\n%s", err, out)
	}
	return binary
}

func TestShellWrapperPwsh(t *testing.T) {
	path, err := exec.LookPath("pwsh")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("pwsh is required in CI")
		}
		t.Skip("pwsh is not on PATH")
	}
	binary := buildShellBinary(t)
	help, err := exec.Command(binary, "--help").Output()
	if err != nil {
		t.Fatal(err)
	}
	loadHelp, err := exec.Command(binary, "load", "--help").Output()
	if err != nil {
		t.Fatal(err)
	}
	run := setup(t)
	t.Setenv("PATH", filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("POWERSHELL_TELEMETRY_OPTOUT", "1")
	value := "raw 'quotes' \"double\" $cash `tick \\ 雪"
	quoteInjection := "a’; $env:PWNED='yes'; ’b‘c‚d‛e'f"
	multiline := "-----BEGIN KEY-----\n  abc\ndef\n-----END KEY-----\n"
	for _, args := range [][]string{
		{"set", "NAME", value},
		{"-n", "staging", "set", "NAME", "staging value"},
		{"-n", "staging", "set", "OTHER", "second value"},
		{"-n", "multiline", "set", "--", "NAME", multiline},
		{"-n", "security", "set", "X", quoteInjection},
	} {
		if r := run(args...); r.code() != 0 {
			t.Fatal(r.err)
		}
	}
	init := "envmagic shell-init pwsh | Out-String | Invoke-Expression\n"
	confirm := "envmagic: environment variables set\n"
	for _, tc := range []struct {
		command string
		want    string
		wantErr string
	}{
		{`envmagic load; [Console]::Out.Write($env:NAME)`, value, confirm},
		{`envmagic load NAME; [Console]::Out.Write($env:NAME)`, value, ""},
		{`Remove-Item Env:PWNED -ErrorAction SilentlyContinue; envmagic -n security load; if (Test-Path Env:PWNED) { throw 'stored value executed' }; [Console]::Out.Write($env:X)`, quoteInjection, confirm},
		{`Remove-Item Env:PWNED -ErrorAction SilentlyContinue; envmagic -n security load X; if (Test-Path Env:PWNED) { throw 'stored value executed' }; [Console]::Out.Write($env:X)`, quoteInjection, ""},
		{`envmagic -n multiline load; [Console]::Out.Write($env:NAME)`, multiline, confirm},
		{`envmagic -n multiline load NAME; [Console]::Out.Write($env:NAME)`, multiline, ""},
		{`envmagic -n staging load; [Console]::Out.Write($env:NAME + '/' + $env:OTHER)`, "staging value/second value", confirm},
		{`envmagic --namespace staging load; [Console]::Out.Write($env:NAME)`, "staging value", confirm},
		{`envmagic --namespace=staging load; [Console]::Out.Write($env:NAME)`, "staging value", confirm},
		{`envmagic load -n staging; [Console]::Out.Write($env:NAME)`, "staging value", confirm},
		{`envmagic -n staging load NAME; [Console]::Out.Write($env:NAME)`, "staging value", ""},
		{`envmagic -n empty load; [Console]::Out.Write($LASTEXITCODE)`, "0", ""},
		{`envmagic get NAME`, value + "\n", ""},
		{`envmagic get MISSING; [Console]::Out.Write($LASTEXITCODE)`, "1", "envmagic: MISSING not found in namespace \"default\"\n"},
		{`$env:NAME = 'before'; envmagic load MISSING; [Console]::Out.Write("$LASTEXITCODE/$env:NAME")`, "1/before", "envmagic: MISSING not found in namespace \"default\"\n"},
		{`envmagic load A B; [Console]::Out.Write($LASTEXITCODE)`, "2", "usage: envmagic load [-n NS] [NAME]\n"},
		{`envmagic --help`, string(help), ""},
		{`envmagic load --help`, string(loadHelp), ""},
		{`envmagic load -h`, string(loadHelp), ""},
		{`envmagic --version load`, "envmagic version v0.5.0\n", ""},
		{`envmagic -v load`, "envmagic version v0.5.0\n", ""},
		{`envmagic load --version; [Console]::Out.Write($LASTEXITCODE)`, "1", "Incorrect Usage: flag provided but not defined: -version\n"},
		{`envmagic load -v; [Console]::Out.Write($LASTEXITCODE)`, "1", "Incorrect Usage: flag provided but not defined: -v\n"},
		{`envmagic --version; [Console]::Out.Write($LASTEXITCODE)`, "envmagic version v0.5.0\n0", ""},
	} {
		t.Run(tc.command, func(t *testing.T) {
			cmd := exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", init+tc.command)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil || string(out) != tc.want || strings.ReplaceAll(stderr.String(), "\r\n", "\n") != tc.wantErr {
				t.Errorf("err=%v stdout=%q want=%q stderr=%q wantErr=%q", err, out, tc.want, stderr.String(), tc.wantErr)
			}
		})
	}

	t.Run("eval guards", func(t *testing.T) {
		fakeDir := t.TempDir()
		source := filepath.Join(fakeDir, "main.go")
		if err := os.WriteFile(source, []byte(`package main
import ("fmt"; "os")
func main() {
    if os.Getenv("ENVMAGIC_TEST_FAIL") == "1" {
        fmt.Println("$env:NAME = 'evaluated'")
        fmt.Fprintln(os.Stderr, "load failed")
        os.Exit(1)
    }
    fmt.Println("throw 'eval failed'")
}
`), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("go", "build", "-o", filepath.Join(fakeDir, filepath.Base(binary)), source).CombinedOutput(); err != nil {
			t.Fatalf("build fake: %v\n%s", err, out)
		}
		t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		for _, fail := range []string{"1", "0"} {
			t.Setenv("ENVMAGIC_TEST_FAIL", fail)
			cmd := exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", shellInitPwsh+`$env:NAME = 'before'; envmagic load; [Console]::Out.Write("$LASTEXITCODE/$env:NAME")`)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			wantErr := "eval failed"
			if fail == "1" {
				wantErr = "load failed"
			}
			if err != nil || string(out) != "1/before" || !strings.Contains(stderr.String(), wantErr) || strings.Contains(stderr.String(), confirm) {
				t.Errorf("fail=%s: err=%v stdout=%q stderr=%q", fail, err, out, stderr.String())
			}
		}
	})
}
