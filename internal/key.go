package internal

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// KeyPath returns the default encryption key file path.
func KeyPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user config dir: %w", err)
	}

	return filepath.Join(cfg, "envmagic", "key"), nil
}

// LoadKey reads a key file, requiring exactly 32 bytes.
func LoadKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != 32 {
			return nil, fmt.Errorf("key file %s has invalid length %d (expected 32)", path, len(data))
		}

		return data, nil
	}

	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("key file not found at %s: %w", path, err)
	}

	return nil, fmt.Errorf("failed to read key file %s: %w", path, err)
}

// WriteKey writes the key with owner-only permissions, creating its directory if needed.
func WriteKey(path string, key []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("failed to create key file directory %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return fmt.Errorf("failed to write key file %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("failed to set key file permissions %s: %w", path, err)
	}
	return nil
}

// LoadOrCreateKey loads the 32-byte AES-256 key from the default key file,
// generating and persisting a new one if it does not exist. The second
// return value is true when a new key file was created.
func LoadOrCreateKey() ([]byte, bool, error) {
	p, err := KeyPath()
	if err != nil {
		return nil, false, fmt.Errorf("failed to get key path: %w", err)
	}

	key, err := LoadKey(p)
	if err == nil {
		return key, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}

	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, false, fmt.Errorf("failed to generate encryption key: %w", err)
	}
	if err := WriteKey(p, key); err != nil {
		return nil, false, err
	}

	return key, true, nil
}
