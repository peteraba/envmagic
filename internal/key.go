package internal

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// KeyPath returns the path to the user's encryption key file.
func KeyPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "envmagic", "key"), nil
}

// LoadOrCreateKey loads the 32-byte AES-256 key from the default key file,
// generating and persisting a new one if it does not exist.
func LoadOrCreateKey() ([]byte, error) {
	p, err := KeyPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err == nil {
		if len(data) != 32 {
			return nil, fmt.Errorf("key file %s has invalid length %d (expected 32)", p, len(data))
		}
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, key, 0o600); err != nil {
		return nil, err
	}

	fmt.Fprintf(os.Stderr, "envmagic: generated new encryption key at %s\n", p)
	fmt.Fprintln(os.Stderr, "envmagic: BACK THIS FILE UP - without it, stored values cannot be decrypted.")

	return key, nil
}
