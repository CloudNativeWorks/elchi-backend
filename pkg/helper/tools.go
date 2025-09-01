package helper

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"strings"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"context"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"

	"go.mongodb.org/mongo-driver/mongo"
)

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

func ToBool(strBool string) bool {
	boolean, err := strconv.ParseBool(strBool)
	if err != nil {
		fmt.Println(err)
	}
	return boolean
}

func HashPassword(password string) string {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	if err != nil {
		log.Printf("Error hashing password: %v", err)
	}
	return string(bytes)
}

func GenerateAllTokens(email, username *string, userID string, groups *[]string, projects *[]models.CombinedProjects, baseGroup, baseProject *string, role *models.Role) (signedToken, signedRefreshToken string, err error) {
	claims := &models.SignedDetails{
		Email:       email,
		Username:    username,
		UserID:      userID,
		Groups:      RemoveDuplicates(groups),
		Projects:    projects,
		BaseGroup:   baseGroup,
		BaseProject: baseProject,
		Role:        role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour * 60)),
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
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute * 60)),
		},
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

func GenerateUniqueID(length int) string {
	const characters = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	charactersLength := big.NewInt(int64(len(characters)))
	result := make([]byte, length)

	for i := 0; i < length; i++ {
		num, err := rand.Int(rand.Reader, charactersLength)
		if err != nil {
			return ""
		}
		result[i] = characters[num.Int64()]
	}

	return string(result)
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
		if err := cursor.Close(ctx); err != nil {
			// Log error but don't panic - cursor close errors are not critical
			// This prevents the nil pointer dereference panic we saw
		}
	}
}

// HandleCursorResults safely processes cursor results
func HandleCursorResults(ctx context.Context, cursor *mongo.Cursor, results interface{}) error {
	if cursor == nil {
		return mongo.ErrNilCursor
	}
	
	defer SafeCloseCursor(ctx, cursor)
	return cursor.All(ctx, results)
}


