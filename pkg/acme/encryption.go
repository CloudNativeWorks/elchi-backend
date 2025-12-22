package acme

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"

	"golang.org/x/crypto/pbkdf2"
)

// Encryptor handles AES-256-GCM encryption/decryption for sensitive data
type Encryptor struct {
	key []byte
}

// NewEncryptor creates a new Encryptor with key derived from secret
func NewEncryptor(secret string) (*Encryptor, error) {
	if secret == "" {
		return nil, fmt.Errorf("encryption secret cannot be empty")
	}

	// Derive 32-byte key using PBKDF2
	key := deriveKey(secret)

	return &Encryptor{
		key: key,
	}, nil
}

// deriveKey derives a 32-byte encryption key from the secret using PBKDF2
func deriveKey(secret string) []byte {
	// Use SHA-256 and 10000 iterations
	// Salt is deterministic (derived from secret) for key consistency
	salt := sha256.Sum256([]byte(secret))
	return pbkdf2.Key([]byte(secret), salt[:], 10000, 32, sha256.New)
}

// Encrypt encrypts plaintext using AES-256-GCM
// Returns base64-encoded string: base64(nonce || ciphertext || tag)
func (e *Encryptor) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", fmt.Errorf("plaintext cannot be empty")
	}

	// Create AES cipher
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	// Create GCM mode
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Generate random nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Encrypt and append tag
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)

	// Encode to base64
	encoded := base64.StdEncoding.EncodeToString(ciphertext)

	return encoded, nil
}

// Decrypt decrypts base64-encoded ciphertext using AES-256-GCM
// Expects format: base64(nonce || ciphertext || tag)
func (e *Encryptor) Decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", fmt.Errorf("encoded string cannot be empty")
	}

	// Decode from base64
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64: %w", err)
	}

	// Create AES cipher
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	// Create GCM mode
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Check minimum length
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	// Extract nonce and ciphertext
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]

	// Decrypt and verify tag
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decryption failed: %w", err)
	}

	return string(plaintext), nil
}

// EncryptEABCredentials encrypts External Account Binding credentials for storage
func (e *Encryptor) EncryptEABCredentials(keyID, hmacKey string) (*EABCredentials, error) {
	if keyID == "" || hmacKey == "" {
		return nil, fmt.Errorf("EAB key ID and HMAC key cannot be empty")
	}

	// Encrypt Key ID
	encryptedKeyID, err := e.Encrypt(keyID)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt EAB key ID: %w", err)
	}

	// Encrypt HMAC key
	encryptedHMACKey, err := e.Encrypt(hmacKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt EAB HMAC key: %w", err)
	}

	return &EABCredentials{
		KeyID:            encryptedKeyID,
		HMACKeyEncrypted: encryptedHMACKey,
	}, nil
}

// DecryptEABCredentials decrypts External Account Binding credentials
func (e *Encryptor) DecryptEABCredentials(eab *EABCredentials) (keyID, hmacKey string, err error) {
	if eab == nil {
		return "", "", fmt.Errorf("EAB credentials cannot be nil")
	}

	// Decrypt Key ID
	keyID, err = e.Decrypt(eab.KeyID)
	if err != nil {
		return "", "", fmt.Errorf("failed to decrypt EAB key ID: %w", err)
	}

	// Decrypt HMAC key
	hmacKey, err = e.Decrypt(eab.HMACKeyEncrypted)
	if err != nil {
		return "", "", fmt.Errorf("failed to decrypt EAB HMAC key: %w", err)
	}

	return keyID, hmacKey, nil
}
