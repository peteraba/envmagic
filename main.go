package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"
)

const version = "0.2.2"

func main() {
	if err := newApp().Run(context.Background(), os.Args); err != nil {
		os.Exit(exitCode(err))
	}
}

// newApp constructs the CLI application with its commands and flags.
func newApp() *cli.Command {
	return &cli.Command{
		Name:    "envmagic",
		Usage:   "encrypted env-var store, scoped to your project directory",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "namespace",
				Aliases: []string{"n"},
				Value:   "default",
				Usage:   "namespace",
			},
			&cli.BoolFlag{
				Name:    "debug",
				Aliases: []string{"d"},
				Usage:   "echo export to stderr (get only)",
			},
		},
		Action: cmdDefault,
		Commands: []*cli.Command{
			{
				Name:    "list",
				Aliases: []string{"ls"},
				Usage:   "list names stored in namespace",
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
				Usage:     "export namespace to a .env file (stdout if omitted)",
				ArgsUsage: "[FILE]",
				Action:    cmdExport,
			},
			{
				Name:      "import",
				Usage:     "import a .env file into namespace (stdin if omitted)",
				ArgsUsage: "[FILE]",
				Action:    cmdImport,
			},
			{
				Name:      "shell-init",
				Usage:     "print shell integration for bash, zsh, or fish",
				ArgsUsage: "<bash|zsh|fish>",
				Action:    cmdShellInit,
			},
		},
	}
}

func exitCode(err error) int {
	if ec, ok := err.(cli.ExitCoder); ok {
		return ec.ExitCode()
	}
	return 1
}

// cmdDefault handles the implicit `envmagic [-n NS] [-d] [NAME [VALUE]]` syntax.
// With no positional arguments it sources (exports) the entire namespace.
func cmdDefault(_ context.Context, cmd *cli.Command) error {
	ns := cmd.String("namespace")
	debug := cmd.Bool("debug")

	if cmd.NArg() == 0 {
		return runSourceAll(ns, debug)
	}

	rawName := cmd.Args().First()
	name := strings.ToUpper(rawName)
	if !validVarName(name) {
		return cli.Exit(fmt.Sprintf("envmagic: invalid env var name %q (must match [A-Z_][A-Z0-9_]*)", rawName), 2)
	}

	switch cmd.NArg() {
	case 1:
		return runGet(ns, name, debug)
	case 2:
		return runSet(ns, name, cmd.Args().Get(1))
	default:
		return cli.Exit("envmagic: too many positional arguments; expected NAME [VALUE]", 2)
	}
}

// cmdList handles `envmagic list [-n NS]`, listing all variable names in the namespace.
func cmdList(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() > 0 {
		return cli.Exit(fmt.Sprintf("envmagic list: unexpected arguments: %v", cmd.Args().Slice()), 2)
	}

	// Retrieve the store from the nearest .envmagic file, erroring if none is found.
	s, err := openActiveStore()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	// Retrieve the list of all variable names in the namespace.
	ns := cmd.String("namespace")
	names, err := s.List(ns)
	if err != nil {
		return errorf("list: %v", err)
	}
	if len(names) == 0 {
		fmt.Fprintf(os.Stderr, "envmagic: no entries in namespace %q\n", ns)
		return nil
	}

	// Print each name on a separate line.
	for _, n := range names {
		fmt.Println(n)
	}

	return nil
}

// cmdRemove handles `envmagic rm [-n NS] NAME`, removing the given name from the namespace.
func cmdRemove(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() != 1 {
		return cli.Exit("usage: envmagic rm [-n NS] NAME", 2)
	}
	rawName := cmd.Args().First()
	name := strings.ToUpper(rawName)
	if !validVarName(name) {
		return errorf("invalid env var name %q", rawName)
	}

	// Retrieve the store from the nearest .envmagic file, erroring if none is found.
	s, err := openActiveStore()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	// Delete the entry from the store.
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

// runSet stores the given name=value pair in the active store under the given namespace.
func runSet(namespace, name, value string) error {
	// Find or create the nearest .envmagic file, prompting to create one in the current directory if none is found.
	dbPath, err := findOrCreateStorePath()
	if err != nil {
		return err
	}

	// Load or create the encryption key.
	key, err := loadOrCreateKey()
	if err != nil {
		return errorf("load key: %v", err)
	}

	// Encrypt the value before storing it.
	enc, err := encrypt(key, []byte(value))
	if err != nil {
		return errorf("encrypt: %v", err)
	}

	// Open the store, creating it if it doesn't exist.
	s, err := openStore(dbPath)
	if err != nil {
		return errorf("open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	// Store the encrypted value in the store under the given namespace and name.
	if err := s.Set(namespace, name, enc); err != nil {
		return errorf("write: %v", err)
	}

	fmt.Fprintf(os.Stderr, "envmagic: stored %s (namespace %q) in %s\n", name, namespace, dbPath)

	return nil
}

// runGet retrieves the given name from the active store and prints an export statement for it.
func runGet(namespace, name string, debug bool) error {
	// Retrieve the store from the nearest .envmagic file, erroring if none is found.
	s, err := openActiveStore()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	// Load or create the encryption key. We have to do this before checking if the entry exists.
	key, err := loadOrCreateKey()
	if err != nil {
		return errorf("load key: %v", err)
	}

	// Retrieve the encrypted value from the store.
	enc, err := s.Get(namespace, name)
	if err != nil {
		return errorf("read: %v", err)
	}
	if enc == nil {
		return errorf("%s not found in namespace %q", name, namespace)
	}

	// Decrypt the value.
	plain, err := decrypt(key, enc)
	if err != nil {
		return errorf("decrypt: %v (wrong key, or value was encrypted with a different key)", err)
	}

	// Print an export statement for the variable.
	line := fmt.Sprintf("export %s=%s", name, shellQuote(string(plain)))
	fmt.Println(line)
	if debug {
		fmt.Fprintln(os.Stderr, line)
	}

	return nil
}

// runSourceAll retrieves all entries in the namespace and prints export statements for each.
func runSourceAll(namespace string, debug bool) error {
	// Retrieve the store from the nearest .envmagic file, erroring if none is found.
	s, err := openActiveStore()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	// Load or create the encryption key. We have to do this before checking if there are any entries,
	key, err := loadOrCreateKey()
	if err != nil {
		return errorf("load key: %v", err)
	}

	// Retrieve all entries in the namespace.
	entries, err := s.GetAll(namespace)
	if err != nil {
		return errorf("read: %v", err)
	}

	// Decrypt each entry and print an export statement for it.
	for _, e := range entries {
		plain, err := decrypt(key, e.Enc)
		if err != nil {
			return errorf("decrypt %s: %v (wrong key?)", e.Name, err)
		}

		line := fmt.Sprintf("export %s=%s", e.Name, shellQuote(string(plain)))

		fmt.Println(line)

		if debug {
			fmt.Fprintln(os.Stderr, line)
		}
	}

	return nil
}

// openActiveStore opens the store from the nearest .envmagic file,
// erroring if none is found.
func openActiveStore() (*store, error) {
	// Find the nearest .envmagic file, erroring if none is found.
	cwd, err := os.Getwd()
	if err != nil {
		return nil, errorf("getcwd: %v", err)
	}
	dbPath, found := findEnvmagic(cwd)
	if !found {
		return nil, errorf("no .envmagic file found in %s or any parent", cwd)
	}

	// Open the store.
	s, err := openStore(dbPath)
	if err != nil {
		return nil, errorf("open store: %v", err)
	}

	return s, nil
}

// findOrCreateStorePath returns the path to the nearest .envmagic file,
// prompting to create one in the current directory if none is found.
func findOrCreateStorePath() (string, error) {
	// Find the nearest .envmagic file, erroring if none is found.
	cwd, err := os.Getwd()
	if err != nil {
		return "", errorf("getcwd: %v", err)
	}
	dbPath, found := findEnvmagic(cwd)
	if found {
		return dbPath, nil
	}

	// Prompt to create a new .envmagic file in the current directory.
	target := filepath.Join(cwd, ".envmagic")
	ok, err := promptYesNo(fmt.Sprintf("No .envmagic file found. Create %s? [y/N]: ", target))
	if err != nil {
		return "", errorf("read prompt: %v", err)
	}
	if !ok {
		return "", cli.Exit("envmagic: aborted", 1)
	}

	return target, nil
}

// findEnvmagic looks for a .envmagic file in the given directory and its parents, returning the path if found.
func findEnvmagic(start string) (string, bool) {
	dir := start
	for {
		// Check if .envmagic exists in this directory.
		candidate := filepath.Join(dir, ".envmagic")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}

		// Move up to the parent directory, unless we're already at the root.
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// validVarName returns true if s is a valid environment variable name (matches [A-Z_][A-Z0-9_]*).
func validVarName(s string) bool {
	if s == "" {
		return false
	}

	for i, c := range s {
		if c == '_' || ('A' <= c && c <= 'Z') {
			continue
		}

		if i > 0 && '0' <= c && c <= '9' {
			continue
		}

		return false
	}

	return true
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

// promptYesNo displays the prompt and reads a line of input, returning true if the input is "y" or "yes" (case-insensitive).
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

// errorf formats an error message and wraps it in a cli.Exit with code 1.
func errorf(format string, a ...any) error {
	return cli.Exit(fmt.Sprintf("envmagic: "+format, a...), 1)
}
