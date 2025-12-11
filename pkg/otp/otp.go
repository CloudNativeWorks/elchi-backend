package otp

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
)

const (
	// OTP settings
	OTPIssuer       = "Elchi Backend"
	OTPDigits       = otp.DigitsSix
	OTPPeriod       = 30 // seconds
	BackupCodeCount = 10
	BackupCodeLen   = 8
)

// GenerateOTPSecret generates a new TOTP secret for the given email/account
func GenerateOTPSecret(email string) (*otp.Key, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      OTPIssuer,
		AccountName: email,
		Period:      OTPPeriod,
		Digits:      OTPDigits,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to generate OTP secret: %w", err)
	}
	return key, nil
}

// ValidateOTPCode validates a 6-digit OTP code against the secret
// Allows 1-step time skew (±30 seconds) for clock drift tolerance
func ValidateOTPCode(secret, code string) bool {
	valid, err := totp.ValidateCustom(
		code,
		secret,
		time.Now(),
		totp.ValidateOpts{
			Period:    OTPPeriod,
			Skew:      1, // Allow ±1 period (±30s)
			Digits:    OTPDigits,
			Algorithm: otp.AlgorithmSHA1,
		},
	)
	if err != nil {
		return false
	}
	return valid
}

// GenerateQRCode generates a QR code image (PNG) from the OTP key
// Returns base64-encoded PNG image
func GenerateQRCode(key *otp.Key) (string, error) {
	// Generate QR code as PNG bytes
	pngBytes, err := qrcode.Encode(key.URL(), qrcode.Medium, 256)
	if err != nil {
		return "", fmt.Errorf("failed to generate QR code: %w", err)
	}

	// Encode to base64 for easy frontend usage
	base64Img := base64.StdEncoding.EncodeToString(pngBytes)
	return base64Img, nil
}

// GenerateBackupCodes generates n backup codes (random alphanumeric strings)
// Returns both plaintext codes (for display) and hashed codes (for storage)
func GenerateBackupCodes(count int) (plainCodes []string, hashedCodes []string, err error) {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	plainCodes = make([]string, count)
	hashedCodes = make([]string, count)

	for i := 0; i < count; i++ {
		// Generate random code
		code := make([]byte, BackupCodeLen)
		for j := 0; j < BackupCodeLen; j++ {
			randomIndex, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
			if err != nil {
				return nil, nil, fmt.Errorf("failed to generate random code: %w", err)
			}
			code[j] = charset[randomIndex.Int64()]
		}

		plainCode := string(code)
		plainCodes[i] = plainCode

		// Hash the code for secure storage
		hashedCode, err := bcrypt.GenerateFromPassword([]byte(plainCode), bcrypt.DefaultCost)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to hash backup code: %w", err)
		}
		hashedCodes[i] = string(hashedCode)
	}

	return plainCodes, hashedCodes, nil
}

// ValidateBackupCode checks if the provided code matches any hashed backup code
// Returns (valid, usedIndex) where usedIndex is the index of the matched code (-1 if not found)
func ValidateBackupCode(hashedCodes []string, code string) (bool, int) {
	for i, hashedCode := range hashedCodes {
		err := bcrypt.CompareHashAndPassword([]byte(hashedCode), []byte(code))
		if err == nil {
			// Code matched!
			return true, i
		}
	}
	return false, -1
}

// RemoveBackupCode removes a backup code from the list by index
// Returns the updated list
func RemoveBackupCode(hashedCodes []string, index int) []string {
	if index < 0 || index >= len(hashedCodes) {
		return hashedCodes
	}
	// Remove by creating a new slice without the element at index
	return append(hashedCodes[:index], hashedCodes[index+1:]...)
}
