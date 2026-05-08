package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// keyPath returns the path to the encryption key file, which is stored in the user's config directory.
func keyPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(cfg, "envmagic", "key"), nil
}

// loadOrCreateKey loads the encryption key from the key file, or creates a new one if it doesn't exist. The key is 32 random bytes (AES-256).
func loadOrCreateKey() ([]byte, error) {
	// Try to read the existing key file. If it exists and has the correct length, return it.
	p, err := keyPath()
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

	// Key file doesn't exist, so create a new one with a random key. Ensure the directory exists and the file is created with 0600 permissions.
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
