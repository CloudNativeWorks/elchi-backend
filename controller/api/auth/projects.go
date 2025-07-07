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

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

type ProjectWithActiveStatus struct {
	models.Project
	IsCreate bool `json:"is_create"`
}

func (handler *AppHandler) ListProjects(c *gin.Context) {
	ctx := c.Request.Context()
	UserID, _ := c.Get("user_id")
	userID, ok := UserID.(string)
	if !ok {
		userID = ""
	}
	var projectCollection *mongo.Collection = handler.Context.Client.Collection("projects")
	projects, _ := handler.GetUserProject(ctx, userID)

	var projectIDs []primitive.ObjectID
	if projects != nil {
		projectIDs = make([]primitive.ObjectID, 0, len(*projects))
		for _, projectID := range *projects {
			objID, err := primitive.ObjectIDFromHex(projectID.ProjectID)
			if err != nil {
				handler.Logger.Infof("Invalid ObjectID: %s", projectID.ProjectID)
				continue
			}
			projectIDs = append(projectIDs, objID)
		}
	}

	filter := bson.M{
		"_id": bson.M{
			"$in": projectIDs,
		},
	}

	opts := options.Find().SetProjection(bson.M{"projectname": 1, "members": 1, "created_at": 1, "updated_at": 1})
	cursor, err := projectCollection.Find(ctx, filter, opts)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "could not find records"})
	}

	var records []bson.M
	if err = cursor.All(ctx, &records); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "could not decode records"})
	}

	c.JSON(http.StatusOK, records)
}

func (handler *AppHandler) GetProject(c *gin.Context) {
	ctx := c.Request.Context()
	var userCollection *mongo.Collection = handler.Context.Client.Collection("projects")
	var record bson.M

	projectID := c.Param("project_id")
	objectID, err := primitive.ObjectIDFromHex(projectID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project_id"})
		return
	}

	filter := bson.M{"_id": objectID}
	opts := options.FindOne().SetProjection(bson.M{"projectname": 1, "email": 1, "created_at": 1, "updated_at": 1, "members": 1})
	err = userCollection.FindOne(ctx, filter, opts).Decode(&record)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "could not find records"})
	}

	c.JSON(http.StatusOK, record)
}

func (handler *AppHandler) GetBaseProjectAndRole(ctx context.Context, userID string) (*string, bool) {
	var usersCollection *mongo.Collection = handler.Context.Client.Collection("users")
	filters := bson.M{"user_id": userID}
	opts := options.Find()
	opts.SetProjection(bson.M{"base_project": 1, "username": 1, "role": 1})
	cursor, err := usersCollection.Find(ctx, filters, opts)
	if err != nil {
		handler.Logger.Info(err)
		return nil, false
	}
	defer cursor.Close(ctx)

	var result struct {
		BaseProject *string `bson:"base_project"`
		UserName    string  `bson:"username"`
		Role        string  `bson:"role"`
	}

	if cursor.Next(ctx) {
		err := cursor.Decode(&result)
		if err != nil {
			handler.Logger.Info(err)
			return nil, false
		}
		isOwner := result.Role == "owner"
		return result.BaseProject, isOwner
	}

	return nil, false
}

func (handler *AppHandler) GetUserProject(ctx context.Context, userID string) (*[]models.CombinedProjects, *string) {
	projectCollection := handler.Context.Client.Collection("projects")
	var projects []models.CombinedProjects

	baseProject, isOwner := handler.GetBaseProjectAndRole(ctx, userID)
	if baseProject != nil {
		projects = append(projects, models.CombinedProjects{
			ProjectID:   *baseProject,
			ProjectName: handler.getProjectName(ctx, projectCollection, *baseProject),
		})
	}

	if isOwner {
		allProjects, err := handler.getAllProjectNamesAndIDs(ctx, projectCollection)
		if err != nil {
			handler.Logger.Info("Error getting all project names and IDs:", err)
			return nil, baseProject
		}
		projects = append(projects, allProjects...)
		return helper.RemoveDuplicatesP(&projects), baseProject
	}

	filters := bson.M{"members": userID}
	opts := options.Find().SetProjection(bson.M{"_id": 1, "projectname": 1})
	cursor, err := projectCollection.Find(ctx, filters, opts)
	if err != nil {
		handler.Logger.Info("Error finding projects:", err)
		return nil, baseProject
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		var project models.Project
		if err := cursor.Decode(&project); err != nil {
			handler.Logger.Info("Error decoding project:", err)
			continue
		}
		projects = append(projects, models.CombinedProjects{
			ProjectID:   project.ID.Hex(),
			ProjectName: *project.ProjectName,
		})
	}

	if err := cursor.Err(); err != nil {
		handler.Logger.Info("Cursor error:", err)
	}

	return helper.RemoveDuplicatesP(&projects), baseProject
}

func (handler *AppHandler) SetUpdateProject(c *gin.Context) {
	ctx := c.Request.Context()
	var userCollection *mongo.Collection = handler.Context.Client.Collection("projects")
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	var status int
	var msg, projectID string
	defer cancel()
	var projectWA ProjectWithActiveStatus

	if err := c.BindJSON(&projectWA); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	if projectWA.IsCreate {
		if !c.GetBool("isOwner") {
			c.JSON(http.StatusUnauthorized, gin.H{"message": "You are not authorized to create a project"})
			return
		}
		status, msg, projectID = handler.CreateProject(ctx, userCollection, projectWA)
	} else {
		status, msg = handler.UpdateProject(ctx, userCollection, projectWA, c.Param("project_id"))
		projectID = c.Param("project_id")
	}

	respondWithJSON(c, status, msg, projectID)
}

func (handler *AppHandler) CreateProject(ctx context.Context, projectCollection *mongo.Collection, projectWA ProjectWithActiveStatus) (int, string, string) {
	count, err := projectCollection.CountDocuments(ctx, bson.M{"projectname": projectWA.ProjectName})
	if err != nil {
		return http.StatusBadRequest, "error occurred while checking for the projectname", "0"
	}

	if count > 0 {
		return http.StatusBadRequest, "projectname already exists", "0"
	}

	validationErr := validate.Struct(projectWA.Project)
	if validationErr != nil {
		return http.StatusBadRequest, validationErr.Error(), "0"
	}

	now := time.Now()
	projectWA.CreatedAt = primitive.NewDateTimeFromTime(now)
	projectWA.UpdatedAt = primitive.NewDateTimeFromTime(now)
	projectWA.Members = []string{}
	projectWA.ID = primitive.NewObjectID()

	insertResult, insertErr := projectCollection.InsertOne(ctx, projectWA.Project)
	if insertErr != nil {
		return http.StatusBadRequest, "Project was not created", "0"
	}

	projectID, ok := insertResult.InsertedID.(primitive.ObjectID)
	if !ok {
		return http.StatusBadRequest, "Invalid project ID", "0"
	}

	collection := handler.Context.Client.Collection("groups")
	groupResult, err := db.CreateGroup(ctx, collection, "", projectID.Hex())
	if err != nil {
		handler.Logger.Infof("Default group not created: %s", err)
	}
	groupID := groupResult.InsertedID.(primitive.ObjectID).Hex()

	for _, vers := range handler.Context.Config.ElchiVersions {
		if err := db.CreateDefaultHttpProtocolOptions(ctx, handler.Context, projectID.Hex(), vers, groupID); err != nil {
			handler.Logger.Infof("Default hpo not created for version %s: %s", vers, err)
		}

		if err := db.CreateDefaultUpstreamTLS(ctx, handler.Context, projectID.Hex(), vers, groupID); err != nil {
			handler.Logger.Infof("Default upstream tls not created for version %s: %s", vers, err)
		}

		if err := db.CreateDefaultCluster(ctx, handler.Context, projectID.Hex(), vers, groupID); err != nil {
			handler.Logger.Infof("Default cluster not created for version %s: %s", vers, err)
		}

		if err := db.CreateDefaultRouter(ctx, handler.Context, projectID.Hex(), vers, groupID); err != nil {
			handler.Logger.Infof("Default router not created for version %s: %s", vers, err)
		}

		if err := db.CreateDefaultStatSinks(ctx, handler.Context, projectID.Hex(), vers, groupID); err != nil {
			handler.Logger.Infof("Default stat sinks not created for version %s: %s", vers, err)
		}
	}

	return http.StatusOK, "Successfully created project", projectWA.ID.String()
}

func (handler *AppHandler) UpdateProject(ctx context.Context, projectCollection *mongo.Collection, projectWA ProjectWithActiveStatus, projectID string) (int, string) {
	objectID, err := primitive.ObjectIDFromHex(projectID)
	if err != nil {
		return http.StatusBadRequest, "no project found with the given project_id"
	}
	filter := bson.M{"_id": objectID}
	update := bson.M{
		"$set": bson.M{},
	}

	setMap, ok := update["$set"].(bson.M)
	if !ok {
		return http.StatusInternalServerError, "unexpected type for update['$set'], expected bson.M"
	}

	if projectWA.ProjectName != nil {
		setMap["projectname"] = projectWA.ProjectName
	}
	if projectWA.Members != nil {
		setMap["members"] = projectWA.Members
	}

	setMap["updated_at"] = primitive.NewDateTimeFromTime(time.Now())
	result, err := projectCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		return http.StatusInternalServerError, fmt.Sprintf("error updating project: %v", err)
	}

	if result.MatchedCount == 0 {
		return http.StatusBadRequest, "no project found with the given projectname"
	}

	return http.StatusOK, "project successfully updated"
}

func (handler *AppHandler) getProjectName(ctx context.Context, projectCollection *mongo.Collection, projectID string) string {
	opts := options.FindOne().SetProjection(bson.M{"projectname": 1, "_id": 0})
	objectID, err := primitive.ObjectIDFromHex(projectID)
	if err != nil {
		handler.Logger.Info(err)
		return ""
	}

	filter := bson.M{"_id": objectID}
	var result struct {
		ProjectName string `bson:"projectname"`
	}

	err = projectCollection.FindOne(ctx, filter, opts).Decode(&result)
	if err != nil {
		return ""
	}

	return result.ProjectName
}

func (handler *AppHandler) getAllProjectNamesAndIDs(ctx context.Context, projectCollection *mongo.Collection) ([]models.CombinedProjects, error) {
	opts := options.Find().SetProjection(bson.M{"_id": 1, "projectname": 1})
	var projects []models.CombinedProjects
	cursor, err := projectCollection.Find(ctx, bson.M{}, opts)
	if err != nil {
		return nil, err
	}

	for cursor.Next(ctx) {
		var result struct {
			ID          primitive.ObjectID `bson:"_id"`
			ProjectName string             `bson:"projectname"`
		}

		if err := cursor.Decode(&result); err != nil {
			return nil, err
		}

		projects = append(projects, models.CombinedProjects{ProjectID: result.ID.Hex(), ProjectName: result.ProjectName})
	}

	if err := cursor.Err(); err != nil {
		return nil, err
	}

	return projects, nil
}

func (handler *AppHandler) DeleteProject(c *gin.Context) {
	ctx := c.Request.Context()
	projectID := c.Param("project_id")

	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Project ID is required"})
		return
	}

	if !c.GetBool("isOwner") {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Project deletion requires owner privileges"})
		return
	}

	projectsCollection := handler.Context.Client.Collection("projects")
	objectID, err := primitive.ObjectIDFromHex(projectID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid project ID format"})
		return
	}

	var project models.Project
	err = projectsCollection.FindOne(ctx, bson.M{"_id": objectID}).Decode(&project)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{"message": "Project not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "Error getting project information"})
		}
		return
	}

	if project.ProjectName != nil && *project.ProjectName == "default" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Default project cannot be deleted"})
		return
	}

	resourceDependencies := checkProjectDependencies(ctx, handler.Context, projectID)

	if len(resourceDependencies.NonDefaultResources) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":      "This project cannot be deleted because it is used by non-default resources",
			"dependencies": resourceDependencies.NonDefaultResources,
		})
		return
	}

	usersCollection := handler.Context.Client.Collection("users")
	userResult, err := usersCollection.DeleteMany(ctx, bson.M{"base_project": projectID})
	if err != nil {
		handler.Logger.Errorf("Error deleting users with base_project %s: %v", projectID, err)
	} else {
		handler.Logger.Infof("Deleted %d users with base_project %s", userResult.DeletedCount, projectID)
	}

	groupsCollection := handler.Context.Client.Collection("groups")

	groupResult, err := groupsCollection.DeleteMany(ctx, bson.M{"project": projectID})
	if err != nil {
		handler.Logger.Errorf("Error deleting groups with project %s: %v", projectID, err)
	} else {
		handler.Logger.Infof("Deleted %d groups with project %s", groupResult.DeletedCount, projectID)
	}

	if len(resourceDependencies.DefaultResources) > 0 {
		handler.Logger.Infof("Deleted default resources: %v", resourceDependencies.DefaultResources)
		for _, resource := range resourceDependencies.DefaultResources {
			collection := handler.Context.Client.Collection(resource.Collection)
			_, err := collection.DeleteOne(ctx, bson.M{"general.name": resource.Name, "general.project": projectID})
			if err != nil {
				handler.Logger.Errorf("Error deleting default resource: %v", err)
			}
		}
	}

	_, err = projectsCollection.DeleteOne(ctx, bson.M{"_id": objectID})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Project could not be deleted"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Project deleted successfully"})
}

type ProjectResource struct {
	Name       string
	Collection string
	IsDefault  bool
}

type ProjectDependencies struct {
	DefaultResources    []ProjectResource
	NonDefaultResources []ProjectResource
}

func checkProjectDependencies(ctx context.Context, appCtx *db.AppContext, projectID string) ProjectDependencies {
	var result ProjectDependencies
	collections := []string{"clusters", "listeners", "routes", "endpoints", "secrets", "extensions", "filters", "bootstrap", "tls", "virtual_hosts"}

	for _, collectionName := range collections {
		collection := appCtx.Client.Collection(collectionName)
		cursor, err := collection.Find(ctx, bson.M{"general.project": projectID})
		if err != nil {
			appCtx.Logger.Errorf("Error getting %s resources: %v", collectionName, err)
			continue
		}

		var resources []struct {
			General struct {
				Name     string `bson:"name"`
				Metadata struct {
					IsDefault bool `bson:"is_default"`
				} `bson:"metadata"`
			} `bson:"general"`
		}

		if err = cursor.All(ctx, &resources); err != nil {
			appCtx.Logger.Errorf("Error getting %s resources: %v", collectionName, err)
			cursor.Close(ctx)
			continue
		}

		cursor.Close(ctx)

		for _, resource := range resources {
			projectResource := ProjectResource{
				Name:       resource.General.Name,
				Collection: collectionName,
				IsDefault:  resource.General.Metadata.IsDefault,
			}

			if projectResource.IsDefault {
				result.DefaultResources = append(result.DefaultResources, projectResource)
			} else {
				result.NonDefaultResources = append(result.NonDefaultResources, projectResource)
			}
		}
	}
	return result
}
