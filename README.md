# envmagic

An encrypted env-var store, scoped to your project directory.

`envmagic` is a small Go CLI that lets you stash secrets (API keys, tokens,
DB URLs) in a per-project encrypted SQLite file, then load them into your
shell on demand. Values are encrypted at rest with AES-256-GCM using a key
that lives in your user config dir, never in the repo.

It also ships as an importable Go library for applications that need to load
secrets into their process environment at start-up.

## Why

`.env` files are convenient but plaintext. Password managers are secure but
clunky for shell work. `envmagic` sits between them: secrets stay encrypted
on disk (so the store can live next to your code), and a one-liner exports
them into the current shell when you need them.

## Install

Requires Go 1.26+.

```sh
go install github.com/peteraba/envmagic/cmd/envmagic@latest
# or, from a clone:
make build
```

## Setup

Add the shell integration to your rc file once:

```sh
# bash / zsh
eval "$(envmagic shell-init zsh)"

# fish
envmagic shell-init fish | source
```

Without the shell wrapper, `envmagic load` and `envmagic load NAME` print `export …`
statements; apply them with `eval "$(envmagic load)"` or `eval "$(envmagic load NAME)"`.
`envmagic get NAME` (or `envmagic NAME`) prints the raw value with a trailing newline.
Bare `envmagic` shows help.

## Usage

```sh
# Store a value (creates .envmagic in the current dir on first use)
envmagic set api_key 'sk-abc123' # or: envmagic api_key 'sk-abc123'

# Print a raw value
envmagic get api_key # or: envmagic api_key

# Load a single value into the current shell
envmagic load api_key

# Load ALL values from the default namespace into the current shell
envmagic load

# Load ALL values from a specific namespace
envmagic -n staging load

# Echo the export lines to stderr too (handy for debugging)
envmagic --debug load
envmagic --debug load api_key

# Use a namespace for individual get/set/load
envmagic -n staging set db_url 'postgres://…'
envmagic -n staging get db_url
envmagic -n staging load db_url

# List names in a namespace
envmagic list
envmagic -n staging list

# Remove an entry
envmagic rm api_key

# Import / export .env files
envmagic import .env
envmagic -n staging export staging.env

# Populate the store from a template (.env.example): fill in each value
# in an interactive form, with the template's values pre-filled as defaults
envmagic import -i .env.example

# Or just scaffold the namespace: store every name with an empty value
envmagic import --empty .env.example

# Non-interactive / CI: create .envmagic without a prompt (set/import only)
envmagic --yes set api_key 'sk-abc123'
envmagic import --yes .env
# or: ENVMAGIC_NONINTERACTIVE=1 envmagic import .env
```

Variable names are uppercased automatically: `envmagic api_key …` stores
`API_KEY`. If a name matches a subcommand (`get`, `set`, `load`, `list`, `key`, …),
read it with `envmagic get NAME` and store it with `envmagic set NAME VALUE`
(e.g. `envmagic set list foo`).

### Importing from a template

Projects often ship a `.env.example` listing the variables they need, with
defaults where they exist (`DB_PORT=5432`) and blanks where they don't
(`API_KEY=`). `envmagic import --interactive` (or `-i`) parses such a file and
walks you through a form with one field per variable; pressing Enter keeps the
template's value, and fields whose names look secret (containing `KEY`,
`SECRET`, `TOKEN`, `PASSWORD`, or `PASS`) are masked while you type.

`envmagic import --empty` skips the form and stores an empty value for every
name — template defaults are ignored — which is handy for scaffolding a
namespace non-interactively.

In all modes, importing a name that already exists overwrites the stored
value.

## Backing up the encryption key

All values are encrypted with a 32-byte key stored at
`$XDG_CONFIG_HOME/envmagic/key` (typically `~/.config/envmagic/key`).
**If this file is lost, stored values are unrecoverable.**

### Show the key

```sh
envmagic key
# path:    /home/alice/.config/envmagic/key
# content: 4Tz8…(base64)…==
```

Copy the `content` value to a password manager or other secure backup.

### Restore the key

On a new machine, or after a reinstall, paste the saved base64 string back:

```sh
envmagic key --set '4Tz8…(base64)…=='
# envmagic: key restored to /home/alice/.config/envmagic/key
```

`key --set` validates that the decoded value is exactly 32 bytes before
writing, so a truncated backup is rejected before it overwrites anything.

## How it works

- **Store.** Each project gets a `.envmagic` SQLite file. `set` looks for one
  in the current directory and offers to create it (use `--yes` or
  `ENVMAGIC_NONINTERACTIVE=1` to create without a prompt); `get`/`load`/`list`/`rm` walk up
  the directory tree to find the nearest one (like `.git`).
- **Encryption.** Values are sealed with AES-256-GCM. Names and namespaces
  are stored in plaintext (so `list` works without the key); only values are
  encrypted. Each value is bound to its namespace and name, so moving
  ciphertext between rows fails to decrypt.
- **Key.** A 32-byte key is generated on first use at
  `$XDG_CONFIG_HOME/envmagic/key` (mode `0600`). Run `envmagic key` to see
  the path and value; use `envmagic key --set <base64>` to restore it.
- **Namespaces.** Use `-n NS` to keep `dev`/`staging`/`prod` separate within
  the same `.envmagic` file. The default namespace is `default`.

### Upgrading from older builds

Before installing the new build, copy the old binary to `envmagic-old`.
Older values must be re-imported: from each store's directory, run the
following for every namespace (replace `NS`, including `default`). List them with
`sqlite3 .envmagic 'SELECT DISTINCT namespace FROM env_vars'`.

Re-import before writing anything with the new build to that namespace: the old
export fails on a new-format row, but without `pipefail` the pipe still exits 0.
Use bash/zsh with `set -o pipefail` and check stderr:

```sh
set -o pipefail
envmagic-old -n NS export | envmagic -n NS import
```

The old export silently replaces bytes that are not valid UTF-8 with U+FFFD;
set such values again by hand with the new build. Import refuses a namespace
with a line over 64 KiB and imports nothing from it: re-import it without those
names first (`envmagic-old -n NS export | grep -v '^NAME=' | envmagic -n NS import`),
then set them by hand. If you already wrote a name with the new build, remove it
with `envmagic -n NS rm NAME`, re-import, then set it again.

After all stores and namespaces are re-imported successfully, delete `envmagic-old`.

## Security notes

- The `.envmagic` file is safe to commit — values are encrypted — but the key
  file is not. Keep the key out of any repo or shared backup that you wouldn't
  trust with the plaintext.
- `set` creates `.envmagic` with mode `0600`; `list` reveals variable names
  but not values.
- `get`/`NAME` print decrypted values; `load NAME` and `load` emit
  exports that the shell wrapper evaluates. `--debug` echoes only these exports.
- If the key is lost or rotated, existing entries can't be decrypted; you'll
  need to re-`set` them.

## Commands

| Command                                  | Description                                                      |
| ---------------------------------------- | ---------------------------------------------------------------- |
| `envmagic [-n NS]`                       | Show help                                                        |
| `envmagic [-n NS] load`                  | Export all values in a namespace to the shell                    |
| `envmagic [-n NS] get NAME`              | Print the raw decrypted value and a newline                      |
| `envmagic [-n NS] NAME`                  | Alias of `get NAME`                                              |
| `envmagic [-n NS] load NAME`             | Emit `export NAME=…`; wrapper loads it into the shell            |
| `envmagic [-n NS] set NAME VALUE`        | Encrypt and store `VALUE` under `NAME`                           |
| `envmagic [-n NS] NAME VALUE`            | Encrypt and store `VALUE` under `NAME`                           |
| `envmagic [-n NS] list` (or `ls`)        | List names in a namespace                                        |
| `envmagic [-n NS] rm NAME`               | Remove a stored entry                                            |
| `envmagic [-n NS] export [FILE]`         | Export namespace to a `.env` file (stdout if omitted)            |
| `envmagic [-n NS] import [FILE]`         | Import a `.env` file into a namespace (stdin if omitted)         |
| `envmagic [-n NS] import -i FILE`        | Fill values in an interactive form (template values as defaults) |
| `envmagic [-n NS] import --empty [FILE]` | Store an empty value for every name in the file                  |
| `envmagic key`                           | Show the key file path and base64-encoded content                |
| `envmagic key --set <base64>`            | Restore the key from a base64 string                             |
| `envmagic shell-init <bash\|zsh\|fish>`  | Print shell integration to eval                                  |
| `envmagic help`                          | Show help                                                        |
| `envmagic --version`                     | Show version                                                     |

## Library usage

`envmagic` can be imported as a Go library for applications that need to load
secrets into their environment at start-up.

```sh
go get github.com/peteraba/envmagic
```

### Load all variables into the process environment

```go
package main

import (
    "log"

    "github.com/peteraba/envmagic"
)

func main() {
    c, err := envmagic.OpenWithPath("/path/to/project/.envmagic")
    if err != nil {
        log.Fatal(err)
    }
    defer c.Close()

    // Decrypts every variable in the namespace and calls os.Setenv for each.
    if _, err := c.Load(envmagic.DefaultNamespace); err != nil {
        log.Fatal(err)
    }

    // Secrets are now in the environment.
    // ...
}
```

### Read a single variable

```go
import (
    "errors"
    "log"
)

val, err := c.Get(envmagic.DefaultNamespace, "API_KEY")
if errors.Is(err, envmagic.ErrNotFound) {
    log.Fatal("API_KEY is not set")
}
if err != nil {
    log.Fatal(err)
}
```

### API reference

```go
const DefaultNamespace = "default"

// Open opens (or creates) .envmagic in the current working directory.
func Open() (*Client, error)

// OpenWithPath opens (or creates) the store at storePath, loading the key from
// $XDG_CONFIG_HOME/envmagic/key (generated on first use).
func OpenWithPath(storePath string) (*Client, error)

// OpenWithKeyAndPath opens storePath using the key file at keyPath.
func OpenWithKeyAndPath(keyPath, storePath string) (*Client, error)

// Close closes the underlying store.
func (c *Client) Close() error

// Load decrypts all variables in namespace, sets them via os.Setenv, and returns the names loaded.
func (c *Client) Load(namespace string) ([]string, error)

// Get returns the decrypted value for namespace/name.
// Use errors.Is(err, ErrNotFound) when the entry does not exist.
func (c *Client) Get(namespace, name string) (string, error)

var ErrNotFound = errors.New("not found")
```

The store path is typically the `.envmagic` file at the root of the project.
The key is shared across all projects on the machine and is loaded
automatically; applications do not need to manage it directly.

## License

MIT. See [LICENSE](LICENSE).
