package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

// encrypt seals plaintext with AES-256-GCM. Output layout: nonce || ciphertext||tag.
func encrypt(key, plaintext []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, errors.New("encrypt: key must be 32 bytes")
	}

	// Create a new AES cipher block and GCM mode instance.
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	// Generate a random nonce and seal the plaintext.
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	// Seal appends the ciphertext and tag to the nonce, which we return as the output.
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// decrypt opens ciphertext produced by encrypt, returning the original plaintext.
func decrypt(key, data []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, errors.New("decrypt: key must be 32 bytes")
	}

	// Create a new AES cipher block and GCM mode instance.
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	// The input data is expected to be nonce || ciphertext||tag. Separate the nonce and ciphertext, then open it.
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	n := gcm.NonceSize()
	if len(data) < n {
		return nil, errors.New("decrypt: ciphertext too short")
	}
	nonce, ct := data[:n], data[n:]

	// Open verifies the tag and returns the original plaintext.
	return gcm.Open(nil, nonce, ct, nil)
}
