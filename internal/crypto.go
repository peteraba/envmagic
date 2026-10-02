package internal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
)

// AD binds a value to its namespace and name without ambiguous concatenation.
func AD(namespace, name string) []byte {
	ad := binary.AppendUvarint(nil, uint64(len(namespace)))
	ad = append(ad, namespace...)
	return append(ad, name...)
}

// Encrypt seals plaintext with AES-256-GCM. Output layout: nonce || ciphertext||tag.
func Encrypt(key, plaintext, ad []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, errors.New("encrypt: key must be 32 bytes")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("error creating new cipher, error: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("error creating new GCM, error: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("error generating nonce, error: %w", err)
	}

	return gcm.Seal(nonce, nonce, plaintext, ad), nil
}

// Decrypt opens ciphertext produced by Encrypt, returning the original plaintext.
func Decrypt(key, data, ad []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, errors.New("decrypt: key must be 32 bytes")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("error creating new cipher, error: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("error creating new GCM, error: %w", err)
	}
	n := gcm.NonceSize()
	if len(data) < n {
		return nil, fmt.Errorf("decrypt: ciphertext too short, length: %d, expected at least %d", len(data), n)
	}
	nonce, ct := data[:n], data[n:]

	return gcm.Open(nil, nonce, ct, ad)
}
