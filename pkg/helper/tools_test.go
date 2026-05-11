package helper

import (
	"errors"
	"strings"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func TestGenerateInternalForwardTokenRoundTrip(t *testing.T) {
	prevSecret := SecretKey
	prevAccess := AccessTokenDuration
	SecretKey = "test-secret-for-internal-forward-token"
	AccessTokenDuration = "15m" // not used here, but kept consistent
	t.Cleanup(func() {
		SecretKey = prevSecret
		AccessTokenDuration = prevAccess
	})

	role := models.RoleAdmin
	user := models.UserDetails{
		UserID:   "user-123",
		UserName: "alice",
		Role:     role,
	}

	tokenStr, err := GenerateInternalForwardToken(user)
	if err != nil {
		t.Fatalf("expected token, got error: %v", err)
	}
	if tokenStr == "" {
		t.Fatal("expected non-empty token")
	}

	claims := &models.SignedDetails{}
	parsed, err := jwt.ParseWithClaims(tokenStr, claims, func(_ *jwt.Token) (any, error) {
		return []byte(SecretKey), nil
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !parsed.Valid {
		t.Fatal("token reported invalid")
	}
	if claims.UserID != user.UserID {
		t.Errorf("user_id=%q want %q", claims.UserID, user.UserID)
	}
	if claims.Username == nil || *claims.Username != user.UserName {
		t.Errorf("username mismatch: %+v", claims.Username)
	}
	if claims.Role == nil || *claims.Role != role {
		t.Errorf("role mismatch: %+v", claims.Role)
	}
	if claims.ExpiresAt == nil {
		t.Fatal("expected ExpiresAt set")
	}
	// TTL should be ~internalForwardTokenTTL from now; allow generous slack.
	ttl := time.Until(claims.ExpiresAt.Time)
	if ttl <= 0 || ttl > internalForwardTokenTTL+5*time.Second {
		t.Errorf("ttl out of expected range: %v (cap %v)", ttl, internalForwardTokenTTL)
	}
}

func TestGenerateInternalForwardTokenMissingUserID(t *testing.T) {
	prevSecret := SecretKey
	SecretKey = "secret"
	t.Cleanup(func() { SecretKey = prevSecret })

	_, err := GenerateInternalForwardToken(models.UserDetails{})
	if err == nil {
		t.Fatal("expected error for empty UserID")
	}
	if !strings.Contains(err.Error(), "trigger user id is empty") {
		t.Errorf("unexpected error text: %v", err)
	}
}

func TestGenerateInternalForwardTokenMissingSecret(t *testing.T) {
	prevSecret := SecretKey
	SecretKey = ""
	t.Cleanup(func() { SecretKey = prevSecret })

	_, err := GenerateInternalForwardToken(models.UserDetails{UserID: "u1"})
	if err == nil {
		t.Fatal("expected error when SecretKey is empty")
	}
	if !errors.Is(err, ErrJWTSecretNotSet) {
		t.Errorf("expected ErrJWTSecretNotSet, got: %v", err)
	}
}

func TestGenerateInternalForwardTokenDefaultsRoleToViewer(t *testing.T) {
	prevSecret := SecretKey
	SecretKey = "secret"
	t.Cleanup(func() { SecretKey = prevSecret })

	tokenStr, err := GenerateInternalForwardToken(models.UserDetails{UserID: "u1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	claims := &models.SignedDetails{}
	if _, err := jwt.ParseWithClaims(tokenStr, claims, func(_ *jwt.Token) (any, error) {
		return []byte(SecretKey), nil
	}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if claims.Role == nil || *claims.Role != models.RoleViewer {
		t.Errorf("expected default role=viewer, got: %+v", claims.Role)
	}
}
