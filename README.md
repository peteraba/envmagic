# envmagic

An encrypted env-var store, scoped to your project directory.

`envmagic` is a small Go CLI that lets you stash secrets (API keys, tokens,
DB URLs) in a per-project encrypted SQLite file, then load them into your
shell on demand. Values are encrypted at rest with AES-256-GCM using a key
that lives in your user config dir, never in the repo.

## Why

`.env` files are convenient but plaintext. Password managers are secure but
clunky for shell work. `envmagic` sits between them: secrets stay encrypted
on disk (so the store can live next to your code), and a one-liner exports
them into the current shell when you need them.

## Install

Requires Go 1.26+.

```sh
go install envmagic@latest
# or, from a clone:
go build -o envmagic .
```

## Setup

Add the shell integration to your rc file once:

```sh
# bash / zsh
eval "$(envmagic shell-init zsh)"

# fish
envmagic shell-init fish | source
```

Without the shell wrapper, `envmagic NAME` just prints an `export …`
statement to stdout - you can still apply it manually with
`eval "$(envmagic NAME)"`.

## Usage

```sh
# Store a value (creates .envmagic in the current dir on first use)
envmagic api_key 'sk-abc123'

# Load a single value into the current shell
envmagic api_key

# Load ALL values from the default namespace into the current shell
envmagic

# Load ALL values from a specific namespace
envmagic -n staging

# Echo the export lines to stderr too (handy for debugging)
envmagic --debug
envmagic --debug api_key

# Use a namespace for individual get/set
envmagic -n staging db_url 'postgres://…'
envmagic -n staging db_url

# List names in a namespace
envmagic list
envmagic -n staging list

# Remove an entry
envmagic rm api_key
```

Variable names are uppercased automatically: `envmagic api_key …` stores
`API_KEY`.

## How it works

- **Store.** Each project gets a `.envmagic` SQLite file. `set` looks for one
  in the current directory and offers to create it; `get`/`list`/`rm` walk up
  the directory tree to find the nearest one (like `.git`).
- **Encryption.** Values are sealed with AES-256-GCM. Names and namespaces
  are stored in plaintext (so `list` works without the key); only values are
  encrypted.
- **Key.** A 32-byte key is generated on first use at
  `$XDG_CONFIG_HOME/envmagic/key` (mode `0600`). **Back this file up.**
  Without it, your stored values are unrecoverable.
- **Namespaces.** Use `-n NS` to keep `dev`/`staging`/`prod` separate within
  the same `.envmagic` file. Default namespace is `default`.

## Security notes

- The `.envmagic` file is safe to commit - values are encrypted - but the
  key file is not. Keep the key out of any repo or shared backup that you
  wouldn't trust with the plaintext.
- `set` creates `.envmagic` with mode `0600`; `list` reveals variable names
  but not values.
- If the key is lost or rotated, existing entries can't be decrypted; you'll
  need to re-`set` them.

## Commands

| Command                                  | Description                                  |
| ---------------------------------------- | -------------------------------------------- |
| `envmagic [-n NS]`                       | Export all values in a namespace             |
| `envmagic [-n NS] NAME`                  | Decrypt and emit `export NAME=…`             |
| `envmagic [-n NS] NAME VALUE`            | Encrypt and store `VALUE` under `NAME`       |
| `envmagic [-n NS] list` (or `ls`)        | List names in a namespace                    |
| `envmagic [-n NS] rm NAME`               | Remove a stored entry                        |
| `envmagic [-n NS] export [FILE]`         | Export namespace to a `.env` file            |
| `envmagic [-n NS] import [FILE]`         | Import a `.env` file into a namespace        |
| `envmagic shell-init <bash\|zsh\|fish>`  | Print shell integration to eval              |
| `envmagic help`                          | Show help                                    |
| `envmagic --version`                     | Show version                                 |

## License

MIT. See [LICENSE](LICENSE).
