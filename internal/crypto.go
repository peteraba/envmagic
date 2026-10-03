package internal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"
)

// AD binds a value to its namespace and name without ambiguous concatenation.
func AD(namespace, name string) []byte {
	ad := binary.AppendUvarint(nil, uint64(len(namespace)))
	ad = append(ad, namespace...)
	return append(ad, name...)
}

func newGCM(key []byte, operation string) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("%s: key must be 32 bytes", operation)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("error creating new cipher, error: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("error creating new GCM, error: %w", err)
	}
	return gcm, nil
}

// Encrypt seals plaintext with AES-256-GCM. Output layout: nonce || ciphertext || tag.
func Encrypt(key, plaintext, ad []byte) ([]byte, error) {
	gcm, err := newGCM(key, "encrypt")
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("error generating nonce, error: %w", err)
	}

	return gcm.Seal(nonce, nonce, plaintext, ad), nil
}

// Decrypt opens AES-256-GCM data in nonce || ciphertext || tag format.
func Decrypt(key, data, ad []byte) ([]byte, error) {
	gcm, err := newGCM(key, "decrypt")
	if err != nil {
		return nil, err
	}
	n := gcm.NonceSize()
	if len(data) < n {
		return nil, fmt.Errorf("decrypt: ciphertext too short, length: %d, expected at least %d", len(data), n)
	}
	nonce, ct := data[:n], data[n:]

	return gcm.Open(nil, nonce, ct, ad)
}
