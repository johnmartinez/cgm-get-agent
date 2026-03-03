package crypto_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/johnmartinez/cgm-get-agent/internal/crypto"
	"github.com/johnmartinez/cgm-get-agent/internal/types"
)

var testKey = bytes.Repeat([]byte{0x01}, 32)
var wrongKey = bytes.Repeat([]byte{0x02}, 32)

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	plaintext := []byte("test glucose token data")
	ciphertext, err := crypto.Encrypt(plaintext, testKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext must not equal plaintext")
	}
	got, err := crypto.Decrypt(ciphertext, testKey)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round-trip failed: got %q, want %q", got, plaintext)
	}
}

func TestEncrypt_ProducesUniqueCiphertexts(t *testing.T) {
	// Two encryptions of the same plaintext must differ (random nonce).
	plaintext := []byte("same plaintext")
	c1, _ := crypto.Encrypt(plaintext, testKey)
	c2, _ := crypto.Encrypt(plaintext, testKey)
	if bytes.Equal(c1, c2) {
		t.Error("two encryptions of same plaintext must differ (nonce reuse)")
	}
}

func TestDecrypt_WrongKey(t *testing.T) {
	ciphertext, _ := crypto.Encrypt([]byte("secret"), testKey)
	_, err := crypto.Decrypt(ciphertext, wrongKey)
	if err == nil {
		t.Fatal("Decrypt with wrong key must return an error")
	}
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	ciphertext, _ := crypto.Encrypt([]byte("secret"), testKey)
	// Flip a byte in the middle of the ciphertext.
	ciphertext[len(ciphertext)/2] ^= 0xFF
	_, err := crypto.Decrypt(ciphertext, testKey)
	if err == nil {
		t.Fatal("Decrypt of tampered ciphertext must return an error")
	}
}

func TestDecrypt_TooShort(t *testing.T) {
	_, err := crypto.Decrypt([]byte("dG9vc2hvcnQ="), testKey) // base64("tooshort")
	if err == nil {
		t.Fatal("Decrypt of too-short ciphertext must return an error")
	}
}

func TestSaveTokens_LoadTokens_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.enc")

	now := time.Now().UTC().Truncate(time.Second)
	tokens := types.OAuthTokens{
		AccessToken:   "access-abc",
		RefreshToken:  "refresh-xyz",
		ExpiresAt:     now.Add(30 * time.Minute),
		LastRefreshed: now,
	}

	if err := crypto.SaveTokens(path, tokens, testKey); err != nil {
		t.Fatalf("SaveTokens: %v", err)
	}

	// File must exist and be non-empty.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("token file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("token file must not be empty")
	}

	got, err := crypto.LoadTokens(path, testKey)
	if err != nil {
		t.Fatalf("LoadTokens: %v", err)
	}
	if got.AccessToken != tokens.AccessToken {
		t.Errorf("AccessToken mismatch: got %q, want %q", got.AccessToken, tokens.AccessToken)
	}
	if got.RefreshToken != tokens.RefreshToken {
		t.Errorf("RefreshToken mismatch: got %q, want %q", got.RefreshToken, tokens.RefreshToken)
	}
	if !got.ExpiresAt.Equal(tokens.ExpiresAt) {
		t.Errorf("ExpiresAt mismatch: got %v, want %v", got.ExpiresAt, tokens.ExpiresAt)
	}
}

func TestSaveTokens_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.enc")

	tokens := types.OAuthTokens{AccessToken: "test"}
	if err := crypto.SaveTokens(path, tokens, testKey); err != nil {
		t.Fatalf("SaveTokens: %v", err)
	}

	// The .tmp file must have been cleaned up by the rename.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file must not exist after successful SaveTokens")
	}
}

func TestSaveTokens_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	// Use a nested path that doesn't exist yet.
	path := filepath.Join(dir, "nested", "dir", "tokens.enc")

	tokens := types.OAuthTokens{AccessToken: "test"}
	if err := crypto.SaveTokens(path, tokens, testKey); err != nil {
		t.Fatalf("SaveTokens should create parent dirs: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("token file not found after save: %v", err)
	}
}

func TestLoadTokens_FileNotFound(t *testing.T) {
	_, err := crypto.LoadTokens("/nonexistent/tokens.enc", testKey)
	if err == nil {
		t.Fatal("LoadTokens on missing file must return an error")
	}
}

func TestLoadTokens_WrongKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.enc")

	tokens := types.OAuthTokens{AccessToken: "test"}
	if err := crypto.SaveTokens(path, tokens, testKey); err != nil {
		t.Fatalf("SaveTokens: %v", err)
	}

	_, err := crypto.LoadTokens(path, wrongKey)
	if err == nil {
		t.Fatal("LoadTokens with wrong key must return an error")
	}
}
