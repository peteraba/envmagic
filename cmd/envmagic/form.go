package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"

	"github.com/urfave/cli/v3"
)

// promptForValues walks the user through a TUI form with one input per variable,
// editing the values in kvs in place. Each field is pre-filled with the parsed
// value so Enter keeps it as the default.
// Each input gets its own group (page) so the variable name and default stay
// visible while editing; a single group scrolls and pushes them out of view.
// The form renders to stderr: the shell-init wrapper captures stdout in a command
// substitution, which would make a stdout-rendered form invisible.
func promptForValues(kvs [][2]string) error {
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		return errorf("--interactive requires a terminal; use --empty or a plain import instead")
	}

	groups := make([]*huh.Group, len(kvs))
	for i := range kvs {
		isSecret := looksSecret(kvs[i][0])
		input := huh.NewInput().
			Title(fmt.Sprintf("%s (%d/%d)", kvs[i][0], i+1, len(kvs))).
			Description(defaultDescription(kvs[i][1], isSecret)).
			Value(&kvs[i][1])
		if isSecret {
			input = input.EchoMode(huh.EchoModePassword)
		}
		groups[i] = huh.NewGroup(input)
	}

	form := huh.NewForm(groups...).WithOutput(os.Stderr)
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return cli.Exit("envmagic: aborted", 1)
		}
		return errorf("form: %v", err)
	}

	return nil
}

func defaultDescription(value string, secret bool) string {
	if value == "" {
		return "(no default)"
	}
	if secret {
		return "(default set)"
	}
	return "default: " + value
}

// looksSecret reports whether the (uppercase) variable name suggests a secret
// whose input should be masked.
func looksSecret(name string) bool {
	for _, marker := range []string{"KEY", "SECRET", "TOKEN", "PASS"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}
