// Package secret encrypts camera passwords at rest with AES-256-GCM.
// The key lives in a separate file (client.key) created on first start.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

const prefix = "enc:"

// Box encrypts and decrypts strings.
type Box struct {
	aead cipher.AEAD
}

// Open loads the key from path, creating a new random key if missing.
func Open(path string) (*Box, error) {
	var key []byte
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
			return nil, fmt.Errorf("write key: %w", err)
		}
	case err != nil:
		return nil, err
	default:
		key, err = hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("invalid key file %s", path)
		}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// IsEncrypted reports whether s has the encrypted prefix.
func IsEncrypted(s string) bool { return strings.HasPrefix(s, prefix) }

// Encrypt returns "enc:<base64(nonce|ciphertext)>". Empty stays empty.
func (b *Box) Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt reverses Encrypt. Values without the prefix are returned as is
// (plain text typed into the config by hand).
func (b *Box) Decrypt(s string) (string, error) {
	if !IsEncrypted(s) {
		return s, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, prefix))
	if err != nil {
		return "", err
	}
	n := b.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("ciphertext too short")
	}
	p, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", errors.New("cannot decrypt (wrong client.key?)")
	}
	return string(p), nil
}
