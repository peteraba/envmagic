package envmagic

import (
	"errors"
	"fmt"
	"os"

	"github.com/peteraba/envmagic/internal"
)

// ErrNotFound is returned by Get when the requested variable does not exist.
var ErrNotFound = errors.New("not found")

// Client holds an open store and its encryption key.
// Obtain one via Open.
type Client struct {
	s   *internal.Store
	key []byte
}

// Open opens (or creates) a store at storePath using the key from the default
// key path (~/.config/envmagic/key), generating a new key if none exists.
func Open(storePath string) (*Client, error) {
	s, err := internal.OpenStore(storePath)
	if err != nil {
		return nil, err
	}
	key, err := internal.LoadOrCreateKey()
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	return &Client{s: s, key: key}, nil
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
		return "", err
	}
	if enc == nil {
		return "", ErrNotFound
	}
	plain, err := internal.Decrypt(c.key, enc)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// Load decrypts all variables in namespace and sets them as environment
// variables in the current process via os.Setenv.
func (c *Client) Load(namespace string) error {
	entries, err := c.s.GetAll(namespace)
	if err != nil {
		return err
	}
	for _, e := range entries {
		plain, err := internal.Decrypt(c.key, e.Enc)
		if err != nil {
			return fmt.Errorf("decrypt %s: %w", e.Name, err)
		}
		if err := os.Setenv(e.Name, string(plain)); err != nil {
			return fmt.Errorf("setenv %s: %w", e.Name, err)
		}
	}
	return nil
}
