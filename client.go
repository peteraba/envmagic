// Package envmagic loads encrypted project environment variables from SQLite stores.
// Values are encrypted with a per-user key shared across projects.
// Variable names are stored upper-case. Stores and keys must already exist;
// create them with the envmagic set or import CLI commands.
package envmagic

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/peteraba/envmagic/internal"
)

// DefaultNamespace is the conventional default namespace for library callers.
const DefaultNamespace = "default"

// ErrNotFound is returned by Get when the requested variable does not exist.
var ErrNotFound = errors.New("not found")

// Client holds an open store and its encryption key.
// Obtain one via Open, OpenWithPath, or OpenWithKeyAndPath.
type Client struct {
	s   *internal.Store
	key []byte
}

// OpenWithKeyAndPath opens an existing store at storePath with the existing key at keyPath.
// It never creates either file; use envmagic set or import to create them.
// A missing store or key returns an error wrapping os.ErrNotExist.
// On Unix, it refuses a store file owned by another user.
func OpenWithKeyAndPath(keyPath, storePath string) (*Client, error) {
	key, err := internal.LoadKey(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load key, key path: %s, error: %w", keyPath, err)
	}

	if _, err := os.Stat(storePath); err != nil {
		return nil, fmt.Errorf("failed to stat store %s: %w", storePath, err)
	}
	s, err := internal.OpenStore(storePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open store, store path: %s, error: %w", storePath, err)
	}

	return &Client{s: s, key: key}, nil
}

// OpenWithPath opens an existing store at storePath with the existing default key
// (~/.config/envmagic/key). It never creates either file; use envmagic set or import to create them.
// A missing store or key returns an error wrapping os.ErrNotExist.
// On Unix, it refuses a store file owned by another user.
func OpenWithPath(storePath string) (*Client, error) {
	keyPath, err := internal.KeyPath()
	if err != nil {
		return nil, fmt.Errorf("failed to get key path: %w", err)
	}
	return OpenWithKeyAndPath(keyPath, storePath)
}

// Open opens the existing .envmagic store in the current working directory with
// the existing default key (~/.config/envmagic/key). It never creates either file;
// use envmagic set or import to create them.
// A missing store or key returns an error wrapping os.ErrNotExist.
// On Unix, it refuses a store file owned by another user.
func Open() (*Client, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}

	return OpenWithPath(filepath.Join(dir, ".envmagic"))
}

// Close closes the underlying store.
func (c *Client) Close() error {
	return c.s.Close()
}

// Get retrieves and decrypts the value for namespace/name.
// Names are stored upper-case; the caller must pass the upper-case name.
// Returns ErrNotFound if the entry does not exist; use errors.Is(err, ErrNotFound).
func (c *Client) Get(namespace, name string) (string, error) {
	enc, err := c.s.Get(namespace, name)
	if err != nil {
		if errors.Is(err, internal.ErrEntryNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("failed to get entry, namespace: %s, name: %s, error: %w", namespace, name, err)
	}

	plain, err := internal.Decrypt(c.key, enc, internal.AD(namespace, name))
	if err != nil {
		return "", fmt.Errorf("failed to decrypt entry: %w (wrong key or stored by an older envmagic; re-import it (see README))", err)
	}

	return string(plain), nil
}

// Load decrypts all variables in namespace and sets them as environment
// variables in the current process via os.Setenv, overriding existing variables
// of the same name. It returns the names it loaded. On error, it sets nothing.
func (c *Client) Load(namespace string) ([]string, error) {
	entries, err := c.s.GetAll(namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get all entries: %w", err)
	}

	values := make([]string, len(entries))
	for i, e := range entries {
		plain, err := internal.Decrypt(c.key, e.Enc, internal.AD(namespace, e.Name))
		if err != nil {
			return nil, fmt.Errorf("decrypt %s: %w (wrong key or stored by an older envmagic; re-import it (see README))", e.Name, err)
		}

		if strings.ContainsRune(string(plain), 0) {
			return nil, fmt.Errorf("value for %s contains a NUL byte", e.Name)
		}
		values[i] = string(plain)
	}

	var loaded []string
	for i, e := range entries {
		if err := os.Setenv(e.Name, values[i]); err != nil {
			return nil, fmt.Errorf("setenv %s: %w", e.Name, err)
		}

		loaded = append(loaded, e.Name)
	}

	return loaded, nil
}
