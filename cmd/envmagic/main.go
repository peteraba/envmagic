package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/peteraba/envmagic"
	"github.com/peteraba/envmagic/internal"
)

// version is overridden at link time for releases.
var version = "v0.5.0"

func main() {
	if err := newApp().Run(context.Background(), os.Args); err != nil {
		os.Exit(1)
	}
}

func newApp() *cli.Command {
	return &cli.Command{
		Name:    "envmagic",
		Usage:   "encrypted env-var store, scoped to your project directory",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "namespace",
				Aliases: []string{"n"},
				Value:   envmagic.DefaultNamespace,
				Usage:   "a namespace",
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
		},
		Action: cmdDefault,
		Commands: []*cli.Command{
			{
				Name:      "get",
				Usage:     "print a decrypted value (usually optional)",
				ArgsUsage: "NAME",
				Action:    cmdDefault,
			},
			{
				Name:      "set",
				Usage:     "store a value (usually optional)",
				ArgsUsage: "NAME VALUE",
				Action:    cmdDefault,
			},
			{
				Name:      "load",
				Usage:     "print export statements for one or all values",
				ArgsUsage: "[NAME]",
				Action:    cmdDefault,
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
				Usage:     "print shell integration for bash, zsh, or fish",
				ArgsUsage: "<bash|zsh|fish>",
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

	key, err := loadKey()
	if err != nil {
		return errorf("load key: %v", err)
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
		if cmd.NArg() != 2 {
			return cli.Exit("usage: envmagic set [-n NS] NAME VALUE", 2)
		}
	}

	ns := cmd.String("namespace")
	debug := cmd.Bool("debug")

	if cmd.NArg() == 0 {
		if cmd.Name == "load" {
			return runSourceAll(ns, debug)
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
		return runGet(cmd, ns, name)
	case 2:
		return runSet(cmd, ns, name, cmd.Args().Get(1))
	default:
		return cli.Exit("envmagic: too many positional arguments; expected NAME [VALUE]", 2)
	}
}

func cmdList(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() > 0 {
		return cli.Exit(fmt.Sprintf("envmagic list: unexpected arguments: %v", cmd.Args().Slice()), 2)
	}

	s, _, err := openActiveStore()
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

	s, _, err := openActiveStore()
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

func runSet(cmd *cli.Command, namespace, name, value string) error {
	dbPath, err := storeAll(cmd, namespace, [][2]string{{name, value}}, false)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "envmagic: stored %s (namespace %q) in %s\n", name, namespace, dbPath)

	return nil
}

func runGet(cmd *cli.Command, namespace, name string) error {
	s, key, err := openActiveStore()
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
		if err := checkValue(name, line); err != nil {
			return err
		}
		line = fmt.Sprintf("export %s=%s", name, shellQuote(line))
		if cmd.Bool("debug") {
			fmt.Fprintln(os.Stderr, line)
		}
	}
	fmt.Println(line)

	return nil
}

func runSourceAll(namespace string, debug bool) error {
	s, key, err := openActiveStore()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	entries, err := s.GetAll(namespace)
	if err != nil {
		return errorf("read: %v", err)
	}

	var output strings.Builder
	for _, e := range entries {
		plain, err := internal.Decrypt(key, e.Enc, internal.AD(namespace, e.Name))
		if err != nil {
			return errorf("decrypt %s: %v (wrong key or stored by an older envmagic; re-import it (see README))", e.Name, err)
		}
		if err := checkValue(e.Name, string(plain)); err != nil {
			return err
		}
		fmt.Fprintf(&output, "export %s=%s\n", e.Name, shellQuote(string(plain)))
	}
	fmt.Print(output.String())
	if debug {
		fmt.Fprint(os.Stderr, output.String())
	}

	return nil
}

func checkValue(name, value string) error {
	if strings.ContainsRune(value, 0) {
		return errorf("value for %s contains a NUL byte", name)
	}
	return nil
}

func openActiveStore() (*internal.Store, []byte, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, errorf("getcwd: %v", err)
	}
	dbPath, found := findEnvmagic(cwd)
	if !found {
		return nil, nil, errorf("no .envmagic file found in %s or any parent", cwd)
	}

	key, err := loadKey()
	if err != nil {
		return nil, nil, errorf("load key: %v", err)
	}

	s, err := internal.OpenStore(dbPath)
	if err != nil {
		return nil, nil, errorf("open store: %v", err)
	}

	return s, key, nil
}

// findOrCreateStorePath returns the path to the nearest .envmagic file,
// prompting to create one in the current directory if none is found.
// With --yes or ENVMAGIC_NONINTERACTIVE=1, creates without prompting.
func findOrCreateStorePath(cmd *cli.Command) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", errorf("getcwd: %v", err)
	}
	dbPath, found := findEnvmagic(cwd)
	if found {
		return dbPath, nil
	}

	target := filepath.Join(cwd, ".envmagic")
	if cmd.Root().Bool("yes") {
		return target, nil
	}

	ok, err := promptYesNo(fmt.Sprintf("No .envmagic file found. Create %s? [y/N]: ", target))
	if err != nil {
		return "", errorf("read prompt: %v", err)
	}
	if !ok {
		return "", cli.Exit("envmagic: aborted", 1)
	}

	return target, nil
}

func findEnvmagic(start string) (string, bool) {
	dir := start
	for {
		candidate := filepath.Join(dir, ".envmagic")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// shellQuote returns a shell-escaped version of s, suitable for use in export statements.
func shellQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\', '$', '`':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

func promptYesNo(prompt string) (bool, error) {
	for range 3 {
		fmt.Fprint(os.Stderr, prompt)

		r := bufio.NewReader(os.Stdin)

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

// loadKey loads or creates the user's encryption key; when a new key file is
// created, backup instructions are printed to stderr.
func loadKey() ([]byte, error) {
	path, err := internal.KeyPath()
	if err != nil {
		return nil, err
	}
	key, created, err := internal.LoadOrCreateKey()
	if err != nil {
		return nil, err
	}
	if created {
		notifyNewEncryptionKey(key, path)
	}
	return key, nil
}

func notifyNewEncryptionKey(key []byte, path string) {
	fmt.Fprintf(os.Stderr, "envmagic: generated new encryption key at %s\n", path)
	fmt.Fprintf(os.Stderr, "envmagic: key (base64): %s\n", base64.StdEncoding.EncodeToString(key))
	fmt.Fprintf(os.Stderr, "envmagic: You can display the key again later by running `envmagic key`.\n")
	fmt.Fprintln(os.Stderr, "envmagic: BACK THIS FILE UP - without it, stored values cannot be decrypted.")
}

func errorf(format string, a ...any) error {
	return cli.Exit(fmt.Sprintf("envmagic: "+format, a...), 1)
}
