package internal

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// KeyPath returns the path to the user's encryption key file.
func KeyPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user config dir: %w", err)
	}

	return filepath.Join(cfg, "envmagic", "key"), nil
}

// LoadKey loads the 32-byte AES-256 key from the specified path.
func LoadKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != 32 {
			return nil, fmt.Errorf("key file %s has invalid length %d (expected 32)", path, len(data))
		}

		return data, nil
	}

	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("failed to read key file %s: %w", path, err)
	}

	return nil, fmt.Errorf("key file not found at %s", path)
}

// LoadOrCreateKey loads the 32-byte AES-256 key from the default key file,
// generating and persisting a new one if it does not exist.
func LoadOrCreateKey() ([]byte, error) {
	// First, try to load the key from the default location.
	p, err := KeyPath()
	if err != nil {
		return nil, fmt.Errorf("failed to get key path: %w", err)
	}

	// If the file exists, read and return the key.
	data, err := os.ReadFile(p)
	if err == nil {
		if len(data) != 32 {
			return nil, fmt.Errorf("key file %s has invalid length %d (expected 32)", p, len(data))
		}

		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("failed to read key file %s: %w", p, err)
	}

	// Otherwise, generate a random 32-byte key for AES-256 and save it.
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, fmt.Errorf("failed to create key file directory %s: %w", filepath.Dir(p), err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("failed to generate encryption key: %w", err)
	}
	if err := os.WriteFile(p, key, 0o600); err != nil {
		return nil, fmt.Errorf("failed to write key file %s: %w", p, err)
	}

	fmt.Fprintf(os.Stderr, "envmagic: generated new encryption key at %s\nenvmagic: key (base64): %s\n", p, base64.StdEncoding.EncodeToString(key))
	fmt.Fprintf(os.Stderr, "envmagic: You can display the key again later by running `envmagic key`.\n")
	fmt.Fprintln(os.Stderr, "envmagic: BACK THIS FILE UP - without it, stored values cannot be decrypted.")

	return key, nil
}
