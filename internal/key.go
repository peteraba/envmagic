package internal

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

func createKey(path string, key []byte, link func(string, string) error) ([]byte, bool, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, fmt.Errorf("failed to create key file directory %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".key-*")
	if err != nil {
		return nil, false, fmt.Errorf("failed to write key file %s: %w", path, err)
	}
	tmp := f.Name()
	if err = writeNewKey(f, key); err == nil {
		err = link(tmp, path)
		switch {
		case err == nil, errors.Is(err, os.ErrExist):
		case keyLinkUnsupported(err):
			err = createExclusive(path, key)
		default:
			err = fmt.Errorf("failed to publish key file %s: %w", path, err)
		}
	}
	removeErr := os.Remove(tmp)
	if err == nil {
		// ponytail: the key is published, so an unremovable temp file (e.g. still open elsewhere on Windows) is a harmless stale .key-* file.
		return key, true, nil
	}
	key = nil
	if errors.Is(err, os.ErrExist) {
		key, err = LoadKey(path)
		if errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("failed to write key file %s: %w", path, err)
		}
	}
	if removeErr != nil {
		return nil, false, errors.Join(err, fmt.Errorf("failed to remove temporary key file %s: %w", tmp, removeErr))
	}
	return key, false, err
}

func createExclusive(path string, key []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("failed to write key file %s: %w", path, err)
	}
	if err := writeNewKey(f, key); err != nil {
		if removeErr := os.Remove(path); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("failed to remove key file %s: %w", path, removeErr))
		}
		return err
	}
	return nil
}

func writeNewKey(f *os.File, key []byte) error {
	// ponytail: Sync/Close failures, createExclusive's cleanup after a failed write, and createKey's
	// "do not publish a temp file whose write failed" gate lack tests; they need a fault-injecting filesystem or writer seam.
	// The fchmod is redundant for a brand-new temp file (already 0600), but keeps the fallback path's mode independent of umask.
	var err error
	if err = f.Chmod(0o600); err != nil {
		err = fmt.Errorf("failed to set key file permissions %s: %w", f.Name(), err)
	} else if _, err = f.Write(key); err != nil {
		err = fmt.Errorf("failed to write key file %s: %w", f.Name(), err)
	} else if err = f.Sync(); err != nil {
		err = fmt.Errorf("failed to sync key file %s: %w", f.Name(), err)
	}
	if closeErr := f.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("failed to close key file %s: %w", f.Name(), closeErr)
	}
	return err
}

func keyLinkUnsupported(err error) bool {
	// Raw Win32 codes, distinct from syscall's POSIX constants on Windows. Safe on every OS:
	// on Unix, Errno(1) is EPERM (already a fallback) and Errno(17) is EEXIST, which createKey checks first.
	const (
		errorInvalidFunction = syscall.Errno(1)
		errorNotSameDevice   = syscall.Errno(17)
	)
	return errors.Is(err, errors.ErrUnsupported) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EXDEV) ||
		errors.Is(err, errorInvalidFunction) || errors.Is(err, errorNotSameDevice)
}
