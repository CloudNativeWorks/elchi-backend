package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

type UserWithGroups struct {
	models.User
	Groups      []string           `json:"groups"`
	Projects    []string           `json:"projects"`
	IsCreate    bool               `json:"is_create"`
	Permissions *models.Permission `json:"permissions"`
}

func (handler *AppHandler) DemoAccount(c *gin.Context) {
	var userCollection *mongo.Collection = handler.Context.Client.Collection("users")
	var userWG UserWithGroups
	var status int
	var msg, userID string
	email := c.Param("email")

	if checkUserByEmailAndIP(userCollection, email, c.ClientIP()) {
		respondWithJSON(c, http.StatusBadRequest, "Email or clientIP already exists", "0")
		return
	}

	clientIP := c.ClientIP()
	baseProjectID := handler.GetDemoProjectID()
	baseGroupID := handler.GetDemoGroupID(baseProjectID)
	ctx := c.Request.Context()
	user := "demo" + helper.GenerateUniqueID(4)
	userWG.Username = &user
	passwd := helper.GenerateUniqueID(8)
	userWG.Password = &passwd
	userWG.Email = &email
	active := true
	userWG.Active = &active
	userWG.Projects = []string{baseProjectID}
	userWG.BaseProject = &baseProjectID
	role := models.RoleEditor
	userWG.Role = &role
	userWG.ClientIP = &clientIP
	status, msg, userID = handler.CreateUser(ctx, userCollection, userWG)

	if status == http.StatusOK {
		if baseProjectID != "" {
			var groupsCollection *mongo.Collection = handler.Context.Client.Collection("groups")
			filter := bson.M{
				"_id": baseGroupID,
			}
			update := bson.M{
				"$addToSet": bson.M{
					"members": userID,
				},
			}
			_, err := groupsCollection.UpdateOne(ctx, filter, update)
			if err != nil {
				handler.Logger.Errorf("Failed to add user to default group: %v", err)
			}
		}

		if err := SendEmail(user, passwd, email, handler.Context.Config.SMTPPassword); err != nil {
			handler.Logger.Errorf("Failed to send email: %v", err)
		}
	}

	respondWithJSON(c, status, msg, userID)
}

func (handler *AppHandler) SetUpdateUser(c *gin.Context) {
	ctx := c.Request.Context()
	var userCollection *mongo.Collection = handler.Context.Client.Collection("users")
	var status int
	var msg, userID string
	var userWG UserWithGroups

	if !handler.CheckUserProjectPermission(c) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "user does not have permission to update of user"})
		return
	}

	if err := c.BindJSON(&userWG); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	if role, exists := c.Get("role"); exists {
		if currentUserRole, ok := role.(*models.Role); ok && *currentUserRole == models.RoleAdmin {
			if userWG.Role != nil && *userWG.Role == models.RoleOwner {
				respondWithJSON(c, http.StatusForbidden, "admin users cannot create a user with the owner role", userID)
				return
			}
		}
	}

	if userWG.IsCreate {
		status, msg, userID = handler.CreateUser(ctx, userCollection, userWG)
	} else {
		status, msg = handler.UpdateUser(c, userCollection, userWG, c.Param("user_id"))
		userID = c.Param("user_id")
	}

	if userWG.Permissions != nil {
		handler.SetPermission(*userWG.Permissions, userID, "users")
	}

	respondWithJSON(c, status, msg, userID)
}

func (handler *AppHandler) CreateUser(ctx context.Context, userCollection *mongo.Collection, userWG UserWithGroups) (int, string, string) {
	count, err := userCollection.CountDocuments(ctx, bson.M{"username": userWG.Username})
	if err != nil {
		return http.StatusBadRequest, "error occurred while checking for the username", "0"
	}

	if count > 0 {
		return http.StatusBadRequest, "username already exists", "0"
	}

	validationErr := validate.Struct(userWG.User)
	if validationErr != nil {
		return http.StatusBadRequest, validationErr.Error(), "0"
	}

	password := helper.HashPassword(*userWG.Password)
	userWG.Password = &password
	now := time.Now()

	userWG.CreatedAt = primitive.NewDateTimeFromTime(now)
	userWG.UpdatedAt = primitive.NewDateTimeFromTime(now)
	userWG.ID = primitive.NewObjectID()
	userWG.UserID = userWG.ID.Hex()
	token, refreshToken, _ := helper.GenerateAllTokens(userWG.Email, userWG.Username, userWG.UserID, nil, nil, nil, nil, userWG.Role)
	userWG.Token = &token
	userWG.RefreshToken = &refreshToken

	insertOneResult, insertErr := userCollection.InsertOne(ctx, userWG.User)

	if insertErr != nil {
		return http.StatusBadRequest, "User item was not created", userWG.UserID
	}

	if userWG.Groups != nil {
		handler.Logger.Debugf("InsertedID: %v, Groups: %v", insertOneResult.InsertedID, userWG.Groups)
	}

	return http.StatusOK, "Successfully created user", userWG.UserID
}

func (handler *AppHandler) UpdateUser(c *gin.Context, userCollection *mongo.Collection, userWG UserWithGroups, userID string) (int, string) {
	ctx := c.Request.Context()

	if status, message := handler.checkUserAuthorization(c, userID); status != http.StatusOK {
		return status, message
	}

	update := bson.M{"$set": handler.buildUpdateFields(userWG)}

	filter := handler.GetProjectFiltersByUser(c, "base_project")
	filter["user_id"] = userID

	result, err := userCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		return http.StatusInternalServerError, fmt.Sprintf("error updating user: %v", err)
	}

	if result.MatchedCount == 0 {
		return http.StatusBadRequest, "no user found with the given username"
	}

	return http.StatusOK, "user successfully updated"
}

func (handler *AppHandler) checkUserAuthorization(c *gin.Context, userID string) (int, string) {
	if !handler.OwnerGuard(c, userID) {
		return http.StatusUnauthorized, errstr.ErrUserUpdatePermError.Error()
	}
	return http.StatusOK, ""
}

func (handler *AppHandler) buildUpdateFields(userWG UserWithGroups) bson.M {
	setMap := bson.M{}

	if userWG.Username != nil {
		setMap["username"] = userWG.Username
	}
	if userWG.Password != nil && *userWG.Password != "" {
		setMap["password"] = helper.HashPassword(*userWG.Password)
	}
	if userWG.Email != nil {
		setMap["email"] = userWG.Email
	}
	if userWG.Role != nil {
		setMap["role"] = userWG.Role
	}
	if userWG.BaseGroup != nil {
		setMap["base_group"] = nil
		if *userWG.BaseGroup != "xremove" {
			setMap["base_group"] = userWG.BaseGroup
		}
	}
	if userWG.BaseProject != nil {
		setMap["base_project"] = nil
		if *userWG.BaseProject != "xremove" {
			setMap["base_project"] = userWG.BaseProject
		}
	}
	if userWG.Active != nil {
		setMap["active"] = userWG.Active
	}

	setMap["updated_at"] = primitive.NewDateTimeFromTime(time.Now())
	return setMap
}

func (handler *AppHandler) ListUsers(c *gin.Context) {
	ctx := c.Request.Context()
	var userCollection *mongo.Collection = handler.Context.Client.Collection("users")

	filter := handler.GetProjectFiltersByUser(c, "base_project")

	if !handler.CheckUserProjectPermission(c) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "user does not have permission to list of users"})
		return
	}

	opts := options.Find().SetProjection(bson.M{"username": 1, "email": 1, "created_at": 1, "updated_at": 1, "user_id": 1, "groups": 1, "role": 1})
	cursor, err := userCollection.Find(ctx, filter, opts)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "could not find records"})
	}

	var records []bson.M
	if err = cursor.All(ctx, &records); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "could not decode records"})
	}

	c.JSON(http.StatusOK, records)
}

func (handler *AppHandler) GetUser(c *gin.Context) {
	ctx := c.Request.Context()
	var userCollection *mongo.Collection = handler.Context.Client.Collection("users")
	filter := handler.GetProjectFiltersByUser(c, "base_project")
	filter["user_id"] = c.Param("user_id")

	if !handler.CheckUserProjectPermission(c) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "user does not have permission to view of user"})
		return
	}

	opts := options.FindOne().SetProjection(bson.M{"username": 1, "email": 1, "created_at": 1, "updated_at": 1, "user_id": 1, "groups": 1, "role": 1, "base_group": 1, "base_project": 1, "active": 1})
	var record bson.M
	err := userCollection.FindOne(ctx, filter, opts).Decode(&record)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "could not find records"})
	}

	userID, ok := record["user_id"].(string)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "user_id is not a valid string"})
		return
	}

	groups, _, _ := handler.GetUserGroups(ctx, userID)
	projects, _ := handler.GetUserProject(ctx, userID)

	record["groups"] = groups
	record["projects"] = projects

	c.JSON(http.StatusOK, record)
}

func (handler *AppHandler) Login() gin.HandlerFunc {
	var userCollection *mongo.Collection = handler.Context.Client.Collection("users")
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		defer cancel()
		var user models.User
		var foundUser models.User

		if err := c.BindJSON(&user); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}

		err := userCollection.FindOne(ctx, bson.M{"username": user.Username}).Decode(&foundUser)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": "username or password is incorrect"})
			return
		}

		passwordIsValid, msg := VerifyPassword(*foundUser.Password, *user.Password)

		if !passwordIsValid {
			c.JSON(http.StatusBadRequest, gin.H{"message": msg})
			return
		}

		groups, baseGroup, _ := handler.GetUserGroups(ctx, foundUser.UserID)
		projects, baseProject := handler.GetUserProject(ctx, foundUser.UserID)

		token, refreshToken, _ := helper.GenerateAllTokens(foundUser.Email, foundUser.Username, foundUser.UserID, groups, projects, baseGroup, baseProject, foundUser.Role)

		foundUser.Token = &token
		foundUser.RefreshToken = &refreshToken

		UpdateAllTokens(handler, token, refreshToken, foundUser.UserID)

		c.JSON(http.StatusOK, foundUser)
	}
}

func (handler *AppHandler) Logout() gin.HandlerFunc {
	var userCollection *mongo.Collection = handler.Context.Client.Collection("users")
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		defer cancel()

		userID, ok := c.Get("user_id")
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"message": "Could not retrieve user id"})
			c.Abort()
			return
		}

		filter := bson.M{"user_id": userID}
		update := bson.M{
			"$unset": bson.M{
				"token":         "",
				"refresh_token": "",
			},
		}

		_, err := userCollection.UpdateOne(ctx, filter, update)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": "Failed to logout"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Successfully logged out"})
	}
}

func (handler *AppHandler) Refresh() gin.HandlerFunc {
	var userCollection *mongo.Collection = handler.Context.Client.Collection("users")
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		defer cancel()
		var token models.User

		if err := c.BindJSON(&token); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}

		var foundUser models.User
		err := userCollection.FindOne(ctx, bson.M{"refresh-token": token.RefreshToken}).Decode(&foundUser)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": "invalid refresh token"})
			return
		}

		groups, baseGroup, _ := handler.GetUserGroups(ctx, foundUser.UserID)
		projects, baseProject := handler.GetUserProject(ctx, foundUser.UserID)

		signedToken, signedRefreshToken, _ := helper.GenerateAllTokens(foundUser.Email, foundUser.Username, foundUser.UserID, groups, projects, baseGroup, baseProject, foundUser.Role)
		UpdateAllTokens(handler, signedToken, signedRefreshToken, foundUser.UserID)

		c.JSON(http.StatusOK, gin.H{
			"token":         signedToken,
			"refresh_token": signedRefreshToken,
		})
	}
}

func (handler *AppHandler) CheckUserProjectPermission(c *gin.Context) bool {
	ctx := c.Request.Context()
	roleAny, _ := c.Get("isOwner")
	role, _ := roleAny.(bool)
	if role {
		return true
	}

	UserID, _ := c.Get("user_id")
	userID, ok := UserID.(string)
	if !ok {
		userID = ""
	}
	projects, _ := handler.GetUserProject(ctx, userID)

	for _, project := range *projects {
		if project.ProjectID == c.Query("project") {
			return true
		}
	}

	return false
}

func (handler *AppHandler) GetProjectFiltersByUser(c *gin.Context, filterKey string) bson.M {
	ctx := c.Request.Context()
	if c.Query("isProjectPage") == "yes" {
		isOwner, ok := helper.GetFromContext[bool](c, "isOwner")
		if ok && isOwner {
			return bson.M{}
		}
	}

	projectIDStr := c.Query("project")
	if projectIDStr == "" {
		return bson.M{filterKey: ""}
	}

	projectID, err := primitive.ObjectIDFromHex(projectIDStr)
	if err != nil {
		return bson.M{filterKey: ""}
	}

	projectCollection := handler.Context.Client.Collection("projects")

	var project bson.M
	err = projectCollection.FindOne(ctx, bson.M{"_id": projectID}).Decode(&project)
	if err != nil {
		return bson.M{filterKey: projectIDStr}
	}

	members, ok := project["members"].(primitive.A)
	if !ok || len(members) == 0 {
		return bson.M{filterKey: projectIDStr}
	}

	memberIDs := make([]primitive.ObjectID, len(members))
	for i, member := range members {
		memberStr, ok := member.(string)
		if !ok {
			return bson.M{filterKey: projectIDStr}
		}

		memberID, err := primitive.ObjectIDFromHex(memberStr)
		if err != nil {
			return bson.M{filterKey: projectIDStr}
		}
		memberIDs[i] = memberID
	}

	filter := bson.M{
		"$or": []bson.M{
			{filterKey: projectIDStr},
			{"_id": bson.M{"$in": memberIDs}},
		},
	}

	return filter
}

func (handler *AppHandler) OwnerGuard(c *gin.Context, userID string) bool {
	ctx := c.Request.Context()
	var user models.User
	userCollection := handler.Context.Client.Collection("users")
	err := userCollection.FindOne(ctx, bson.M{"user_id": userID}).Decode(&user)
	if err != nil {
		return false
	}

	role, exists := c.Get("role")
	if !exists {
		return false
	}

	currentUserRole, ok := role.(*models.Role)
	if !ok {
		return false
	}

	if isSelfUpdatingAdmin(user, c) {
		return true
	}

	if isOwnerUpdatingNonOwner(*currentUserRole, user) {
		return true
	}

	if isAdminUpdatingNonOwner(*currentUserRole, user) {
		return true
	}

	return false
}

func isSelfUpdatingAdmin(user models.User, c *gin.Context) bool {
	return *user.Username == "admin" && c.GetString("user_id") == user.UserID
}

func isOwnerUpdatingNonOwner(currentUserRole models.Role, user models.User) bool {
	return currentUserRole == models.RoleOwner &&
		(*user.Role == models.RoleAdmin || *user.Role == models.RoleViewer || *user.Role == models.RoleEditor)
}

func isAdminUpdatingNonOwner(currentUserRole models.Role, user models.User) bool {
	return currentUserRole == models.RoleAdmin &&
		(*user.Role == models.RoleAdmin || *user.Role == models.RoleViewer || *user.Role == models.RoleEditor)
}

func checkUserByEmailAndIP(userCollection *mongo.Collection, email, clientIP string) bool {
	filter := bson.M{
		"$or": []bson.M{
			{"email": email},
			{"client_ip": clientIP},
		},
	}

	var user models.User
	err := userCollection.FindOne(context.TODO(), filter).Decode(&user)
	return err == nil
}

func (handler *AppHandler) GetDemoProjectID() string {
	client := handler.Context.Client.Collection("projects")
	var project bson.M
	err := client.FindOne(context.TODO(), bson.M{"projectname": "demo"}).Decode(&project)
	if err != nil {
		handler.Logger.Errorf("Failed to find demo project: %v", err)
		return ""
	}

	id, ok := project["_id"].(primitive.ObjectID)
	if !ok {
		handler.Logger.Errorf("Invalid project ID: %v", err)
		return ""
	}

	return id.Hex()
}

func (handler *AppHandler) GetDemoGroupID(projectID string) primitive.ObjectID {
	client := handler.Context.Client.Collection("groups")
	var group bson.M
	err := client.FindOne(context.TODO(), bson.M{"groupname": "default", "project": projectID}).Decode(&group)
	if err != nil {
		handler.Logger.Errorf("Failed to find default group: %v", err)
		return primitive.NilObjectID
	}

	id, ok := group["_id"].(primitive.ObjectID)
	if !ok {
		handler.Logger.Errorf("Invalid group ID: %v", err)
		return primitive.NilObjectID
	}

	return id
}

func (handler *AppHandler) DeleteUser(c *gin.Context) {
	ctx := c.Request.Context()
	userID := c.Param("user_id")

	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "User ID is required"})
		return
	}

	if !handler.CheckUserProjectPermission(c) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "User does not have permission to delete users"})
		return
	}

	usersCollection := handler.Context.Client.Collection("users")

	var user models.User
	err := usersCollection.FindOne(ctx, bson.M{"user_id": userID}).Decode(&user)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{"message": "User not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to get user information"})
		}
		return
	}

	if user.Username != nil && *user.Username == "admin" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Admin user cannot be deleted"})
		return
	}

	isDefault, err := common.IsDefaultResource(ctx, handler.Context, *user.Username, "users", "")
	if err != nil {
		handler.Logger.Errorf("An error occurred while checking if the user is default: %v", err)
	} else if isDefault {
		c.JSON(http.StatusBadRequest, gin.H{"message": "This user is a default resource and cannot be deleted"})
		return
	}

	projectsCollection := handler.Context.Client.Collection("projects")
	_, err = projectsCollection.UpdateMany(
		ctx,
		bson.M{"members": userID},
		bson.M{"$pull": bson.M{"members": userID}},
	)
	if err != nil {
		handler.Logger.Errorf("Failed to remove user from projects: %v", err)
	}

	collectionsToClean := []string{"clusters", "listeners", "routes", "endpoints", "secrets", "extensions", "filters", "bootstrap", "tls", "virtual_hosts"}
	for _, collectionName := range collectionsToClean {
		collection := handler.Context.Client.Collection(collectionName)
		_, err = collection.UpdateMany(
			ctx,
			bson.M{"general.permissions.users": userID},
			bson.M{"$pull": bson.M{"general.permissions.users": userID}},
		)
		if err != nil {
			handler.Logger.Errorf("Failed to remove user permissions from %s: %v", collectionName, err)
		}
	}

	_, err = usersCollection.DeleteOne(ctx, bson.M{"user_id": userID})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to delete user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "User successfully deleted"})
}
