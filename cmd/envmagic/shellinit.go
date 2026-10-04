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
		return cli.Exit("usage: envmagic shell-init <bash|zsh|fish|pwsh>", 2)
	}
	switch cmd.Args().First() {
	case "bash", "zsh", "sh":
		fmt.Print(shellInitPosix)
	case "fish":
		fmt.Print(shellInitFish)
	case "pwsh", "powershell":
		fmt.Print(shellInitPwsh)
	default:
		fmt.Fprintf(os.Stderr, "envmagic: unsupported shell %q (supported: bash, zsh, fish, pwsh)\n", cmd.Args().First())
		return cli.Exit("", 2)
	}
	return nil
}

// shellInitPosix / shellInitFish eval only load; confirm only load without a name.
// Namespace values are skipped; help/version/format flags always bypass eval.

const shellInitPosix = `# envmagic shell integration - load with: eval "$(envmagic shell-init zsh)"
envmagic() {
    local _envmagic_arg _envmagic_command='' _envmagic_positional=0 _envmagic_skip=0
    for _envmagic_arg in "$@"; do
        case "$_envmagic_arg" in
            -h|--help|-v|--version|--format|--format=*|-format|-format=*)
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
                fi
                _envmagic_positional=$((_envmagic_positional + 1))
                ;;
        esac
    done
    if [ "$_envmagic_command" != load ]; then
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
        if [ "$_envmagic_positional" -eq 1 ]; then
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
            case -h --help -v --version --format '--format=*' -format '-format=*'
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
                end
                set _envmagic_positional (math $_envmagic_positional + 1)
        end
    end
    if test "$_envmagic_command" != load
        command envmagic $argv
        return $status
    end
    set -l _envmagic_out (command envmagic $argv | string collect)
    set -l _envmagic_rc $pipestatus[1]
    if test $_envmagic_rc -ne 0
        return $_envmagic_rc
    end
    if test -n "$_envmagic_out"
        eval "$_envmagic_out"
        or return $status
        if test $_envmagic_positional -eq 1
            echo 'envmagic: environment variables set' >&2
        end
    end
end
`

const shellInitPwsh = `# envmagic shell integration - load with: envmagic shell-init pwsh | Out-String | Invoke-Expression
function envmagic {
    $argv = @($args | ForEach-Object { $_ })
    $binary = (Get-Command envmagic -CommandType Application | Select-Object -First 1).Source
    $command = ''
    $positional = 0
    $skip = $false
    foreach ($arg in $argv) {
        if ($arg -cin '-h', '--help', '-v', '--version', '--format', '-format' -or $arg -clike '--format=*' -or $arg -clike '-format=*') {
            & $binary @argv
            return
        }
        if ($skip) {
            $skip = $false
            continue
        }
        if ($arg -cin '-n', '--namespace') {
            $skip = $true
        } elseif ($arg -notlike '-*') {
            if ($positional -eq 0) { $command = $arg }
            $positional++
        }
    }
    if ($command -cne 'load') {
        & $binary @argv
        return
    }
    $out = & $binary --format pwsh @argv
    if ($LASTEXITCODE -ne 0) { return }
    if ($out) {
        try {
            Invoke-Expression ($out -join "` + "`n" + `")
        } catch {
            Write-Error $_ -ErrorAction Continue
            $global:LASTEXITCODE = 1
            return
        }
        if ($positional -eq 1) {
            [Console]::Error.WriteLine('envmagic: environment variables set')
        }
    }
}
`
