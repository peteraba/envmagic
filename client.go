package envmagic

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/peteraba/envmagic/internal"
)

const DEFAULT_NAMESPACE = "default"

// ErrNotFound is returned by Get when the requested variable does not exist.
var ErrNotFound = errors.New("not found")

// Client holds an open store and its encryption key.
// Obtain one via Open.
type Client struct {
	s   *internal.Store
	key []byte
}

// OpenWithKeyAndPath opens a store at storePath using the key from the specified key path.
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

// OpenWithPath opens (or creates) a store at storePath using the key from the default
// key path (~/.config/envmagic/key), generating a new key if none exists.
func OpenWithPath(storePath string) (*Client, error) {
	s, err := internal.OpenStore(storePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open store, store path: %s, error: %w", storePath, err)
	}

	key, err := internal.LoadOrCreateKey()
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("failed to load or create key, store path: %s, error: %w", storePath, err)
	}

	return &Client{s: s, key: key}, nil
}

// Open opens (or creates) a store at the default store path using the key from the default
// key path (~/.config/envmagic/key), generating a new key if none exists.
func Open() (*Client, error) {
	dir, err := os.Getwd()
	if err != nil {
		slog.Error("Error getting working directory", "error", err, "dir", dir)
		os.Exit(1)
	}

	return OpenWithPath(dir + "/.envmagic")
}

// Close closes the underlying store.
func (c *Client) Close() error {
	return c.s.Close()
}

// Get retrieves and decrypts the value for namespace/name.
// Returns ErrNotFound if the entry does not exist.
func (c *Client) Get(namespace, name string) (string, error) {
	enc, err := c.s.Get(namespace, name)
	if err != nil {
		return "", fmt.Errorf("failed to get entry, namespace: %s, name: %s, error: %w", namespace, name, err)
	}

	if enc == nil {
		return "", fmt.Errorf("entry not found: %w", ErrNotFound)
	}

	plain, err := internal.Decrypt(c.key, enc)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt entry: %w", err)
	}

	return string(plain), nil
}

// Load decrypts all variables in namespace and sets them as environment
// variables in the current process via os.Setenv.
func (c *Client) Load(namespace string) ([]string, error) {
	entries, err := c.s.GetAll(namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get all entries: %w", err)
	}

	var loaded []string
	for _, e := range entries {
		plain, err := internal.Decrypt(c.key, e.Enc)
		if err != nil {
			return nil, fmt.Errorf("decrypt %s: %w", e.Name, err)
		}

		if err := os.Setenv(e.Name, string(plain)); err != nil {
			return nil, fmt.Errorf("setenv %s: %w", e.Name, err)
		}

		loaded = append(loaded, e.Name)
	}

	return loaded, nil
}
