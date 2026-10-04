package main

import (
	"bytes"
	"encoding/base64"
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
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--format", "pwsh", "load"}, wantA + "$env:B = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(''))\n"},
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
	for _, tc := range []struct {
		value string
		want  string
	}{
		{"hello", "$env:SAMPLE = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('aGVsbG8='))\n"},
		{"雪\r\n", "$env:SAMPLE = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('6ZuqDQo='))\n"},
		{"x\r", "$env:SAMPLE = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('eA0='))\n"},
	} {
		if r := run("set", "SAMPLE", tc.value); r.code() != 0 {
			t.Fatal(r.err)
		}
		if r := run("--format", "pwsh", "load", "SAMPLE"); r.code() != 0 || r.stdout != tc.want || r.stderr != "" {
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
		{"set", "EMPTY", ""},
		{"set", "LOAD", "Write-Output LOAD_DATA"},
		{"-n", "--help", "set", "X", "help namespace"},
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
	confirm := "envmagic: environment variables set\n"
	for _, tc := range []struct {
		command string
		want    string
		wantErr string
	}{
		{`$env:EMPTY = ''; $emptyPresent = Test-Path Env:EMPTY; $env:EMPTY = 'before'; envmagic load EMPTY; if ((Test-Path Env:EMPTY) -ne $emptyPresent -or $env:EMPTY) { throw 'empty value differs from native assignment' }`, "", ""},
		{`envmagic -n cr load; [Console]::Out.Write($env:X)`, "CR\rCRLF\r\ntrailing\r", confirm},
		{`envmagic -n cr load X; [Console]::Out.Write($env:X)`, "CR\rCRLF\r\ntrailing\r", ""},
		{`Remove-Item Env:X -ErrorAction SilentlyContinue; envmagic -n --help load; if (Test-Path Env:X) { throw 'namespace help evaluated' }`, "export X=\"help namespace\"\n", ""},
		{`envmagic LOAD`, "Write-Output LOAD_DATA\n", ""},
		{`envmagic load; [Console]::Out.Write($env:NAME)`, value, confirm},
		{`envmagic load NAME; [Console]::Out.Write($env:NAME)`, value, ""},
		{`envmagic load '--' NAME; [Console]::Out.Write($env:NAME)`, value, ""},
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

	for _, command := range []string{
		`envmagic -n X load --format posix`,
		`envmagic -n X load --format=posix`,
		`envmagic -n X load -format posix`,
		`envmagic -n X load -format=posix`,
		`envmagic -n X load @('--format','posix')`,
		`$f = '--format','posix'; envmagic -n X load $f`,
	} {
		t.Run("format "+command, func(t *testing.T) {
			cmd := exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", init+`Remove-Item Env:X, Env:PWNED -ErrorAction SilentlyContinue; `+command+`; if ($LASTEXITCODE -ne 0 -or (Test-Path Env:X) -or (Test-Path Env:PWNED)) { throw 'explicit format evaluated' }`)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			want := "export X=\"\\$(Set-Item Env:PWNED 1)\"\n"
			if err != nil || string(out) != want || stderr.Len() != 0 {
				t.Errorf("err=%v stdout=%q want=%q stderr=%q", err, out, want, stderr.String())
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
		for _, tc := range []struct {
			fail   string
			prefix string
		}{
			{"1", ""},
			{"0", ""},
			{"0", `$ErrorActionPreference='Stop';`},
		} {
			t.Setenv("ENVMAGIC_TEST_FAIL", tc.fail)
			cmd := exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", tc.prefix+shellInitPwsh+`$env:NAME = 'before'; envmagic load; [Console]::Out.Write("$LASTEXITCODE/$env:NAME")`)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			wantErr := "eval failed"
			if tc.fail == "1" {
				wantErr = "load failed"
			}
			if err != nil || string(out) != "1/before" || !strings.Contains(stderr.String(), wantErr) || strings.Contains(stderr.String(), confirm) {
				t.Errorf("fail=%s prefix=%q: err=%v stdout=%q stderr=%q", tc.fail, tc.prefix, err, out, stderr.String())
			}
		}
	})
}
