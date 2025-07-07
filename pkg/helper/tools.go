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
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
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
		return "", "", fmt.Errorf("token oluşturulurken hata oluştu: %w", err)
	}

	refreshToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString([]byte(SecretKey))
	if err != nil {
		log.Printf("Error generating refresh token: %v", err)
		return "", "", fmt.Errorf("refresh token oluşturulurken hata oluştu: %w", err)
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

func MarshalJSON(data any, logger *logrus.Logger) (string, error) {
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

func ConvertToJSON(v any, logger *logrus.Logger) string {
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


