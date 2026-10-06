# Go library

[Overview](../README.md) · [Usage](usage.md) · [How it works](how-it-works.md) · [Operations](operations.md)

Use the library when the Go application should read secrets itself, without a
shell wrapper. It is a read/load API, not a store-writing API. Create the store
and key first with the CLI, and provision the same key on the application's
machine. Do not generate a replacement for an existing encrypted store.

```sh
go get github.com/peteraba/envmagic
```

## Choose paths explicitly

| Constructor | Store | Key |
| --- | --- | --- |
| `envmagic.Open()` | `.envmagic` in the current working directory | Default OS config location |
| `envmagic.OpenWithPath(storePath)` | Supplied path | Default OS config location |
| `envmagic.OpenWithKeyAndPath(keyPath, storePath)` | Second argument | First argument |

Unlike the CLI, `Open()` **does not search parents**. Prefer `OpenWithPath` when
the application can run from several directories. The default key location is
the same [OS-specific path](operations.md#back-up-and-restore) used by the CLI.
Explicit key paths can be useful for dedicated service accounts or isolated
application deployments.

All constructors require existing files; they do not create the key or store.
Use `errors.Is(err, os.ErrNotExist)` to identify a missing file. On Unix, opening
a store owned by a different user fails. Close the client when finished.

## Load the process environment

This complete example opens the store in the working directory and loads its
`default` namespace. It logs loaded names, not values:

```go
package main

import (
    "errors"
    "log"
    "os"

    "github.com/peteraba/envmagic"
)

func main() {
    c, err := envmagic.OpenWithPath(".envmagic")
    if errors.Is(err, os.ErrNotExist) {
        log.Fatal("provision the existing store and its original key before starting")
    }
    if err != nil {
        log.Fatal(err)
    }
    defer c.Close()

    names, err := c.Load(envmagic.DefaultNamespace)
    if err != nil {
        log.Fatal(err)
    }
    log.Printf("loaded variables: %v", names)
}
```

`Load(namespace)` returns the loaded names and uses `os.Setenv` to overwrite
matching variables in **this process**. Children started afterward inherit them;
a parent shell or already-running child does not. Variables absent from the
namespace are not unset. Load before starting consumers or spawning children.

The entire namespace is read, names validated, values decrypted, and NUL bytes
rejected before environment updates begin. A failure in that preflight leaves
the environment untouched. If `os.Setenv` fails during application, earlier
updates are not rolled back. An empty namespace loads no names.

## Read one value without changing the environment

`Get(namespace, name)` returns a string. Pass uppercase names yourself: unlike
the CLI, the library does not uppercase them. Missing entries match
`envmagic.ErrNotFound`; an empty stored value is a successful read, not a missing
entry. Handle it separately if your application requires a nonempty value.

For example, this function uses the imports above and returns the value for your
application to pass directly to its configuration:

```go
func readAPIKey(c *envmagic.Client) (string, error) {
    value, err := c.Get(envmagic.DefaultNamespace, "API_KEY")
    if errors.Is(err, envmagic.ErrNotFound) {
        return "", errors.New("API_KEY is not configured in the default namespace")
    }
    if err != nil {
        return "", err
    }
    return value, nil
}
```

`Get` does not export, print, or change the value. It can return bytes that are
not suitable for an environment variable; `Load` additionally rejects NUL.
Wrong keys and old ciphertext produce decryption errors, not `ErrNotFound`.

## API and security references

- [Package API on pkg.go.dev](https://pkg.go.dev/github.com/peteraba/envmagic).
- [Client implementation](../client.go) and [runnable repository example](../example_test.go).
- [Security boundaries](how-it-works.md#security-boundaries): values and keys are
  available to code running as the application user; environment loading is not
  a vault boundary.
