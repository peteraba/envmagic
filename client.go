// Package envmagic loads encrypted project environment variables from SQLite stores.
// Values are encrypted with a per-user key shared across projects.
// Variable names are stored upper-case.
package envmagic

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/peteraba/envmagic/internal"
)

// DefaultNamespace is the conventional default namespace for library callers.
const DefaultNamespace = "default"

// ErrNotFound is returned by Get when the requested variable does not exist.
var ErrNotFound = errors.New("not found")

// Client holds an open store and its encryption key.
// Obtain one via Open, OpenWithPath, or OpenWithKeyAndPath.
type Client struct {
	s          *internal.Store
	key        []byte
	keyCreated bool
}

// OpenWithKeyAndPath opens a store at storePath using the key from the specified key path.
// On Unix, it refuses a store file owned by another user.
func OpenWithKeyAndPath(keyPath, storePath string) (*Client, error) {
	s, err := internal.OpenStore(storePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open store, store path: %s, error: %w", storePath, err)
	}

	key, err := internal.LoadKey(keyPath)
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("failed to load key, key path: %s, error: %w", keyPath, err)
	}

	return &Client{s: s, key: key}, nil
}

// OpenWithPath opens (or creates) the SQLite store at storePath using the key from the default
// key path (~/.config/envmagic/key), generating a new key if none exists.
// If the store file does not exist, it is created on open.
// On Unix, it refuses a store file owned by another user.
func OpenWithPath(storePath string) (*Client, error) {
	s, err := internal.OpenStore(storePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open store, store path: %s, error: %w", storePath, err)
	}

	key, created, err := internal.LoadOrCreateKey()
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("failed to load or create key, store path: %s, error: %w", storePath, err)
	}

	return &Client{s: s, key: key, keyCreated: created}, nil
}

// Open opens (or creates) the store at .envmagic in the current working directory,
// using the key from the default key path (~/.config/envmagic/key), generating a new key if none exists.
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

// KeyCreated reports whether Open or OpenWithPath generated a new default key file
// (~/.config/envmagic/key) because none existed. Callers should warn users to back
// up the key when this is true.
func (c *Client) KeyCreated() bool {
	return c.keyCreated
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
// variables in the current process via os.Setenv.
// It returns the names it loaded.
func (c *Client) Load(namespace string) ([]string, error) {
	entries, err := c.s.GetAll(namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get all entries: %w", err)
	}

	var loaded []string
	for _, e := range entries {
		plain, err := internal.Decrypt(c.key, e.Enc, internal.AD(namespace, e.Name))
		if err != nil {
			return nil, fmt.Errorf("decrypt %s: %w (wrong key or stored by an older envmagic; re-import it (see README))", e.Name, err)
		}

		if err := os.Setenv(e.Name, string(plain)); err != nil {
			return nil, fmt.Errorf("setenv %s: %w", e.Name, err)
		}

		loaded = append(loaded, e.Name)
	}

	return loaded, nil
}
