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

const version = "0.2.1"

func main() {
	if err := newApp().Run(context.Background(), os.Args); err != nil {
		os.Exit(exitCode(err))
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

func cmdList(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() > 0 {
		return cli.Exit(fmt.Sprintf("envmagic list: unexpected arguments: %v", cmd.Args().Slice()), 2)
	}
	ns := cmd.String("namespace")

	s, err := openActiveStore()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	names, err := s.List(ns)
	if err != nil {
		return errorf("list: %v", err)
	}
	if len(names) == 0 {
		fmt.Fprintf(os.Stderr, "envmagic: no entries in namespace %q\n", ns)
		return nil
	}
	for _, n := range names {
		fmt.Println(n)
	}
	return nil
}

func cmdRemove(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() != 1 {
		return cli.Exit("usage: envmagic rm [-n NS] NAME", 2)
	}
	ns := cmd.String("namespace")
	rawName := cmd.Args().First()
	name := strings.ToUpper(rawName)
	if !validVarName(name) {
		return errorf("invalid env var name %q", rawName)
	}

	s, err := openActiveStore()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

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

func runSet(namespace, name, value string) error {
	dbPath, err := findOrCreateStorePath()
	if err != nil {
		return err
	}

	key, err := loadOrCreateKey()
	if err != nil {
		return errorf("load key: %v", err)
	}

	enc, err := encrypt(key, []byte(value))
	if err != nil {
		return errorf("encrypt: %v", err)
	}

	s, err := openStore(dbPath)
	if err != nil {
		return errorf("open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Set(namespace, name, enc); err != nil {
		return errorf("write: %v", err)
	}

	fmt.Fprintf(os.Stderr, "envmagic: stored %s (namespace %q) in %s\n", name, namespace, dbPath)
	return nil
}

func runGet(namespace, name string, debug bool) error {
	s, err := openActiveStore()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	key, err := loadOrCreateKey()
	if err != nil {
		return errorf("load key: %v", err)
	}

	enc, err := s.Get(namespace, name)
	if err != nil {
		return errorf("read: %v", err)
	}
	if enc == nil {
		return errorf("%s not found in namespace %q", name, namespace)
	}

	plain, err := decrypt(key, enc)
	if err != nil {
		return errorf("decrypt: %v (wrong key, or value was encrypted with a different key)", err)
	}

	line := fmt.Sprintf("export %s=%s", name, shellQuote(string(plain)))
	fmt.Println(line)
	if debug {
		fmt.Fprintln(os.Stderr, line)
	}
	return nil
}

func runSourceAll(namespace string, debug bool) error {
	s, err := openActiveStore()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	key, err := loadOrCreateKey()
	if err != nil {
		return errorf("load key: %v", err)
	}

	entries, err := s.GetAll(namespace)
	if err != nil {
		return errorf("read: %v", err)
	}
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
	cwd, err := os.Getwd()
	if err != nil {
		return nil, errorf("getcwd: %v", err)
	}
	dbPath, found := findEnvmagic(cwd)
	if !found {
		return nil, errorf("no .envmagic file found in %s or any parent", cwd)
	}
	s, err := openStore(dbPath)
	if err != nil {
		return nil, errorf("open store: %v", err)
	}
	return s, nil
}

// findOrCreateStorePath returns the path to the nearest .envmagic file,
// prompting to create one in the current directory if none is found.
func findOrCreateStorePath() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", errorf("getcwd: %v", err)
	}
	dbPath, found := findEnvmagic(cwd)
	if found {
		return dbPath, nil
	}
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
	fmt.Fprint(os.Stderr, prompt)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, os.ErrClosed) && line == "" {
		return false, err
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes", nil
}

func errorf(format string, a ...any) error {
	return cli.Exit(fmt.Sprintf("envmagic: "+format, a...), 1)
}
