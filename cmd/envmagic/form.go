package main

import (
	"errors"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"

	"github.com/urfave/cli/v3"
)

// promptForValues walks the user through a TUI form with one input per variable,
// editing the values in kvs in place. Each field is pre-filled with the parsed
// value so Enter keeps it as the default.
// The form renders to stderr: the shell-init wrapper captures stdout in a command
// substitution, which would make a stdout-rendered form invisible.
func promptForValues(kvs [][2]string) error {
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		return errorf("--interactive requires a terminal; use --empty or a plain import instead")
	}

	fields := make([]huh.Field, len(kvs))
	for i := range kvs {
		desc := "(no default)"
		if kvs[i][1] != "" {
			desc = "default: " + kvs[i][1]
		}
		input := huh.NewInput().
			Title(kvs[i][0]).
			Description(desc).
			Value(&kvs[i][1])
		if looksSecret(kvs[i][0]) {
			input = input.EchoMode(huh.EchoModePassword)
		}
		fields[i] = input
	}

	form := huh.NewForm(huh.NewGroup(fields...)).WithOutput(os.Stderr)
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return cli.Exit("envmagic: aborted", 1)
		}
		return errorf("form: %v", err)
	}

	return nil
}

// looksSecret reports whether the (uppercase) variable name suggests a secret
// whose input should be masked.
func looksSecret(name string) bool {
	for _, marker := range []string{"KEY", "SECRET", "TOKEN", "PASSWORD", "PASS"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}
