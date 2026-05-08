package main

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"
)

// cmdShellInit outputs shell code to integrate envmagic with the user's shell, allowing commands like "envmagic set NAME=VALUE" to update the environment of the current shell session. The user is expected to load this code into their shell with something like "eval $(envmagic shell-init zsh)" or "envmagic shell-init fish | source".
func cmdShellInit(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() == 0 {
		return cli.Exit("usage: envmagic shell-init <bash|zsh|fish>", 2)
	}
	switch cmd.Args().First() {
	case "bash", "zsh", "sh":
		fmt.Print(shellInitPosix)
	case "fish":
		fmt.Print(shellInitFish)
	default:
		fmt.Fprintf(os.Stderr, "envmagic: unsupported shell %q (supported: bash, zsh, fish)\n", cmd.Args().First())
		return cli.Exit("", 2)
	}
	return nil
}

const shellInitPosix = `# envmagic shell integration - load with: eval "$(envmagic shell-init zsh)"
envmagic() {
    case "$1" in
        shell-init|help|--help|-h|--version|-v)
            command envmagic "$@"
            return $?
            ;;
    esac
    local _envmagic_out _envmagic_rc
    _envmagic_out="$(command envmagic "$@")"
    _envmagic_rc=$?
    if [ $_envmagic_rc -ne 0 ]; then
        return $_envmagic_rc
    fi
    if [ -n "$_envmagic_out" ]; then
        eval "$_envmagic_out"
    fi
}
`

const shellInitFish = `# envmagic shell integration - load with: envmagic shell-init fish | source
function envmagic
    switch "$argv[1]"
        case shell-init help --help -h --version -v
            command envmagic $argv
            return $status
    end
    set -l _envmagic_out (command envmagic $argv)
    set -l _envmagic_rc $status
    if test $_envmagic_rc -ne 0
        return $_envmagic_rc
    end
    if test -n "$_envmagic_out"
        eval "$_envmagic_out"
    end
end
`
