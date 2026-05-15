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
// generating and persisting a new one if it does not exist. The second
// return value is true when a new key file was created.
func LoadOrCreateKey() (key []byte, created bool, err error) {
	p, err := KeyPath()
	if err != nil {
		return nil, false, fmt.Errorf("failed to get key path: %w", err)
	}

	data, err := os.ReadFile(p)
	if err == nil {
		if len(data) != 32 {
			return nil, false, fmt.Errorf("key file %s has invalid length %d (expected 32)", p, len(data))
		}

		return data, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("failed to read key file %s: %w", p, err)
	}

	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, false, fmt.Errorf("failed to create key file directory %s: %w", filepath.Dir(p), err)
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, false, fmt.Errorf("failed to generate encryption key: %w", err)
	}
	if err := os.WriteFile(p, key, 0o600); err != nil {
		return nil, false, fmt.Errorf("failed to write key file %s: %w", p, err)
	}

	return key, true, nil
}
