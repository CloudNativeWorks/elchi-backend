package common

import (
	"context"
	"slices"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

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

	// P1-1 FIX (CORRECTED): Add user-level permissions for Editor/Viewer ONLY
	// Admin can access ALL resources in their base_project (no group check needed)
	// Owner bypasses ALL checks across all projects
	if !details.User.IsOwner && details.User.Role != models.RoleAdmin {
		// Build complete group list including base_group
		allGroups := append([]string{}, details.User.Groups...)
		if details.User.BaseGroup != "" && !slices.Contains(allGroups, details.User.BaseGroup) {
			allGroups = append(allGroups, details.User.BaseGroup)
		}

		// Build user permission filter
		userPermFilter := bson.M{
			"$or": []bson.M{
				{"general.permissions.groups": bson.M{"$in": allGroups}},
				{"general.permissions.users": details.User.UserID},
			},
		}

		// Combine with existing filters using $and to preserve both conditions
		// Final filter: (project OR is_default) AND (group_permission OR user_permission)
		finalFilter := bson.M{
			"$and": []bson.M{
				secureFilter,   // Contains project filter from EnforceProjectFilter
				userPermFilter, // Contains permission filter
			},
		}
		return finalFilter, nil
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
