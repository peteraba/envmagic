package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/urfave/cli/v3"
)

// cmdExport exports all entries in the namespace to a .env file.
func cmdExport(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() > 1 {
		return cli.Exit("usage: envmagic export [-n NS] [FILE]", 2)
	}
	ns := cmd.String("namespace")
	var outPath string
	if cmd.NArg() == 1 {
		outPath = cmd.Args().First()
	}

	// Open the active store database.
	s, err := openActiveStore()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	// Load the encryption key.
	key, err := loadOrCreateKey()
	if err != nil {
		return errorf("load key: %v", err)
	}

	// Get all entries in the namespace.
	entries, err := s.GetAll(ns)
	if err != nil {
		return errorf("read: %v", err)
	}

	// By default, write to stdout. If a file path is provided, write to the file instead, creating it with 0600 permissions.
	var w io.Writer = os.Stdout
	if outPath != "" {
		f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return errorf("open %s: %v", outPath, err)
		}
		defer func() { _ = f.Close() }()
		w = f
	}

	// For each entry, decrypt the value and write it as NAME=value to the output, quoting the value as needed for .env files.
	for _, e := range entries {
		plain, err := decrypt(key, e.Enc)
		if err != nil {
			return errorf("decrypt %s: %v (wrong key?)", e.Name, err)
		}
		_, _ = fmt.Fprintf(w, "%s=%s\n", e.Name, dotenvQuote(string(plain)))
	}

	//
	if outPath != "" {
		_, _ = fmt.Fprintf(os.Stderr, "envmagic: exported %d variable(s) from namespace %q to %s\n", len(entries), ns, outPath)
	}

	return nil
}

// cmdImport reads NAME=value lines from a file or stdin and stores them in the active store under the given namespace.
func cmdImport(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() > 1 {
		return cli.Exit("usage: envmagic import [-n NS] [FILE]", 2)
	}
	ns := cmd.String("namespace")
	var inPath string
	if cmd.NArg() == 1 {
		inPath = cmd.Args().First()
	}

	// By default, read from stdin. If a file path is provided, read from the file instead.
	var r io.Reader = os.Stdin
	if inPath != "" {
		f, err := os.Open(inPath)
		if err != nil {
			return errorf("open %s: %v", inPath, err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}

	// Parse the input as dotenv format, extracting NAME=value pairs. The variable names are normalized to uppercase.
	kvs, err := parseDotenv(r)
	if err != nil {
		return errorf("parse: %v", err)
	}
	if len(kvs) == 0 {
		fmt.Fprintln(os.Stderr, "envmagic: no variables found in input")
		return nil
	}

	// Find the path to the store database, creating it if it doesn't exist.
	dbPath, err := findOrCreateStorePath()
	if err != nil {
		return err
	}

	// Load the encryption key, creating it if it doesn't exist.
	key, err := loadOrCreateKey()
	if err != nil {
		return errorf("load key: %v", err)
	}

	// Open the store database and write each variable.
	s, err := openStore(dbPath)
	if err != nil {
		return errorf("open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	// For each variable, encrypt the value and write it to the store under the given namespace.
	// The store will overwrite any existing value with the same name.
	for _, kv := range kvs {
		enc, err := encrypt(key, []byte(kv[1]))
		if err != nil {
			return errorf("encrypt %s: %v", kv[0], err)
		}
		if err := s.Set(ns, kv[0], enc); err != nil {
			return errorf("write %s: %v", kv[0], err)
		}
	}

	src := "stdin"
	if inPath != "" {
		src = inPath
	}

	fmt.Fprintf(os.Stderr, "envmagic: imported %d variable(s) from %s into namespace %q\n", len(kvs), src, ns)

	return nil
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

		// Skip blank lines and comments.
		line := strings.TrimLeft(sc.Text(), " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Trim leading "export " if present,
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimLeft(line, " \t")

		// Split on the first '=' to separate the name and value.
		rawName, rest, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: missing '='", lineNum)
		}

		// The variable name is normalized to uppercase and validated as a valid variable name.
		name := strings.ToUpper(strings.TrimSpace(rawName))
		if !validVarName(name) {
			return nil, fmt.Errorf("line %d: invalid variable name %q", lineNum, rawName)
		}

		// The value is parsed according to dotenv rules (unquoted, single-quoted, or double-quoted).
		val, err := parseDotenvValue(rest)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}

		result = append(result, [2]string{name, val})
	}

	return result, sc.Err()
}

// parseDotenvValue parses a dotenv value, which may be unquoted, single-quoted, or double-quoted. It returns the unescaped value.
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

// parseDQString parses a double-quoted string with backslash escapes, returning the unescaped value. The input should not include the opening quote, but should include the closing quote.
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

// dotenvQuote quotes s as a double-quoted value suitable for .env files, escaping special characters as needed.
func dotenvQuote(s string) string {
	var b strings.Builder

	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '$':
			b.WriteString(`\$`)
		case '`':
			b.WriteString("\\`")
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')

	return b.String()
}
