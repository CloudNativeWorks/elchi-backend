package authorization

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// ProjectValidator handles project-based authorization
type ProjectValidator struct {
	db *mongo.Database
}

// NewProjectValidator creates a new project validator instance
func NewProjectValidator(db *mongo.Database) *ProjectValidator {
	return &ProjectValidator{
		db: db,
	}
}

// ValidateProjectAccess checks if a user has access to a specific project
// Returns nil if access is granted, error otherwise
func (pv *ProjectValidator) ValidateProjectAccess(ctx context.Context, userDetails models.UserDetails, projectID string) error {
	// Empty project check
	if projectID == "" {
		return ErrEmptyProject
	}

	// ONLY Owners have unlimited access to all projects
	if userDetails.IsOwner {
		return nil
	}

	// Validate project ID format
	projectObjectID, err := primitive.ObjectIDFromHex(projectID)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidProjectID, projectID)
	}

	// Check if project exists
	projectCollection := pv.db.Collection("projects")
	var project bson.M
	err = projectCollection.FindOne(ctx, bson.M{"_id": projectObjectID}).Decode(&project)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return ErrProjectNotFound
		}
		return fmt.Errorf("failed to verify project: %w", err)
	}

	// For ALL non-owner roles (Admin, Editor, Viewer): Check project and group membership
	// Admin can be assigned to multiple projects but only manage those projects
	
	// First check direct project membership
	if err := pv.checkProjectMembership(project, userDetails.UserID); err == nil {
		return nil
	}

	// If not a direct member, check group membership
	if len(userDetails.Groups) > 0 {
		if err := pv.checkGroupMembership(ctx, projectID, userDetails.Groups); err == nil {
			return nil
		}
	}

	// If neither direct nor group membership found
	return ErrProjectAccessDenied
}

// checkProjectMembership verifies if a user is a direct member of the project
func (pv *ProjectValidator) checkProjectMembership(project bson.M, userID string) error {
	members, ok := project["members"].(primitive.A)
	if !ok {
		return ErrProjectAccessDenied
	}

	// Convert userID to ObjectID for comparison
	userObjectID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		// Try string comparison as fallback
		for _, member := range members {
			if memberStr, ok := member.(string); ok && memberStr == userID {
				return nil
			}
		}
		return ErrProjectAccessDenied
	}

	// Check if user is in project members
	for _, member := range members {
		// Handle both string and ObjectID member formats
		switch v := member.(type) {
		case string:
			if v == userID || v == userObjectID.Hex() {
				return nil
			}
		case primitive.ObjectID:
			if v == userObjectID {
				return nil
			}
		}
	}

	return ErrProjectAccessDenied
}

// checkGroupMembership verifies if user's groups have access to the project
func (pv *ProjectValidator) checkGroupMembership(ctx context.Context, projectID string, userGroups []string) error {
	if len(userGroups) == 0 {
		return ErrProjectAccessDenied
	}

	// Check if any of the user's groups belong to this project
	groupCollection := pv.db.Collection("groups")
	
	// Convert group IDs to ObjectIDs
	groupObjectIDs := make([]primitive.ObjectID, 0, len(userGroups))
	for _, groupID := range userGroups {
		if oid, err := primitive.ObjectIDFromHex(groupID); err == nil {
			groupObjectIDs = append(groupObjectIDs, oid)
		}
	}

	// Find groups that belong to this project
	filter := bson.M{
		"_id":     bson.M{"$in": groupObjectIDs},
		"project": projectID,
	}

	count, err := groupCollection.CountDocuments(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to verify group membership: %w", err)
	}

	if count > 0 {
		return nil
	}

	return ErrProjectAccessDenied
}

// GetAccessibleProjects returns list of projects the user can access
func (pv *ProjectValidator) GetAccessibleProjects(ctx context.Context, userDetails models.UserDetails) ([]string, error) {
	// ONLY Owners can access all projects
	if userDetails.IsOwner {
		return pv.getAllProjectIDs(ctx)
	}

	projectIDs := make([]string, 0)
	projectCollection := pv.db.Collection("projects")

	// For ALL non-owner roles (Admin, Editor, Viewer): Get projects where user is a direct member
	userObjectID, _ := primitive.ObjectIDFromHex(userDetails.UserID)
	directMemberFilter := bson.M{
		"members": bson.M{"$in": []any{userDetails.UserID, userObjectID}},
	}

	cursor, err := projectCollection.Find(ctx, directMemberFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to get user projects: %w", err)
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		var project bson.M
		if err := cursor.Decode(&project); err != nil {
			continue
		}
		if id, ok := project["_id"].(primitive.ObjectID); ok {
			projectIDs = append(projectIDs, id.Hex())
		}
	}

	// Also check group-based access for all non-owner roles
	if len(userDetails.Groups) > 0 {
		groupProjectIDs, err := pv.getProjectsFromGroups(ctx, userDetails.Groups)
		if err == nil {
			projectIDs = append(projectIDs, groupProjectIDs...)
		}
	}

	return deduplicateStrings(projectIDs), nil
}

// getProjectsFromGroups returns projects accessible through group membership
func (pv *ProjectValidator) getProjectsFromGroups(ctx context.Context, groupIDs []string) ([]string, error) {
	groupCollection := pv.db.Collection("groups")
	
	// Convert group IDs to ObjectIDs
	groupObjectIDs := make([]primitive.ObjectID, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		if oid, err := primitive.ObjectIDFromHex(groupID); err == nil {
			groupObjectIDs = append(groupObjectIDs, oid)
		}
	}

	filter := bson.M{"_id": bson.M{"$in": groupObjectIDs}}
	cursor, err := groupCollection.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	projectIDs := make([]string, 0)
	for cursor.Next(ctx) {
		var group bson.M
		if err := cursor.Decode(&group); err != nil {
			continue
		}
		if projectID, ok := group["project"].(string); ok && projectID != "" {
			projectIDs = append(projectIDs, projectID)
		}
	}

	return projectIDs, nil
}

// getAllProjectIDs returns all project IDs (for owners)
func (pv *ProjectValidator) getAllProjectIDs(ctx context.Context) ([]string, error) {
	projectCollection := pv.db.Collection("projects")
	cursor, err := projectCollection.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	projectIDs := make([]string, 0)
	for cursor.Next(ctx) {
		var project bson.M
		if err := cursor.Decode(&project); err != nil {
			continue
		}
		if id, ok := project["_id"].(primitive.ObjectID); ok {
			projectIDs = append(projectIDs, id.Hex())
		}
	}

	return projectIDs, nil
}

// deduplicateStrings removes duplicate strings from slice
func deduplicateStrings(strings []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(strings))
	
	for _, s := range strings {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	
	return result
}