// Package crypto provides AES-256-GCM encryption for OAuth token storage.
//
// Token file format: base64(12-byte-nonce || AES-256-GCM-ciphertext)
// Key source: GA_ENCRYPTION_KEY env var (32 bytes, hex-encoded) — never hardcoded.
//
// Atomic write guarantee: SaveTokens writes to a .tmp file then renames, ensuring
// the token file is never partially written even on crash or power loss.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/johnmartinez/cgm-get-agent/internal/types"
)

// Encrypt encrypts plaintext using AES-256-GCM with a random nonce.
// key must be exactly 32 bytes.
// Output: base64(12-byte-nonce || ciphertext || 16-byte-GCM-tag).
func Encrypt(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: creating GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: generating nonce: %w", err)
	}
	// Seal appends ciphertext+tag to nonce, giving us nonce||ciphertext||tag.
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(sealed)))
	base64.StdEncoding.Encode(encoded, sealed)
	return encoded, nil
}

// Decrypt decrypts a value produced by Encrypt.
// Returns an error if the key is wrong or the ciphertext has been tampered with.
func Decrypt(ciphertext, key []byte) ([]byte, error) {
	raw := make([]byte, base64.StdEncoding.DecodedLen(len(ciphertext)))
	n, err := base64.StdEncoding.Decode(raw, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("crypto: base64 decode: %w", err)
	}
	raw = raw[:n]

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: creating GCM: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return nil, fmt.Errorf("crypto: ciphertext too short")
	}
	nonce, data := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: decryption failed (wrong key or tampered data): %w", err)
	}
	return plaintext, nil
}

// SaveTokens marshals tokens to JSON, encrypts with key, and atomically writes to path.
// Atomic write: write to path+".tmp" then os.Rename to path (POSIX atomic on same filesystem).
// Directory is created with mode 0700 if it does not exist.
func SaveTokens(path string, tokens types.OAuthTokens, key []byte) error {
	data, err := json.Marshal(tokens)
	if err != nil {
		return fmt.Errorf("crypto: marshaling tokens: %w", err)
	}
	encrypted, err := Encrypt(data, key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("crypto: creating token directory: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encrypted, 0600); err != nil {
		return fmt.Errorf("crypto: writing temp token file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("crypto: renaming token file: %w", err)
	}
	return nil
}

// LoadTokens reads, decrypts, and unmarshals OAuthTokens from path.
// Returns an error if the file does not exist, the key is wrong, or the data is corrupt.
func LoadTokens(path string, key []byte) (types.OAuthTokens, error) {
	var tokens types.OAuthTokens
	data, err := os.ReadFile(path)
	if err != nil {
		return tokens, fmt.Errorf("crypto: reading token file: %w", err)
	}
	plaintext, err := Decrypt(data, key)
	if err != nil {
		return tokens, err
	}
	if err := json.Unmarshal(plaintext, &tokens); err != nil {
		return tokens, fmt.Errorf("crypto: unmarshaling tokens: %w", err)
	}
	return tokens, nil
}
