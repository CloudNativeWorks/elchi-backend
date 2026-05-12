package helper

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"

	"go.mongodb.org/mongo-driver/mongo"
)

// Helper functions for token expiry
func getAccessTokenExpiry() time.Time {
	duration, err := time.ParseDuration(AccessTokenDuration)
	if err != nil {
		// Fallback to default if parsing fails
		log.Printf("Error parsing access token duration '%s': %v, using default 15m", AccessTokenDuration, err)
		duration = 15 * time.Minute
	}
	return time.Now().Add(duration)
}

func getRefreshTokenExpiry() time.Time {
	duration, err := time.ParseDuration(RefreshTokenDuration)
	if err != nil {
		// Fallback to default if parsing fails
		log.Printf("Error parsing refresh token duration '%s': %v, using default 7d", RefreshTokenDuration, err)
		duration = 7 * 24 * time.Hour
	}
	return time.Now().Add(duration)
}

var Unmarshaler = protojson.UnmarshalOptions{
	AllowPartial:   true,
	DiscardUnknown: true,
}

func Contains(s []string, str string) bool {
	for _, v := range s {
		if v == str {
			return true
		}
	}
	return false
}

// PrettyPrint outputs formatted JSON for development debugging (unused in production).
func PrettyPrint(data any) {
	if data == nil {
		return
	}

	var jsonData any
	switch v := data.(type) {
	case string:
		if err := json.Unmarshal([]byte(v), &jsonData); err != nil {
			fmt.Println(v)
			return
		}
	default:
		jsonData = v
	}

	prettyJSON, err := json.MarshalIndent(jsonData, "", "    ")
	if err != nil {
		log.Printf("JSON marshaling error: %v", err)
	}

	fmt.Println(string(prettyJSON))
}

// Password policy constants
const (
	MinPasswordLength   = 12
	MinSpecialChars     = 1
	RequireUppercase    = true
	RequireLowercase    = true
	RequireNumbers      = true
	RequireSpecialChars = true
)

// ValidatePassword validates password against security policy
func ValidatePassword(password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters long", MinPasswordLength)
	}

	var hasUpper, hasLower, hasNumber, hasSpecial bool
	specialCharCount := 0

	for _, char := range password {
		switch {
		case 'A' <= char && char <= 'Z':
			hasUpper = true
		case 'a' <= char && char <= 'z':
			hasLower = true
		case '0' <= char && char <= '9':
			hasNumber = true
		case isSpecialChar(char):
			hasSpecial = true
			specialCharCount++
		}
	}

	if RequireUppercase && !hasUpper {
		return fmt.Errorf("password must contain at least one uppercase letter")
	}

	if RequireLowercase && !hasLower {
		return fmt.Errorf("password must contain at least one lowercase letter")
	}

	if RequireNumbers && !hasNumber {
		return fmt.Errorf("password must contain at least one number")
	}

	if RequireSpecialChars && (!hasSpecial || specialCharCount < MinSpecialChars) {
		return fmt.Errorf("password must contain at least %d special character(s)", MinSpecialChars)
	}

	// Check for common weak passwords
	if isCommonPassword(password) {
		return fmt.Errorf("password is too common, please choose a stronger password")
	}

	return nil
}

// isSpecialChar checks if character is a special character
func isSpecialChar(char rune) bool {
	specialChars := "!@#$%^&*()_+-=[]{}|;:,.<>?"
	for _, special := range specialChars {
		if char == special {
			return true
		}
	}
	return false
}

// isCommonPassword checks against common weak passwords
func isCommonPassword(password string) bool {
	commonPasswords := []string{
		"password", "123456", "123456789", "qwerty", "abc123",
		"password123", "admin", "administrator", "root", "user",
		"guest", "test", "welcome", "login",
	}

	lowerPassword := strings.ToLower(password)
	for _, common := range commonPasswords {
		if lowerPassword == common {
			return true
		}
	}
	return false
}

func HashPassword(password string) string {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	if err != nil {
		log.Printf("Error hashing password: %v", err)
		return ""
	}
	return string(bytes)
}

// HashPasswordWithValidation validates password and then hashes it
func HashPasswordWithValidation(password string) (string, error) {
	// Validate password strength first
	if err := ValidatePassword(password); err != nil {
		return "", err
	}

	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	if err != nil {
		return "", fmt.Errorf("error hashing password: %w", err)
	}
	return string(bytes), nil
}

func GenerateAllTokens(email, username *string, userID string, groups *[]string, projects *[]models.CombinedProjects, baseGroup, baseProject *string, role *models.Role, authType *string) (signedToken, signedRefreshToken string, err error) {
	claims := &models.SignedDetails{
		Email:       email,
		Username:    username,
		UserID:      userID,
		Groups:      RemoveDuplicates(groups),
		Projects:    projects,
		BaseGroup:   baseGroup,
		BaseProject: baseProject,
		Role:        role,
		AuthType:    authType,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(getAccessTokenExpiry()),
		},
	}

	refreshClaims := &models.SignedDetails{
		Email:       email,
		Username:    username,
		UserID:      userID,
		Groups:      RemoveDuplicates(groups),
		Projects:    projects,
		BaseGroup:   baseGroup,
		BaseProject: baseProject,
		Role:        role,
		AuthType:    authType,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(getRefreshTokenExpiry()),
		},
	}

	// Validate JWT secret is configured
	if SecretKey == "" {
		return "", "", fmt.Errorf("JWT secret not configured: %w", ErrJWTSecretNotSet)
	}

	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(SecretKey))
	if err != nil {
		log.Printf("Error generating token: %v", err)
		return "", "", fmt.Errorf("an error occurred while creating the token: %w", err)
	}

	refreshToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString([]byte(SecretKey))
	if err != nil {
		log.Printf("Error generating refresh token: %v", err)
		return "", "", fmt.Errorf("an error occurred while creating the refresh token: %w", err)
	}

	return token, refreshToken, err
}

// internalForwardTokenTTL bounds the lifetime of tokens issued for
// inter-controller forwarding. Forward HTTPTimeout is 25s; 60s leaves a
// comfortable margin while keeping the leak window tiny.
const internalForwardTokenTTL = 60 * time.Second

// GenerateInternalForwardToken issues a short-lived JWT used when one
// controller forwards a worker-originated command to another controller.
// The token impersonates the trigger user so the target pod's
// authentication, authorization and audit pipelines see the original
// identity — no special-case branching needed on the receiving side.
//
// Returns an error if the JWT secret is unset or the trigger user lacks
// an ID. Callers should fail the forward in that case rather than send
// an unauthenticated request.
func GenerateInternalForwardToken(user models.UserDetails) (string, error) {
	if SecretKey == "" {
		return "", ErrJWTSecretNotSet
	}
	if user.UserID == "" {
		return "", fmt.Errorf("internal forward token: trigger user id is empty")
	}

	username := user.UserName
	role := user.Role
	if role == "" {
		role = models.RoleViewer
	}

	// Carry the trigger user's project scope into the token so the target
	// pod's authorization layer sees the same projects the user had at job
	// creation time. Required for Editor/Viewer roles whose authorization
	// is project-scoped; Admin/Owner bypass project checks but populating
	// the field is harmless for them.
	projects := make([]models.CombinedProjects, 0, len(user.Projects))
	for _, pid := range user.Projects {
		projects = append(projects, models.CombinedProjects{ProjectID: pid})
	}

	// Initialise remaining pointer fields to empty (non-nil) values so the
	// target pod's GetUserDetails does not deref typed-nil pointers when
	// dereferencing BaseGroup/BaseProject/AuthType/Email.
	emptyStr := ""
	authType := "internal"
	claims := &models.SignedDetails{
		Username:    &username,
		UserID:      user.UserID,
		Role:        &role,
		Projects:    &projects,
		BaseGroup:   &emptyStr,
		BaseProject: &emptyStr,
		AuthType:    &authType,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(internalForwardTokenTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(SecretKey))
}

func RemoveDuplicates(strings *[]string) *[]string {
	if strings == nil {
		result := []string{}
		return &result
	}

	uniqueStrings := make(map[string]bool)
	result := []string{}

	for _, str := range *strings {
		if _, exists := uniqueStrings[str]; !exists {
			uniqueStrings[str] = true
			result = append(result, str)
		}
	}

	return &result
}

func MarshalJSON(data any, logger *logger.Logger) (string, error) {
	jsonString, err := json.Marshal(data)
	if err != nil {
		logger.Debugf("Error marshaling JSON: %v", err)
		return "", err
	}
	return string(jsonString), nil
}

func RemoveDuplicatesP(projects *[]models.CombinedProjects) *[]models.CombinedProjects {
	uniqueProjects := make(map[string]models.CombinedProjects)
	for _, project := range *projects {
		if _, exists := uniqueProjects[project.ProjectID]; !exists {
			uniqueProjects[project.ProjectID] = project
		}
	}

	result := make([]models.CombinedProjects, 0, len(uniqueProjects))
	for _, project := range uniqueProjects {
		result = append(result, project)
	}

	return &result
}

func MarshalUnmarshalWithType(data any, msg proto.Message) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	err = Unmarshaler.Unmarshal(jsonData, msg)
	if err != nil {
		return err
	}

	return nil
}

func ConvertToJSON(v any, logger *logger.Logger) string {
	jsonData, err := json.Marshal(v)
	if err != nil {
		logger.Infof("JSON convert err: %v", err)
	}
	return string(jsonData)
}

func EscapePointKey(key string) string {
	return strings.ReplaceAll(key, ".", `\.`)
}

// ToK8sServiceName converts a controller ID to its full Kubernetes headless service DNS name for StatefulSet
// Example: elchi-controller-foo-1 -> elchi-controller-foo-1.elchi-controller-foo-headless.elchi-stack.svc
func ToK8sServiceName(controllerID string, namespace string) string {
	// For StatefulSet pods, DNS format is: {pod-name}.{headless-service-name}.{namespace}.svc
	// Pod name: full controller ID (e.g., elchi-controller-foo-1)
	// Service name: controller ID without the last digit (e.g., elchi-controller-foo)

	podName := controllerID
	serviceName := controllerID

	// Remove last digit from service name (e.g., -0, -1, -2)
	if idx := strings.LastIndex(controllerID, "-"); idx > 0 {
		// Check if the last part is a digit (StatefulSet pod ordinal)
		lastPart := controllerID[idx+1:]
		if _, err := strconv.Atoi(lastPart); err == nil {
			serviceName = controllerID[:idx]
		}
	}

	return podName + "." + serviceName + "-headless." + namespace + ".svc"
}

// SafeCloseCursor safely closes a MongoDB cursor
func SafeCloseCursor(ctx context.Context, cursor *mongo.Cursor) {
	if cursor != nil {
		_ = cursor.Close(ctx) // Ignore error - cursor close errors are not critical
	}
}

// HandleCursorResults safely processes cursor results
func HandleCursorResults(ctx context.Context, cursor *mongo.Cursor, results any) error {
	if cursor == nil {
		return mongo.ErrNilCursor
	}

	defer SafeCloseCursor(ctx, cursor)
	return cursor.All(ctx, results)
}

// GenerateSecureRequestID generates a unique request ID using crypto/rand for security
// Format: {timestamp_nanoseconds}_{secure_random_uint32}
// This is suitable for request tracing and debugging while meeting security standards
func GenerateSecureRequestID() string {
	var randBytes [4]byte
	if _, err := rand.Read(randBytes[:]); err != nil {
		// Fallback to timestamp only if crypto/rand fails (extremely rare)
		log.Printf("Warning: Failed to generate secure random bytes for request ID: %v", err)
		return fmt.Sprintf("%d_0", time.Now().UnixNano())
	}
	randInt := binary.LittleEndian.Uint32(randBytes[:])
	return fmt.Sprintf("%d_%d", time.Now().UnixNano(), randInt)
}

// ================== GSLB Helper Functions ==================

// NormalizeFQDN normalizes FQDN for consistent hashing
// - Converts to lowercase
// - Ensures trailing dot
func NormalizeFQDN(fqdn string) string {
	normalized := strings.ToLower(strings.TrimSpace(fqdn))
	if !strings.HasSuffix(normalized, ".") {
		normalized += "."
	}
	return normalized
}

// NormalizeFQDNWithZone adds zone to FQDN if not already present
// Example: "myservice" + "atest.elchi" -> "myservice.atest.elchi."
// Example: "dedeff" + "atest.elchi" -> "dedeff.atest.elchi."
func NormalizeFQDNWithZone(fqdn, zone string) string {
	normalized := strings.ToLower(strings.TrimSpace(fqdn))
	normalizedZone := strings.ToLower(strings.TrimSpace(zone))

	// Remove trailing dot from zone for easier manipulation
	normalizedZone = strings.TrimSuffix(normalizedZone, ".")

	// Check if FQDN already contains the zone
	if strings.HasSuffix(normalized, "."+normalizedZone) || strings.HasSuffix(normalized, "."+normalizedZone+".") {
		// Already has zone, just ensure trailing dot
		if !strings.HasSuffix(normalized, ".") {
			normalized += "."
		}
		return normalized
	}

	// Remove trailing dot from FQDN for appending zone
	normalized = strings.TrimSuffix(normalized, ".")

	// Append zone and ensure trailing dot
	return normalized + "." + normalizedZone + "."
}
