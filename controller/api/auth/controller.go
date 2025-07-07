package auth

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	jwt "github.com/golang-jwt/jwt/v5"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"golang.org/x/crypto/bcrypt"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

type AppHandler struct {
	crud.Application
	Logger *logger.Logger
}

var validate = validator.New()

func NewUserHandler(context *db.AppContext) *AppHandler {
	return &AppHandler{
		Application: crud.Application{
			Context: context,
		},
		Logger: logger.NewLogger("controller/auth"),
	}
}

func VerifyPassword(userHashedPassword, providedPassword string) (bool, string) {
	err := bcrypt.CompareHashAndPassword([]byte(userHashedPassword), []byte(providedPassword))
	check := true
	msg := ""

	if err != nil {
		msg = "username or password is incorrect"
		check = false
	}

	return check, msg
}

func ValidateToken(signedToken string) (claims *models.SignedDetails, msg string) {
	token, err := jwt.ParseWithClaims(
		signedToken,
		&models.SignedDetails{},
		func(_ *jwt.Token) (any, error) {
			return []byte(helper.SecretKey), nil
		},
	)
	if err != nil {
		msg = err.Error()
		return
	}

	claims, ok := token.Claims.(*models.SignedDetails)
	if !ok {
		msg = "the token is invalid"
		return
	}

	if claims.ExpiresAt.Unix() < time.Now().Local().Unix() {
		msg = "token is expired"
		return
	}

	return claims, msg
}

func UpdateAllTokens(handler *AppHandler, signedToken, signedRefreshToken, userID string) {
	var userCollection *mongo.Collection = handler.Context.Client.Collection("users")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	var updateObj primitive.D

	updateObj = append(updateObj, bson.E{Key: "token", Value: signedToken})
	updateObj = append(updateObj, bson.E{Key: "refresh_token", Value: signedRefreshToken})
	now := time.Now()

	UpdatedAt := primitive.NewDateTimeFromTime(now)
	updateObj = append(updateObj, bson.E{Key: "updated_at", Value: UpdatedAt})

	upsert := true
	filter := bson.M{"user_id": userID}
	opt := options.UpdateOptions{
		Upsert: &upsert,
	}

	_, err := userCollection.UpdateOne(
		ctx,
		filter,
		bson.D{
			{Key: "$set", Value: updateObj},
		},
		&opt,
	)
	if err != nil {
		handler.Logger.Fatal(err)
		return
	}
}

func ValidateRefreshToken(tokenString string) (models.SignedDetails, error) {
	token, err := jwt.ParseWithClaims(
		tokenString,
		&models.SignedDetails{},
		func(token *jwt.Token) (any, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return []byte(os.Getenv(helper.SecretKey)), nil
		},
	)
	if err != nil {
		return models.SignedDetails{}, fmt.Errorf("could not parse refresh token: %w", err)
	}

	claims, ok := token.Claims.(*models.SignedDetails)
	if !ok || !token.Valid {
		return models.SignedDetails{}, errstr.ErrInvalidRefreshToken
	}

	return *claims, nil
}

func respondWithJSON(c *gin.Context, status int, msg, userOrGroupID string) {
	c.JSON(status, gin.H{"message": msg, "id": userOrGroupID})
}
