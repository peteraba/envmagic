package main

import (
	"bytes"
	"encoding/base64"
	"errors"
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
	wantA := "$env:A = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('c2luZ2xlICfigJjigJnigJrigJsgZG91YmxlICIgZG9sbGFyICQgYmFja3RpY2sgYCBzbGFzaCBcCsOhcnbDrXog6ZuqCg=='))\n"
	fishA := "set -gx A 'single \\'‘’‚‛ double \" dollar $ backtick ` slash \\\\\nárvíz 雪\n'\n"
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--format", "pwsh", "load"}, wantA + "$env:B = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(''))\n"},
		{[]string{"--format", "pwsh", "load", "A"}, wantA},
		{[]string{"--format", "pwsh", "-n", "empty", "load"}, ""},
		{[]string{"--format", "pwsh", "get", "A"}, value + "\n"},
		{[]string{"--format", "pwsh", "list"}, "A\nB\n"},
		{[]string{"--format", "fish", "load"}, strings.TrimSuffix(fishA, "\n") + " &&\nset -gx B ''\n"},
		{[]string{"--format", "fish", "load", "A"}, fishA},
		{[]string{"--format", "fish", "-n", "empty", "load"}, ""},
		{[]string{"--format", "fish", "get", "A"}, value + "\n"},
		{[]string{"--format", "fish", "list"}, "A\nB\n"},
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
		for _, format := range []string{"pwsh", "fish"} {
			debug := run(append([]string{"--format", format, "--debug"}, args...)...)
			if debug.code() != 0 || debug.stderr != debug.stdout || debug.stdout != run(append([]string{"--format", format}, args...)...).stdout {
				t.Errorf("debug %s %v: %+v", format, args, debug)
			}
		}
	}
	for _, format := range []string{"invalid", "", "powershell"} {
		if r := run("--format", format, "load"); r.code() != 2 || r.stdout != "" || !strings.Contains(r.err.Error(), "expected posix, pwsh, or fish") {
			t.Errorf("invalid format %q: %+v", format, r)
		}
	}
	for _, tc := range []struct {
		format string
		value  string
		want   string
	}{
		{"pwsh", "hello", "$env:SAMPLE = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('aGVsbG8='))\n"},
		{"pwsh", "雪\r\n", "$env:SAMPLE = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('6ZuqDQo='))\n"},
		{"pwsh", "x\r", "$env:SAMPLE = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('eA0='))\n"},
		{"fish", "a`b$c\"d\\e", "set -gx SAMPLE 'a`b$c\"d\\\\e'\n"},
		{"fish", "'\\\\'trailing\\", "set -gx SAMPLE '\\'\\\\\\\\\\'trailing\\\\'\n"},
		{"fish", "雪\r\n", "set -gx SAMPLE '雪\r\n'\n"},
		{"fish", "a\xffb", "set -gx SAMPLE 'a\xffb'\n"},
	} {
		if r := run("set", "SAMPLE", tc.value); r.code() != 0 {
			t.Fatal(r.err)
		}
		if r := run("--format", tc.format, "load", "SAMPLE"); r.code() != 0 || r.stdout != tc.want || r.stderr != "" {
			t.Errorf("value=%q: result=%+v, want stdout=%q", tc.value, r, tc.want)
		}
	}
}

func TestLoadFormatInvalidUTF8(t *testing.T) {
	run := setup(t)
	value := "a\xffb"
	for _, args := range [][]string{{"set", "A_GOOD", "safe"}, {"set", "Z_BAD", value}} {
		if r := run(args...); r.code() != 0 {
			t.Fatal(r.err)
		}
	}
	for _, args := range [][]string{
		{"--format", "pwsh", "load"},
		{"--format", "pwsh", "load", "Z_BAD"},
		{"--format", "pwsh", "--debug", "load"},
		{"--format", "pwsh", "--debug", "load", "Z_BAD"},
	} {
		r := run(args...)
		if r.code() != 1 || r.stdout != "" || r.stderr != "" || r.err.Error() != "envmagic: value for Z_BAD is not valid UTF-8; it cannot be loaded into PowerShell" {
			t.Errorf("%v: %+v", args, r)
		}
	}
	for _, args := range [][]string{{"load"}, {"load", "Z_BAD"}} {
		r := run(args...)
		want := "export Z_BAD=\"" + value + "\"\n"
		if len(args) == 1 {
			want = "export A_GOOD=\"safe\" &&\n" + want
		}
		if r.code() != 0 || r.stdout != want || r.stderr != "" {
			t.Errorf("posix %v: %+v, want=%q", args, r, want)
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

func TestShellWrapperInvalidNameNoCall(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte(`package main
import "os"
func main() {
    _ = os.WriteFile(os.Getenv("ENVMAGIC_TEST_CALLED"), []byte("called"), 0600)
    os.Exit(99)
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "envmagic"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", filepath.Join(dir, name), source).CombinedOutput(); err != nil {
		t.Fatalf("build fake: %v\n%s", err, out)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("POWERSHELL_TELEMETRY_OPTOUT", "1")
	for _, shell := range []string{"bash", "zsh", "fish", "pwsh"} {
		t.Run(shell, func(t *testing.T) {
			path, err := exec.LookPath(shell)
			if err != nil {
				if os.Getenv("CI") != "" {
					t.Fatalf("%s is required in CI", shell)
				}
				t.Skip(shell + " is not on PATH")
			}
			init, exit := shellInitPosix, "; exit $?"
			options := []string{"-c"}
			switch shell {
			case "fish":
				init, exit = shellInitFish, "; exit $status"
			case "pwsh":
				init, exit = shellInitPwsh, "; exit $LASTEXITCODE"
				options = []string{"-NoProfile", "-NonInteractive", "-Command"}
			}
			check := func(t *testing.T, args, prefix string) {
				t.Helper()
				called := filepath.Join(t.TempDir(), "called")
				t.Setenv("ENVMAGIC_TEST_CALLED", called)
				cmd := exec.Command(path, append(options, init+prefix+"envmagic "+args+exit)...)
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				var exitErr *exec.ExitError
				arg := strings.Trim(strings.TrimPrefix(args, "load "), "'")
				wantErr := "envmagic: load accepts only -n/--namespace and a NAME (got " + arg + ")\n"
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 || len(out) != 0 || strings.ReplaceAll(stderr.String(), "\r\n", "\n") != wantErr {
					t.Errorf("%s: err=%v stdout=%q stderr=%q", args, err, out, stderr.String())
				}
				if _, err := os.Stat(called); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s: program called (marker stat: %v)", args, err)
				}
			}
			for _, args := range wrapperLoadArgs {
				if strings.HasPrefix(args, "load '") {
					t.Run(args, func(t *testing.T) { check(t, args, "") })
				}
			}
			if shell == "bash" {
				t.Run("UTF-8 locale", func(t *testing.T) {
					out, err := exec.Command("locale", "-a").Output()
					if err != nil {
						t.Skipf("cannot list locales: %v", err)
					}
					for _, locale := range []string{"en_US.UTF-8", "en_US.utf8", "C.UTF-8", "C.utf8"} {
						if strings.Contains("\n"+string(out), "\n"+locale+"\n") {
							t.Setenv("LC_ALL", locale)
							t.Logf("LC_ALL=%s, globasciiranges disabled", locale)
							check(t, "load 'é'", "shopt -u globasciiranges; ")
							return
						}
					}
					t.Skip("no en_US or C UTF-8 locale available")
				})
			}
		})
	}
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
		{"set", "VERSION", "Set-Item Env:PWNED 1"},
		{"set", "HELP", "Set-Item Env:PWNED 1"},
		{"set", "H", "h value"},
		{"set", "_X1", "underscore value"},
		{"-n", "x", "set", "PWNED", "Set-Item Env:PWNED 1"},
		{"-n", "-debug", "set", "NAME", "flag namespace"},
		{"set", "EMPTY", ""},
		{"set", "LOAD", "Write-Output LOAD_DATA"},
		{"-n", "--help", "set", "NAME", "help namespace"},
		{"-n", "X", "set", "X", "$(Set-Item Env:PWNED 1)"},
		{"-n", "cr", "set", "X", "CR\rCRLF\r\ntrailing\r"},
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
	t.Run("missing namespace", func(t *testing.T) {
		cmd := exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", init+`Remove-Item Env:NAME, Env:VERSION, Env:HELP, Env:H, Env:_X1, Env:EMPTY, Env:LOAD -ErrorAction SilentlyContinue; envmagic load -n; $code = $LASTEXITCODE; if (Get-Item Env:NAME, Env:VERSION, Env:HELP, Env:H, Env:_X1, Env:EMPTY, Env:LOAD -ErrorAction SilentlyContinue) { throw 'unexpected output applied' }; exit $code`)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || len(out) != 0 || strings.ReplaceAll(stderr.String(), "\r\n", "\n") != "Incorrect Usage: flag needs an argument: -n\n" {
			t.Errorf("err=%v stdout=%q stderr=%q", err, out, stderr.String())
		}
	})
	for _, args := range wrapperLoadArgs {
		t.Run("args "+args, func(t *testing.T) {
			commandArgs := args
			if args == "load --" {
				commandArgs = "load '--'"
			}
			cmd := exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", init+`Remove-Item Env:PWNED -ErrorAction SilentlyContinue; $env:VERSION = 'before'; $env:HELP = 'before'; envmagic `+commandArgs+`; $code = $LASTEXITCODE; if ($env:VERSION -cne 'before' -or $env:HELP -cne 'before' -or (Test-Path Env:PWNED)) { throw 'unexpected output applied' }; exit $code`)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			arg := strings.TrimPrefix(args, "load ")
			flag := strings.Fields(arg)[0]
			if strings.HasPrefix(arg, "'") {
				flag = strings.Trim(arg, "'")
			}
			wantCode := 2
			wantOut := ""
			wantErr := "envmagic: load accepts only -n/--namespace and a NAME (got " + flag + ")\n"
			if args == "--n x load" || args == "-namespace x load" {
				wantCode = 0
				wantOut = "export PWNED=\"Set-Item Env:PWNED 1\"\n"
				wantErr = ""
			}
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != wantCode || string(out) != wantOut || strings.ReplaceAll(stderr.String(), "\r\n", "\n") != wantErr {
				t.Errorf("err=%v stdout=%q stderr=%q wantErr=%q", err, out, stderr.String(), wantErr)
			}
			for _, name := range []string{"VERSION", "HELP"} {
				if r := run("get", name); r.code() != 0 || r.stdout != "Set-Item Env:PWNED 1\n" {
					t.Errorf("%s changed: %+v", name, r)
				}
			}
		})
	}
	seedReservedNamespaceY(t, run, loadAssignment("PWNED", "EXECUTED", "pwsh"))
	if r := run("-n", "x", "set", "Y", loadAssignment("PWNED", "EXECUTED", "pwsh")); r.code() != 0 {
		t.Fatal(r.err)
	}
	for _, flag := range []string{"--n", "-namespace"} {
		t.Run("reserved "+flag, func(t *testing.T) {
			cmd := exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", init+`Remove-Item Env:PWNED -ErrorAction SilentlyContinue; envmagic `+flag+` load get Y; $code = $LASTEXITCODE; if (Test-Path Env:PWNED) { throw 'stored value applied' }; exit $code`)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 2 || len(out) != 0 || strings.ReplaceAll(stderr.String(), "\r\n", "\n") != "envmagic: load accepts only -n/--namespace and a NAME (got "+flag+")\n" {
				t.Errorf("err=%v stdout=%q stderr=%q", err, out, stderr.String())
			}
		})
	}
	for _, args := range []string{"--n load -n x get Y", "-namespace load --namespace=x get Y"} {
		t.Run("repeated "+args, func(t *testing.T) {
			cmd := exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", init+`Remove-Item Env:PWNED -ErrorAction SilentlyContinue; envmagic `+args+`; $code = $LASTEXITCODE; if (Test-Path Env:PWNED) { throw 'stored value applied' }; exit $code`)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			var exit *exec.ExitError
			flag := strings.Fields(args)[0]
			if !errors.As(err, &exit) || exit.ExitCode() != 2 || len(out) != 0 || strings.ReplaceAll(stderr.String(), "\r\n", "\n") != "envmagic: load accepts only -n/--namespace and a NAME (got "+flag+")\n" {
				t.Errorf("err=%v stdout=%q stderr=%q", err, out, stderr.String())
			}
		})
	}
	confirm := "envmagic: environment variables set\n"
	for _, tc := range []struct {
		command string
		want    string
		wantErr string
	}{
		{`$env:EMPTY = ''; $emptyPresent = Test-Path Env:EMPTY; $env:EMPTY = 'before'; envmagic load EMPTY; if ((Test-Path Env:EMPTY) -ne $emptyPresent -or $env:EMPTY) { throw 'empty value differs from native assignment' }`, "", ""},
		{`envmagic -n cr load; [Console]::Out.Write($env:X)`, "CR\rCRLF\r\ntrailing\r", confirm},
		{`envmagic -n cr load X; [Console]::Out.Write($env:X)`, "CR\rCRLF\r\ntrailing\r", ""},
		{`$env:NAME = 'before'; envmagic -n --help load NAME; if ($env:NAME -cne 'before') { throw 'namespace help evaluated' }`, "export NAME=\"help namespace\"\n", ""},
		{`envmagic LOAD`, "Write-Output LOAD_DATA\n", ""},
		{`envmagic load; [Console]::Out.Write($env:NAME)`, value, confirm},
		{`envmagic $null load; [Console]::Out.Write($env:NAME)`, value, confirm},
		{`envmagic @($null) load; [Console]::Out.Write($env:NAME)`, value, confirm},
		{`envmagic load NAME; [Console]::Out.Write($env:NAME)`, value, ""},
		{`envmagic load name; [Console]::Out.Write($env:NAME)`, value, ""},
		{`envmagic load _X1; [Console]::Out.Write($env:_X1)`, "underscore value", ""},
		{`envmagic load help; [Console]::Out.Write($env:HELP)`, "Set-Item Env:PWNED 1", ""},
		{`envmagic load h; [Console]::Out.Write($env:H)`, "h value", ""},
		{`envmagic -n -debug load NAME; [Console]::Out.Write($env:NAME)`, "flag namespace", ""},
		{`envmagic --namespace -debug load NAME; [Console]::Out.Write($env:NAME)`, "flag namespace", ""},
		{`Remove-Item Env:PWNED -ErrorAction SilentlyContinue; envmagic -n security load; if (Test-Path Env:PWNED) { throw 'stored value executed' }; [Console]::Out.Write($env:X)`, quoteInjection, confirm},
		{`Remove-Item Env:PWNED -ErrorAction SilentlyContinue; envmagic -n security load X; if (Test-Path Env:PWNED) { throw 'stored value executed' }; [Console]::Out.Write($env:X)`, quoteInjection, ""},
		{`envmagic -n multiline load; [Console]::Out.Write($env:NAME)`, multiline, confirm},
		{`envmagic -n multiline load NAME; [Console]::Out.Write($env:NAME)`, multiline, ""},
		{`envmagic -n staging load; [Console]::Out.Write($env:NAME + '/' + $env:OTHER)`, "staging value/second value", confirm},
		{`envmagic --namespace staging load; [Console]::Out.Write($env:NAME)`, "staging value", confirm},
		{`envmagic --namespace=staging load; [Console]::Out.Write($env:NAME)`, "staging value", confirm},
		{`envmagic -n=staging load; [Console]::Out.Write($env:NAME)`, "staging value", confirm},
		{`envmagic load -n staging; [Console]::Out.Write($env:NAME)`, "staging value", confirm},
		{`envmagic load NAME -n staging; [Console]::Out.Write($env:NAME)`, "staging value", ""},
		{`envmagic -n staging load NAME; [Console]::Out.Write($env:NAME)`, "staging value", ""},
		{`envmagic -n empty load; [Console]::Out.Write($LASTEXITCODE)`, "0", ""},
		{`envmagic get NAME`, value + "\n", ""},
		{`envmagic --debug get NAME`, value + "\n", ""},
		{`envmagic get MISSING; [Console]::Out.Write($LASTEXITCODE)`, "1", "envmagic: MISSING not found in namespace \"default\"\n"},
		{`$env:NAME = 'before'; envmagic load MISSING; [Console]::Out.Write("$LASTEXITCODE/$env:NAME")`, "1/before", "envmagic: MISSING not found in namespace \"default\"\n"},
		{`envmagic load A B; [Console]::Out.Write($LASTEXITCODE)`, "2", "usage: envmagic load [-n NS] [NAME]\n"},
		{`envmagic --help`, string(help), ""},
		{`envmagic load --help`, string(loadHelp), ""},
		{`envmagic --debug load --help`, string(loadHelp), ""},
		{`envmagic load -h`, string(loadHelp), ""},
		{`envmagic --version load`, "envmagic version v0.6.0\n", ""},
		{`envmagic -v load`, "envmagic version v0.6.0\n", ""},
		{`envmagic load --version; [Console]::Out.Write($LASTEXITCODE)`, "1", "Incorrect Usage: flag provided but not defined: -version\n"},
		{`envmagic load -v; [Console]::Out.Write($LASTEXITCODE)`, "1", "Incorrect Usage: flag provided but not defined: -v\n"},
		{`envmagic --version; [Console]::Out.Write($LASTEXITCODE)`, "envmagic version v0.6.0\n0", ""},
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

	for _, tc := range []struct {
		command string
		code    string
		wantErr string
	}{
		{`envmagic -n X load --format posix`, "0", ""},
		{`envmagic -n X load --format=posix`, "0", ""},
		{`envmagic -n X load -format posix`, "0", ""},
		{`envmagic -n X load -format=posix`, "0", ""},
		{`envmagic -n X load --% --format posix`, "0", ""},
		{`envmagic -n X load '--%' '--format posix'`, "0", ""},
		{`envmagic -n X load @('--format','posix')`, "0", ""},
		{`$f = '--format','posix'; envmagic -n X load $f`, "0", ""},
		{`envmagic -n X load (,@('--format','posix'))`, "2", "envmagic: load accepts only -n/--namespace and a NAME (got --format posix)\n"},
		{`envmagic -n X @('load',@('--format','posix'))`, "2", "envmagic: load accepts only -n/--namespace and a NAME (got --format posix)\n"},
		{`$f = [System.Collections.Generic.List[object]]::new(); $f.Add(@('--format','posix')); envmagic -n X load $f`, "2", "envmagic: load accepts only -n/--namespace and a NAME (got --format posix)\n"},
	} {
		t.Run("format "+tc.command, func(t *testing.T) {
			cmd := exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", init+`Remove-Item Env:X, Env:PWNED -ErrorAction SilentlyContinue; `+tc.command+"\n"+`if ((Test-Path Env:X) -or (Test-Path Env:PWNED)) { throw 'explicit format evaluated' }; [Console]::Out.Write($LASTEXITCODE)`)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			want := tc.code
			if tc.code == "0" {
				want = "export X=\"\\$(Set-Item Env:PWNED 1)\"\n" + want
			}
			if err != nil || string(out) != want || strings.ReplaceAll(stderr.String(), "\r\n", "\n") != tc.wantErr {
				t.Errorf("err=%v stdout=%q want=%q stderr=%q wantErr=%q", err, out, want, stderr.String(), tc.wantErr)
			}
		})
	}

	for _, marker := range []string{"\u2011", "т", "сf", "\U0001086f", "\u0092"} {
		if r := run("-n", "security", "set", "X", "a"+marker+"; $env:PWNED=1; #"+marker+"b"); r.code() != 0 {
			t.Fatal(r.err)
		}
		for _, codepage := range []string{"1252", "932", "936", "949"} {
			t.Run("encoding "+codepage+" "+marker, func(t *testing.T) {
				cmd := exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", init+`[Text.Encoding]::RegisterProvider([Text.CodePagesEncodingProvider]::Instance); [Console]::OutputEncoding = [Text.Encoding]::GetEncoding(`+codepage+`); Remove-Item Env:PWNED -ErrorAction SilentlyContinue; envmagic -n security load; if (Test-Path Env:PWNED) { throw 'stored value executed' }; [Console]::Out.Write([Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($env:X)))`)
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				decoded, decodeErr := base64.StdEncoding.DecodeString(string(out))
				want := "a" + marker + "; $env:PWNED=1; #" + marker + "b"
				if err != nil || decodeErr != nil || string(decoded) != want || strings.ReplaceAll(stderr.String(), "\r\n", "\n") != confirm {
					t.Errorf("err=%v decodeErr=%v stdout=%q decoded=%q want=%q stderr=%q", err, decodeErr, out, decoded, want, stderr.String())
				}
			})
		}
	}

	t.Run("eval guards", func(t *testing.T) {
		fakeDir := t.TempDir()
		source := filepath.Join(fakeDir, "main.go")
		if err := os.WriteFile(source, []byte(`package main
import ("fmt"; "os")
func main() {
    if os.Getenv("ENVMAGIC_TEST_FAIL") == "1" {
        fmt.Println("$env:NAME = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('ZXZhbHVhdGVk'))")
        fmt.Fprintln(os.Stderr, "load failed")
        os.Exit(1)
    }
    switch os.Getenv("ENVMAGIC_TEST_OUTPUT") {
    case "mixed":
        fmt.Println("$env:NAME = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('ZXZhbHVhdGVk'))")
        fmt.Println("unexpected output")
    case "posix":
        fmt.Println("export X=\"$(Set-Item Env:PWNED 1)\"")
    case "bad base64":
        fmt.Println("$env:NAME = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('ZXZhbHVhdGVk'))")
        fmt.Println("$env:X = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('a'))")
    case "leading character":
        fmt.Println("x$env:X = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('YQ=='))")
    case "trailing character":
        fmt.Println("$env:X = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('YQ=='))x")
    case "uppercase ENV":
        fmt.Println("$ENV:X = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('YQ=='))")
    case "lowercase name":
        fmt.Println("$env:x = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('YQ=='))")
    case "invalid name":
        fmt.Println("$env:X-Y = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('YQ=='))")
    default:
        fmt.Println("throw 'eval failed'")
    }
}
`), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("go", "build", "-o", filepath.Join(fakeDir, filepath.Base(binary)), source).CombinedOutput(); err != nil {
			t.Fatalf("build fake: %v\n%s", err, out)
		}
		t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		for _, tc := range []struct {
			name   string
			fail   string
			prefix string
			output string
		}{
			{"binary failure", "1", "", ""},
			{"invalid", "0", "", ""},
			{"invalid with stop", "0", `$ErrorActionPreference='Stop';`, ""},
			{"mixed", "0", "", "mixed"},
			{"posix", "0", "", "posix"},
			{"bad base64", "0", "", "bad base64"},
			{"leading character", "0", "", "leading character"},
			{"trailing character", "0", "", "trailing character"},
			{"uppercase ENV", "0", "", "uppercase ENV"},
			{"lowercase name", "0", "", "lowercase name"},
			{"invalid name", "0", "", "invalid name"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv("ENVMAGIC_TEST_FAIL", tc.fail)
				t.Setenv("ENVMAGIC_TEST_OUTPUT", tc.output)
				cmd := exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", tc.prefix+shellInitPwsh+`Remove-Item Env:PWNED -ErrorAction SilentlyContinue; $env:X = 'before'; $env:NAME = 'before'; envmagic load; if ($env:X -cne 'before' -or (Test-Path Env:PWNED)) { throw 'unexpected output applied' }; [Console]::Out.Write("$LASTEXITCODE/$env:NAME")`)
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				wantErr := "envmagic: unexpected load output; nothing was set\n"
				if tc.fail == "1" {
					wantErr = "load failed\n"
				}
				if err != nil || string(out) != "1/before" || strings.ReplaceAll(stderr.String(), "\r\n", "\n") != wantErr {
					t.Errorf("err=%v stdout=%q stderr=%q wantErr=%q", err, out, stderr.String(), wantErr)
				}
			})
		}
	})
}
