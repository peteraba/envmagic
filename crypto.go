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
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func decrypt(key, data []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, errors.New("decrypt: key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	n := gcm.NonceSize()
	if len(data) < n {
		return nil, errors.New("decrypt: ciphertext too short")
	}
	nonce, ct := data[:n], data[n:]
	return gcm.Open(nil, nonce, ct, nil)
}
