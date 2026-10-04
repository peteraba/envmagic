package internal

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
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
	// Filesystems without hard links publish via exclusive creation; allow that writer to finish.
	for attempt := 0; err == nil && len(data) < 32 && attempt < 20; attempt++ {
		time.Sleep(25 * time.Millisecond)
		data, err = os.ReadFile(path)
	}
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
	// ponytail: write/close error checks and absence of O_TRUNC lack tests; need a short-write/RLIMIT fixture or a writable regular file the user cannot chmod.
	// The 0o600 create mode is also unbound (fchmod masks it), but closes the window between create and fchmod.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("failed to create key file directory %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("failed to write key file %s: %w", path, err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to set key file permissions %s: %w", path, err)
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to truncate key file %s: %w", path, err)
	}
	_, err = f.Write(key)
	closeErr := f.Close()
	if err != nil {
		return fmt.Errorf("failed to write key file %s: %w", path, err)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close key file %s: %w", path, closeErr)
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
	return createKey(p, key, os.Link)
}

func createKey(path string, key []byte, link func(string, string) error) (result []byte, created bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, fmt.Errorf("failed to create key file directory %s: %w", filepath.Dir(path), err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".key-*")
	if err != nil {
		return nil, false, fmt.Errorf("failed to create temporary key file %s: %w", path, err)
	}
	defer func() {
		if removeErr := os.Remove(f.Name()); removeErr != nil {
			result, created = nil, false
			err = errors.Join(err, fmt.Errorf("failed to remove temporary key file %s: %w", f.Name(), removeErr))
		}
	}()
	if err := writeNewKey(f, key); err != nil {
		return nil, false, err
	}

	if err := link(f.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			key, err := LoadKey(path)
			if errors.Is(err, os.ErrNotExist) {
				return nil, false, fmt.Errorf("failed to write key file %s: %w", path, err)
			}
			return key, false, err
		}
		if !keyLinkUnsupported(err) {
			return nil, false, fmt.Errorf("failed to publish key file %s: %w", path, err)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			key, err := LoadKey(path)
			if errors.Is(err, os.ErrNotExist) {
				return nil, false, fmt.Errorf("failed to write key file %s: %w", path, err)
			}
			return key, false, err
		}
		if err != nil {
			return nil, false, fmt.Errorf("failed to create key file %s: %w", path, err)
		}
		if err := writeNewKey(f, key); err != nil {
			if removeErr := os.Remove(path); removeErr != nil {
				err = errors.Join(err, fmt.Errorf("failed to remove key file %s: %w", path, removeErr))
			}
			return nil, false, err
		}
	}
	return key, true, nil
}

func writeNewKey(f *os.File, key []byte) error {
	defer func() { _ = f.Close() }()
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("failed to set key file permissions %s: %w", f.Name(), err)
	}
	if _, err := f.Write(key); err != nil {
		return fmt.Errorf("failed to write key file %s: %w", f.Name(), err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("failed to sync key file %s: %w", f.Name(), err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close key file %s: %w", f.Name(), err)
	}
	return nil
}

func keyLinkUnsupported(err error) bool {
	// Win32 codes are distinct from syscall's POSIX compatibility constants.
	const (
		errorInvalidFunction = syscall.Errno(1)
		errorNotSameDevice   = syscall.Errno(17)
	)
	return errors.Is(err, errors.ErrUnsupported) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EXDEV) ||
		(runtime.GOOS == "windows" && (errors.Is(err, errorInvalidFunction) || errors.Is(err, errorNotSameDevice)))
}
