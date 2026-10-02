package main

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"
)

// cmdShellInit outputs shell code so load forms update the current shell's environment.
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

// shellInitPosix / shellInitFish eval only load or no positional arguments.
// Namespace values are skipped; help/version flags always bypass eval.

const shellInitPosix = `# envmagic shell integration - load with: eval "$(envmagic shell-init zsh)"
envmagic() {
    local _envmagic_arg _envmagic_command='' _envmagic_positional=0 _envmagic_skip=0
    for _envmagic_arg in "$@"; do
        case "$_envmagic_arg" in
            -h|--help|-v|--version)
                command envmagic "$@"
                return $?
                ;;
        esac
        if [ "$_envmagic_skip" -eq 1 ]; then
            _envmagic_skip=0
            continue
        fi
        case "$_envmagic_arg" in
            -n|--namespace) _envmagic_skip=1 ;;
            -*) ;;
            *)
                if [ "$_envmagic_positional" -eq 0 ]; then
                    _envmagic_command=$_envmagic_arg
                    _envmagic_positional=1
                fi
                ;;
        esac
    done
    if [ "$_envmagic_positional" -ne 0 ] && [ "$_envmagic_command" != load ]; then
        command envmagic "$@"
        return $?
    fi
    local _envmagic_out _envmagic_rc
    _envmagic_out="$(command envmagic "$@")"
    _envmagic_rc=$?
    if [ $_envmagic_rc -ne 0 ]; then
        return $_envmagic_rc
    fi
    if [ -n "$_envmagic_out" ]; then
        eval "$_envmagic_out" || return $?
        if [ "$_envmagic_positional" -eq 0 ]; then
            echo 'envmagic: environment variables set' >&2
        fi
    fi
}
`

const shellInitFish = `# envmagic shell integration - load with: envmagic shell-init fish | source
function envmagic
    set -l _envmagic_command ''
    set -l _envmagic_positional 0
    set -l _envmagic_skip 0
    for _envmagic_arg in $argv
        switch "$_envmagic_arg"
            case -h --help -v --version
                command envmagic $argv
                return $status
        end
        if test $_envmagic_skip -eq 1
            set _envmagic_skip 0
            continue
        end
        switch "$_envmagic_arg"
            case -n --namespace
                set _envmagic_skip 1
            case '-*'
            case '*'
                if test $_envmagic_positional -eq 0
                    set _envmagic_command "$_envmagic_arg"
                    set _envmagic_positional 1
                end
        end
    end
    if test $_envmagic_positional -ne 0; and test "$_envmagic_command" != load
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
        or return $status
        if test $_envmagic_positional -eq 0
            echo 'envmagic: environment variables set' >&2
        end
    end
end
`
