package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"

	"github.com/peteraba/envmagic/internal"
)

func cmdExport(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() > 1 {
		return cli.Exit("usage: envmagic export [-n NS] [FILE]", 2)
	}
	ns := cmd.String("namespace")
	outPath := cmd.Args().First()

	s, key, err := openActiveStore()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	entries, err := s.GetAll(ns)
	if err != nil {
		return errorf("read: %v", err)
	}

	var output strings.Builder
	for _, e := range entries {
		plain, err := internal.Decrypt(key, e.Enc, internal.AD(ns, e.Name))
		if err != nil {
			return errorf("decrypt %s: %v (wrong key or stored by an older envmagic; re-import it (see README))", e.Name, err)
		}
		if err := checkValue(e.Name, string(plain)); err != nil {
			return err
		}
		fmt.Fprintf(&output, "%s=%s\n", e.Name, dotenvQuote(string(plain)))
	}

	var w io.Writer = os.Stdout
	if outPath != "" {
		f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return errorf("open %s: %v", outPath, err)
		}
		defer func() { _ = f.Close() }()
		w = f
	}

	_, _ = fmt.Fprint(w, output.String())

	if outPath != "" {
		_, _ = fmt.Fprintf(os.Stderr, "envmagic: exported %d variable(s) from namespace %q to %s\n", len(entries), ns, outPath)
	}

	return nil
}

// cmdImport reads NAME=value lines from a file or stdin and stores them in the active store under the given namespace.
// With --empty every variable is stored with an empty value; with --interactive the
// user is walked through a form to fill in each value, file values pre-filled as defaults.
func cmdImport(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() > 1 {
		return cli.Exit("usage: envmagic import [-n NS] [-i|--empty] [FILE]", 2)
	}
	interactive := cmd.Bool("interactive")
	empty := cmd.Bool("empty")
	if interactive && empty {
		return cli.Exit("envmagic import: --interactive and --empty are mutually exclusive", 2)
	}

	ns := cmd.String("namespace")
	inPath := cmd.Args().First()
	if interactive && inPath == "" {
		return cli.Exit("envmagic import: --interactive requires a FILE argument", 2)
	}
	if inPath == "" && isatty.IsTerminal(os.Stdin.Fd()) {
		return cli.Exit("envmagic import: provide a FILE or pipe .env content on stdin", 2)
	}

	var r io.Reader = os.Stdin
	if inPath != "" {
		f, err := os.Open(inPath)
		if err != nil {
			return errorf("open %s: %v", inPath, err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}

	kvs, err := parseDotenv(r)
	if err != nil {
		return errorf("parse: %v", err)
	}
	if len(kvs) == 0 {
		fmt.Fprintln(os.Stderr, "envmagic: no variables found in input")
		return nil
	}

	switch {
	case empty:
		for i := range kvs {
			kvs[i][1] = ""
		}
	case interactive:
		if err := promptForValues(kvs); err != nil {
			return err
		}
	}

	if _, err := storeAll(cmd, ns, kvs, true); err != nil {
		return err
	}

	src := "stdin"
	if inPath != "" {
		src = inPath
	}

	fmt.Fprintf(os.Stderr, "envmagic: imported %d variable(s) from %s into namespace %q\n", len(kvs), src, ns)

	return nil
}

// storeAll encrypts and stores all kvs in the active store under the given namespace,
// creating the store if needed. Existing entries are overwritten.
func storeAll(cmd *cli.Command, ns string, kvs [][2]string, includeName bool) (string, error) {
	for _, kv := range kvs {
		if err := checkValue(kv[0], kv[1]); err != nil {
			return "", err
		}
	}

	dbPath, checked, err := findOrCreateStorePath(cmd)
	if err != nil {
		return "", err
	}

	key, err := loadKey()
	if err != nil {
		return "", errorf("load key: %v", err)
	}

	s, err := openCheckedStore(dbPath, checked)
	if err != nil {
		return "", errorf("open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	for _, kv := range kvs {
		label := ""
		if includeName {
			label = " " + kv[0]
		}
		enc, err := internal.Encrypt(key, []byte(kv[1]), internal.AD(ns, kv[0]))
		if err != nil {
			return "", errorf("encrypt%s: %v", label, err)
		}
		if err := s.Set(ns, kv[0], enc); err != nil {
			return "", errorf("write%s: %v", label, err)
		}
	}

	return dbPath, nil
}

// parseDotenv reads NAME=value lines from r.
// Blank lines and lines starting with # are skipped.
// An optional "export " prefix is stripped.
// Values may be unquoted, single-quoted, or double-quoted.
func parseDotenv(r io.Reader) ([][2]string, error) {
	var result [][2]string

	sc := bufio.NewScanner(r)

	lineNum := 0
	for sc.Scan() {
		lineNum++

		line := strings.TrimLeft(sc.Text(), " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimLeft(line, " \t")

		rawName, rest, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: missing '='", lineNum)
		}

		name := strings.ToUpper(strings.TrimSpace(rawName))
		if !internal.ValidName(name) {
			return nil, fmt.Errorf("line %d: invalid variable name %q", lineNum, rawName)
		}

		val, err := parseDotenvValue(rest)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}

		result = append(result, [2]string{name, val})
	}

	return result, sc.Err()
}

func parseDotenvValue(raw string) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	switch raw[0] {
	case '\'':
		end := strings.Index(raw[1:], "'")
		if end < 0 {
			return "", fmt.Errorf("unterminated single-quoted value")
		}
		return raw[1 : 1+end], nil
	case '"':
		return parseDQString(raw[1:])
	default:
		return strings.TrimRight(raw, " \t"), nil
	}
}

func parseDQString(s string) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '"' {
			return b.String(), nil
		}
		if c == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case '$':
				b.WriteByte('$')
			case '`':
				b.WriteByte('`')
			default:
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
		} else {
			b.WriteByte(c)
		}
		i++
	}
	return "", fmt.Errorf("unterminated double-quoted value")
}

var dotenvEscaper = strings.NewReplacer(
	`"`, `\"`, `\`, `\\`, `$`, `\$`, "`", "\\`",
	"\n", `\n`, "\r", `\r`, "\t", `\t`,
)

func dotenvQuote(s string) string {
	return `"` + dotenvEscaper.Replace(s) + `"`
}
