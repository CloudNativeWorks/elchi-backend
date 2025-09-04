package common

import (
	"context"
	"maps"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// AddUserFilter adds user-level filtering based on permissions
// DEPRECATED: This function has security issues. Use AddSecureUserFilter instead.
func AddUserFilter(details models.RequestDetails, mainFilter bson.M) bson.M {
	if mainFilter == nil {
		mainFilter = bson.M{}
	}

	userFilter := bson.M{}
	// SECURITY FIX: Only owners bypass user-level permission checks
	// Admins must also check permissions like other roles
	if !details.User.IsOwner {
		userFilter = bson.M{
			"$or": []bson.M{
				{"general.permissions.groups": bson.M{"$in": details.User.Groups}},
				{"general.permissions.users": details.User.UserID},
			},
		}
	}

	mainFilter["general.project"] = details.Project
	maps.Copy(mainFilter, userFilter)

	return mainFilter
}

// AddSecureUserFilter adds project-based filtering using the authorization package
// This is the new secure way to filter resources based on user access
func AddSecureUserFilter(ctx context.Context, db *mongo.Database, details models.RequestDetails, mainFilter bson.M) (bson.M, error) {
	if mainFilter == nil {
		mainFilter = bson.M{}
	}

	// Use the authorization package to enforce project filters
	secureFilter, err := authorization.EnforceProjectFilter(ctx, db, details.User, mainFilter, details.Project)
	if err != nil {
		return nil, err
	}

	// Add user-level permissions for non-owners/non-admins
	if !details.User.IsOwner && details.User.Role != models.RoleAdmin {
		userFilter := bson.M{
			"$or": []bson.M{
				{"general.permissions.groups": bson.M{"$in": details.User.Groups}},
				{"general.permissions.users": details.User.UserID},
			},
		}
		maps.Copy(secureFilter, userFilter)
	}

	return secureFilter, nil
}

func AddResourceIDFilter(requestDetails models.RequestDetails, mainFilter bson.M) (bson.M, error) {
	if mainFilter == nil {
		mainFilter = bson.M{}
	}

	objectID, err := primitive.ObjectIDFromHex(requestDetails.ResourceID)
	if err != nil {
		return mainFilter, err
	}

	mainFilter["_id"] = objectID
	return mainFilter, nil
}
