package main

import (
	"bufio"
	"context"
	"errors"
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

	s, key, storePath, err := openActiveStore()
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

	if outPath != "" {
		target, err := os.Stat(outPath)
		if err != nil && !os.IsNotExist(err) {
			return errorf("stat %s: %v", outPath, err)
		}
		if err == nil {
			keyPath, err := internal.KeyPath()
			if err != nil {
				return errorf("key path: %v", err)
			}
			for _, protected := range []struct{ path, label string }{
				{storePath, "store"},
				{keyPath, "key file"},
			} {
				info, err := os.Stat(protected.path)
				if err != nil {
					return errorf("stat %s: %v", protected.path, err)
				}
				if os.SameFile(target, info) {
					return errorf("refusing to export over the %s %s", protected.label, outPath)
				}
			}
		}
		f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE, 0o600)
		if err != nil {
			return errorf("open %s: %v", outPath, err)
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return errorf("stat %s: %v", outPath, err)
		}
		if info.Mode().IsRegular() {
			if err := f.Chmod(0o600); err != nil {
				_ = f.Close()
				return errorf("chmod %s: %v", outPath, err)
			}
			if err := f.Truncate(0); err != nil {
				_ = f.Close()
				return errorf("truncate %s: %v", outPath, err)
			}
		}
		// ponytail: In-place writes leave the target truncated on failure (e.g. disk full);
		// temp+rename avoids that but breaks FIFOs, /dev/stdout, hard links and file ownership.
		// Untested (need a failing filesystem or an injection seam): close errors, the chmod/truncate
		// failure branches and their order, and the post-open stat errors.
		_, err = f.WriteString(output.String())
		closeErr := f.Close()
		if err != nil {
			return errorf("write %s: %v", outPath, err)
		}
		if closeErr != nil {
			return errorf("close %s: %v", outPath, closeErr)
		}
	} else if _, err := fmt.Fprint(os.Stdout, output.String()); err != nil {
		return errorf("write stdout: %v", err)
	}

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

	names := make(map[string]struct{}, len(kvs))
	for _, kv := range kvs {
		names[kv[0]] = struct{}{}
	}
	fmt.Fprintf(os.Stderr, "envmagic: imported %d variable(s) from %s into namespace %q\n", len(names), src, ns)

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

	entries := make([]internal.Entry, 0, len(kvs))
	for _, kv := range kvs {
		label := ""
		if includeName {
			label = " " + kv[0]
		}
		enc, err := internal.Encrypt(key, []byte(kv[1]), internal.AD(ns, kv[0]))
		if err != nil {
			return "", errorf("encrypt%s: %v", label, err)
		}
		entries = append(entries, internal.Entry{Name: kv[0], Enc: enc})
	}
	if err := s.SetAll(ns, entries); err != nil {
		return "", errorf("write: %v", err)
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
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)

	lineNum := 0
	for sc.Scan() {
		lineNum++

		line := sc.Text()
		if lineNum == 1 {
			line = strings.TrimPrefix(line, "\uFEFF")
		}
		line = strings.TrimLeft(line, " \t")
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

	err := sc.Err()
	if errors.Is(err, bufio.ErrTooLong) {
		return nil, fmt.Errorf("line %d: %w", lineNum+1, err)
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

func parseDotenvValue(raw string) (string, error) {
	quoted := strings.TrimLeft(raw, " \t")
	if len(quoted) == 0 {
		return "", nil
	}
	switch quoted[0] {
	case '\'':
		end := strings.Index(quoted[1:], "'")
		if end < 0 {
			return "", fmt.Errorf("unterminated single-quoted value")
		}
		return quoted[1 : 1+end], checkDotenvTail(quoted[2+end:])
	case '"':
		return parseDQString(quoted[1:])
	default:
		return strings.TrimRight(raw, " \t"), nil
	}
}

func checkDotenvTail(tail string) error {
	tail = strings.TrimSpace(tail)
	if tail != "" && !strings.HasPrefix(tail, "#") {
		return fmt.Errorf("unexpected characters after quoted value")
	}
	return nil
}

func parseDQString(s string) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '"' {
			return b.String(), checkDotenvTail(s[i+1:])
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
