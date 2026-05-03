package license

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// NewKEK derives a 32-byte AES-256 key from the controller's JWT secret.
// Using SHA-256 of the secret gives a stable key whose lifetime matches the
// controller's identity — rotating the JWT secret invalidates stored license keys
// and forces re-activation, which is the desired behavior.
func NewKEK(jwtSecret string) ([]byte, error) {
	if jwtSecret == "" {
		return nil, errors.New("jwt secret is empty; cannot derive license KEK")
	}
	sum := sha256.Sum256([]byte(jwtSecret))
	return sum[:], nil
}

// Encrypt seals plaintext with AES-256-GCM and returns base64(nonce || ciphertext).
func Encrypt(plaintext, kek []byte) (string, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt reverses Encrypt.
func Decrypt(ciphertext string, kek []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("base64: %w", err)
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce := raw[:gcm.NonceSize()]
	ct := raw[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("gcm open: %w", err)
	}
	return pt, nil
}
