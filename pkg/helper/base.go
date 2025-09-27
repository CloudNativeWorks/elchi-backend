package helper

import (
	"errors"
	"fmt"

	"github.com/gin-gonic/gin"
)

var (
	SecretKey            string
	AccessTokenDuration  string
	RefreshTokenDuration string
	ErrJWTSecretNotSet   = errors.New("ELCHI_JWT_SECRET is required")
	ErrJWTSecretTooShort = errors.New("JWT secret must be at least 32 characters for security")
)

const MinSecretLength = 32

// InitializeJWTConfigFromAppConfig initializes JWT config from AppConfig struct
func InitializeJWTConfigFromAppConfig(jwtSecret, accessDuration, refreshDuration string) error {
	if jwtSecret == "" {
		return ErrJWTSecretNotSet
	}

	// Validate minimum length for security
	if len(jwtSecret) < MinSecretLength {
		return fmt.Errorf("%w: current length is %d", ErrJWTSecretTooShort, len(jwtSecret))
	}

	SecretKey = jwtSecret

	// Set token durations with defaults
	if accessDuration != "" {
		AccessTokenDuration = accessDuration
	} else {
		AccessTokenDuration = "15m" // Default: 15 minutes
	}

	if refreshDuration != "" {
		RefreshTokenDuration = refreshDuration
	} else {
		RefreshTokenDuration = "7d" // Default: 7 days
	}

	return nil
}

func GetFromContext[T any](c *gin.Context, key string) (T, bool) {
	value, exists := c.Get(key)
	if !exists {
		var zero T
		return zero, false
	}

	typedValue, ok := value.(T)
	if !ok {
		var zero T
		return zero, false
	}

	return typedValue, true
}
