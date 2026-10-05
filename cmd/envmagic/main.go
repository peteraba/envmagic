package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"

	"github.com/peteraba/envmagic"
	"github.com/peteraba/envmagic/internal"
)

// version is overridden at link time for releases.
var version = "v0.6.0"

var stdinIsTerminal = func() bool { return isatty.IsTerminal(os.Stdin.Fd()) }

var stderrIsTerminal = func() bool { return isatty.IsTerminal(os.Stderr.Fd()) }

var fileOwner = internal.OwnerUID

var currentUID = os.Getuid

func main() {
	if err := runApp(newApp(), os.Args); err != nil {
		os.Exit(1)
	}
}

func runApp(app *cli.Command, args []string) error {
	var diagnostics strings.Builder
	// ponytail: any ErrWriter output counts as already reported; write app warnings to os.Stderr, since a flag Deprecated warning would hide the error.
	app.ErrWriter = io.MultiWriter(os.Stderr, &diagnostics)
	err := app.Run(context.Background(), args)
	if _, ok := err.(cli.ExitCoder); err != nil && !ok && diagnostics.Len() == 0 {
		fmt.Fprintf(os.Stderr, "envmagic: %v\n", err)
	}
	return err
}

func newApp() *cli.Command {
	app := &cli.Command{
		Name:    "envmagic",
		Usage:   "encrypted env-var store, scoped to your project directory",
		Version: version,
		OnUsageError: func(_ context.Context, cmd *cli.Command, err error, _ bool) error {
			_, _ = fmt.Fprintf(cmd.ErrWriter, "Incorrect Usage: %s\n", err)
			return err
		},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "format",
				Value: "posix",
				Usage: "load output format: posix (default), pwsh, or fish",
				Action: func(_ context.Context, _ *cli.Command, format string) error {
					if format != "posix" && format != "pwsh" && format != "fish" {
						return cli.Exit(fmt.Sprintf("envmagic: invalid format %q (expected posix, pwsh, or fish)", format), 2)
					}
					return nil
				},
			},
			&cli.StringFlag{
				Name:     "namespace",
				Aliases:  []string{"n"},
				Value:    envmagic.DefaultNamespace,
				Usage:    "a namespace",
				OnlyOnce: true,
				Action: func(_ context.Context, _ *cli.Command, namespace string) error {
					if namespace == "load" {
						return cli.Exit(`envmagic: namespace "load" is reserved`, 2)
					}
					return nil
				},
			},
			&cli.BoolFlag{
				Name:    "debug",
				Aliases: []string{"d"},
				Usage:   "echo export to stderr (load only)",
			},
			&cli.BoolFlag{
				Name:    "yes",
				Aliases: []string{"y"},
				Usage:   "create a new .envmagic in the current directory if none exists (no prompt); may use ENVMAGIC_NONINTERACTIVE=1 instead",
				Sources: cli.EnvVars("ENVMAGIC_NONINTERACTIVE"),
			},
			&cli.BoolFlag{
				Name:  "here",
				Usage: "with set and import, use or create .envmagic in the current directory instead of a parent's",
			},
		},
		Action: cmdDefault,
		Commands: []*cli.Command{
			{
				Name:            "get",
				Usage:           "print a decrypted value (usually optional)",
				ArgsUsage:       "NAME",
				HideHelpCommand: true,
				Action:          cmdDefault,
			},
			{
				Name:            "set",
				Usage:           "store a value (usually optional); without VALUE, read piped stdin",
				ArgsUsage:       "NAME [VALUE]",
				HideHelpCommand: true,
				Action:          cmdDefault,
			},
			{
				Name:            "load",
				Usage:           "print export statements for one or all values",
				ArgsUsage:       "[NAME]",
				HideHelpCommand: true,
				Action:          cmdDefault,
			},
			{
				Name:    "list",
				Aliases: []string{"ls"},
				Usage:   "list names stored in a namespace",
				Action:  cmdList,
			},
			{
				Name:      "rm",
				Aliases:   []string{"remove", "delete"},
				Usage:     "remove a stored entry",
				ArgsUsage: "NAME",
				Action:    cmdRemove,
			},
			{
				Name:      "export",
				Usage:     "export a namespace to a .env file (stdout if omitted)",
				ArgsUsage: "[FILE]",
				Action:    cmdExport,
			},
			{
				Name:      "import",
				Usage:     "import a .env file into a namespace (stdin if omitted)",
				ArgsUsage: "[FILE]",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:    "interactive",
						Aliases: []string{"i"},
						Usage:   "prompt for each value in a form (file values pre-filled as defaults)",
					},
					&cli.BoolFlag{
						Name:  "empty",
						Usage: "store an empty value for every variable",
					},
				},
				Action: cmdImport,
			},
			{
				Name:      "shell-init",
				Usage:     "print shell integration for bash, zsh, fish, or pwsh",
				ArgsUsage: "<bash|zsh|fish|pwsh>",
				Action:    cmdShellInit,
			},
			{
				Name:  "key",
				Usage: "show the encryption key path and content; use --set to restore",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "set",
						Usage: "restore the key from a `base64` string",
					},
				},
				Action: cmdKey,
			},
		},
	}
	_ = app.Walk(func(cmd *cli.Command) error {
		cmd.OnUsageError = app.OnUsageError
		return nil
	})
	return app
}

func cmdKey(_ context.Context, cmd *cli.Command) error {
	if b64 := cmd.String("set"); b64 != "" {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return errorf("invalid base64: %v", err)
		}
		if len(data) != 32 {
			return errorf("key must be 32 bytes (got %d after decoding)", len(data))
		}

		path, err := internal.KeyPath()
		if err != nil {
			return errorf("key path: %v", err)
		}
		if err := internal.WriteKey(path, data); err != nil {
			return errorf("%v", err)
		}

		fmt.Fprintf(os.Stderr, "envmagic: key restored to %s\n", path)

		return nil
	}

	path, err := internal.KeyPath()
	if err != nil {
		return errorf("key path: %v", err)
	}

	key, err := loadKey(false)
	if err != nil {
		return err
	}

	fmt.Printf("path:    %s\n", path)
	fmt.Printf("content: %s\n", base64.StdEncoding.EncodeToString(key))

	return nil
}

// cmdDefault handles get/set/load and the implicit `envmagic [-n NS] [-d] [NAME [VALUE]]` syntax.
// With no positional arguments it shows help; load exports the entire namespace.
func cmdDefault(_ context.Context, cmd *cli.Command) error {
	switch cmd.Name {
	case "get":
		if cmd.NArg() != 1 {
			return cli.Exit(fmt.Sprintf("usage: envmagic %s [-n NS] NAME", cmd.Name), 2)
		}
	case "load":
		if cmd.NArg() > 1 {
			return cli.Exit("usage: envmagic load [-n NS] [NAME]", 2)
		}
	case "set":
		if err := checkSetArgs(cmd.NArg(), stdinIsTerminal()); err != nil {
			return err
		}
	}

	ns := cmd.String("namespace")
	debug := cmd.Bool("debug")

	if cmd.NArg() == 0 {
		if cmd.Name == "load" {
			return runSourceAll(ns, debug, cmd.String("format"))
		}
		return cli.ShowRootCommandHelp(cmd)
	}

	rawName := cmd.Args().First()
	name := strings.ToUpper(rawName)
	if !internal.ValidName(name) {
		return cli.Exit(fmt.Sprintf("envmagic: invalid env var name %q (must match [A-Z_][A-Z0-9_]*)", rawName), 2)
	}

	switch cmd.NArg() {
	case 1:
		if cmd.Name == "set" {
			return runSetFromStdin(cmd, ns, name)
		}
		return runGet(cmd, ns, name)
	case 2:
		return runSet(cmd, ns, name, cmd.Args().Get(1))
	default:
		return cli.Exit("envmagic: too many positional arguments; expected NAME [VALUE]", 2)
	}
}

func checkSetArgs(argCount int, stdinTerminal bool) error {
	if argCount < 1 || argCount > 2 || (argCount == 1 && stdinTerminal) {
		return cli.Exit("usage: envmagic set [-n NS] NAME [VALUE]; without VALUE, pipe the value on stdin (e.g. printf '%s' \"$SECRET\" | envmagic set NAME)", 2)
	}
	return nil
}

func cmdList(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() > 0 {
		return cli.Exit(fmt.Sprintf("envmagic list: unexpected arguments: %v", cmd.Args().Slice()), 2)
	}

	s, _, _, err := openActiveStore(false)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	ns := cmd.String("namespace")
	entries, err := s.GetAll(ns)
	if err != nil {
		return errorf("list: %v", err)
	}
	if len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "envmagic: no entries in namespace %q\n", ns)
		return nil
	}

	for _, e := range entries {
		fmt.Println(e.Name)
	}

	return nil
}

func cmdRemove(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() != 1 {
		return cli.Exit("usage: envmagic rm [-n NS] NAME", 2)
	}
	rawName := cmd.Args().First()
	name := strings.ToUpper(rawName)
	if !internal.ValidName(name) {
		return errorf("invalid env var name %q", rawName)
	}

	s, _, _, err := openActiveStore(false)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	ns := cmd.String("namespace")
	n, err := s.Delete(ns, name)
	if err != nil {
		return errorf("delete: %v", err)
	}
	if n == 0 {
		return errorf("%s not found in namespace %q", name, ns)
	}

	fmt.Fprintf(os.Stderr, "envmagic: removed %s from namespace %q\n", name, ns)

	return nil
}

func runSetFromStdin(cmd *cli.Command, namespace, name string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return errorf("getcwd: %v", err)
	}
	if _, checked := findEnvmagic(cwd, false, cmd.Root().Bool("here")); checked == nil && !cmd.Root().Bool("yes") {
		_, _ = findEnvmagic(cwd, true, cmd.Root().Bool("here"))
		if err := refuseSkippedStore(filepath.Join(cwd, ".envmagic")); err != nil {
			return err
		}
		return errorf("no .envmagic file found; reading a value from stdin requires --yes or ENVMAGIC_NONINTERACTIVE=1 to create a store")
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20+1))
	if err != nil {
		return errorf("read stdin: %v", err)
	}
	if len(data) > 1<<20 {
		return cli.Exit("value on stdin is larger than 1 MiB", 2)
	}
	value := string(data)
	if strings.HasSuffix(value, "\n") {
		value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	}
	if value == "" {
		return cli.Exit("no value on stdin; to store an empty value use: envmagic set NAME ''", 2)
	}
	return runSet(cmd, namespace, name, value)
}

func runSet(cmd *cli.Command, namespace, name, value string) error {
	dbPath, err := storeAll(cmd, namespace, [][2]string{{name, value}}, false)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "envmagic: stored %s (namespace %q) in %s\n", name, namespace, dbPath)

	return nil
}

func runGet(cmd *cli.Command, namespace, name string) error {
	s, key, _, err := openActiveStore(true)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	enc, err := s.Get(namespace, name)
	if err != nil {
		if errors.Is(err, internal.ErrEntryNotFound) {
			return errorf("%s not found in namespace %q", name, namespace)
		}
		return errorf("read: %v", err)
	}

	plain, err := internal.Decrypt(key, enc, internal.AD(namespace, name))
	if err != nil {
		return errorf("decrypt: %v (wrong key or stored by an older envmagic; re-import it (see README))", err)
	}

	line := string(plain)
	if cmd.Name == "load" {
		if err := checkValue(name, line, cmd.String("format")); err != nil {
			return err
		}
		line = loadAssignment(name, line, cmd.String("format"))
		if cmd.Bool("debug") {
			fmt.Fprintln(os.Stderr, line)
		}
	}
	fmt.Println(line)

	return nil
}

func runSourceAll(namespace string, debug bool, format string) error {
	s, key, _, err := openActiveStore(true)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	entries, err := s.GetAll(namespace)
	if err != nil {
		return errorf("read: %v", err)
	}

	var output strings.Builder
	for i, e := range entries {
		plain, err := internal.Decrypt(key, e.Enc, internal.AD(namespace, e.Name))
		if err != nil {
			return errorf("decrypt %s: %v (wrong key or stored by an older envmagic; re-import it (see README))", e.Name, err)
		}
		if err := checkValue(e.Name, string(plain), format); err != nil {
			return err
		}
		ending := "\n"
		if format != "pwsh" && i < len(entries)-1 {
			ending = " &&\n"
		}
		fmt.Fprint(&output, loadAssignment(e.Name, string(plain), format), ending)
	}
	fmt.Print(output.String())
	if debug {
		fmt.Fprint(os.Stderr, output.String())
	}

	return nil
}

func loadAssignment(name, value, format string) string {
	if format == "pwsh" {
		return fmt.Sprintf("$env:%s = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%s'))", name, base64.StdEncoding.EncodeToString([]byte(value)))
	}
	if format == "fish" {
		return fmt.Sprintf("set -gx %s '%s'", name, strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value))
	}
	return fmt.Sprintf("export %s=%s", name, shellQuote(value))
}

func checkValue(name, value, format string) error {
	if strings.ContainsRune(value, 0) {
		return errorf("value for %s contains a NUL byte", name)
	}
	if format == "pwsh" && !utf8.ValidString(value) {
		return errorf("value for %s is not valid UTF-8; it cannot be loaded into PowerShell", name)
	}
	return nil
}

func openActiveStore(decrypt bool) (*internal.Store, []byte, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, "", errorf("getcwd: %v", err)
	}
	dbPath, checked := findEnvmagic(cwd, true, false)
	if checked == nil {
		return nil, nil, "", errorf("no .envmagic file found in %s or any parent", cwd)
	}

	var key []byte
	if decrypt {
		key, err = loadKey(false)
		if err != nil {
			return nil, nil, "", err
		}
	}

	s, err := openCheckedStore(dbPath, checked)
	if err != nil {
		return nil, nil, "", errorf("open store: %v", err)
	}

	return s, key, dbPath, nil
}

// findOrCreateStorePath returns the path to the nearest .envmagic file,
// prompting on a terminal to create one in the current directory if none is found
// (without a terminal it fails with a --yes hint).
// With --yes or ENVMAGIC_NONINTERACTIVE=1, creates without prompting.
func findOrCreateStorePath(cmd *cli.Command) (string, os.FileInfo, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", nil, errorf("getcwd: %v", err)
	}
	dbPath, checked := findEnvmagic(cwd, true, cmd.Root().Bool("here"))
	if checked != nil {
		if dbPath != filepath.Join(cwd, ".envmagic") {
			fmt.Fprintf(os.Stderr, "envmagic: using %s\n", dbPath)
		}
		return dbPath, checked, nil
	}

	target := filepath.Join(cwd, ".envmagic")
	if err := refuseSkippedStore(target); err != nil {
		return "", nil, err
	}
	if cmd.Root().Bool("yes") {
		return target, nil, nil
	}
	if !stdinIsTerminal() {
		return "", nil, errorf("no .envmagic in %s or any parent; rerun with --yes (or ENVMAGIC_NONINTERACTIVE=1) to create one", cwd)
	}
	ok, err := promptYesNo(fmt.Sprintf("No .envmagic file found. Create %s? [y/N]: ", target))
	if err != nil {
		return "", nil, errorf("read prompt: %v", err)
	}
	if !ok {
		return "", nil, cli.Exit("envmagic: aborted", 1)
	}

	return target, nil, nil
}

func refuseSkippedStore(target string) error {
	if _, err := os.Lstat(target); err == nil {
		return errorf("refusing to overwrite skipped store %s", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return errorf("stat %s: %v", target, err)
	}
	return nil
}

func findEnvmagic(start string, warn, here bool) (string, os.FileInfo) {
	dir := start
	for {
		candidate := filepath.Join(dir, ".envmagic")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			if uid, known := fileOwner(info); known && uid != currentUID() {
				if warn {
					fmt.Fprintf(os.Stderr, "envmagic: skipping %s: owned by uid %d, not by you (uid %d)\n", candidate, uid, currentUID())
				}
			} else {
				return candidate, info
			}
		}
		parent := filepath.Dir(dir)
		if here || parent == dir {
			return "", nil
		}
		dir = parent
	}
}

func openCheckedStore(path string, checked os.FileInfo) (*internal.Store, error) {
	s, err := internal.OpenStore(path)
	if err != nil {
		return nil, err
	}
	if checked != nil {
		// ponytail: SQLite opens by path, so a swap after this re-check is still possible; a full fix needs opening by descriptor.
		now, err := os.Stat(path)
		if err != nil || !os.SameFile(checked, now) {
			_ = s.Close()
			return nil, fmt.Errorf("store %s changed while opening; refusing to use it", path)
		}
		if uid, known := fileOwner(now); known && uid != currentUID() {
			_ = s.Close()
			return nil, fmt.Errorf("store %s changed while opening; refusing to use it", path)
		}
	}
	return s, nil
}

var shellEscaper = strings.NewReplacer(`"`, `\"`, `\`, `\\`, `$`, `\$`, "`", "\\`")

// shellQuote returns a shell-escaped version of s, suitable for use in export statements.
func shellQuote(s string) string {
	return `"` + shellEscaper.Replace(s) + `"`
}

func promptYesNo(prompt string) (bool, error) {
	r := bufio.NewReader(os.Stdin)
	for range 3 {
		fmt.Fprint(os.Stderr, prompt)

		line, err := r.ReadString('\n')
		if err != nil && !errors.Is(err, os.ErrClosed) && line == "" {
			return false, err
		}
		line = strings.ToLower(strings.TrimSpace(line))

		switch line {
		case "n", "no", "":
			return false, nil
		case "y", "yes":
			return true, nil
		}
	}

	return false, errorf("prompt failed after 3 attempts")
}

// loadKey loads the user's encryption key, optionally creating it. New keys
// get backup instructions on stderr. It also warns on stderr
// when the key file permissions allow access by other users.
func loadKey(create bool) ([]byte, error) {
	path, err := internal.KeyPath()
	if err != nil {
		return nil, errorf("load key: %v", err)
	}
	var key []byte
	var created bool
	if create {
		key, created, err = internal.LoadOrCreateKey()
	} else {
		key, err = internal.LoadKey(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, errorf("no key at %s; restore it with envmagic key --set, or run envmagic set to create one", path)
		}
	}
	if err != nil {
		return nil, errorf("load key: %v", err)
	}
	if created {
		notifyNewEncryptionKey(key, path)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 {
			fmt.Fprintf(os.Stderr, "envmagic: warning: key file %s is readable by other users (mode %04o); run chmod 600 %s\n", path, info.Mode().Perm(), path)
		}
	}
	return key, nil
}

func notifyNewEncryptionKey(key []byte, path string) {
	fmt.Fprintf(os.Stderr, "envmagic: generated new encryption key at %s\n", path)
	if stderrIsTerminal() {
		fmt.Fprintf(os.Stderr, "envmagic: key (base64): %s\n", base64.StdEncoding.EncodeToString(key))
		fmt.Fprintf(os.Stderr, "envmagic: You can display the key again later by running `envmagic key`.\n")
	} else {
		fmt.Fprintln(os.Stderr, "envmagic: run `envmagic key` on a terminal to see the key")
	}
	fmt.Fprintln(os.Stderr, "envmagic: BACK THIS FILE UP - without it, stored values cannot be decrypted.")
}

func errorf(format string, a ...any) error {
	return cli.Exit(fmt.Sprintf("envmagic: "+format, a...), 1)
}
