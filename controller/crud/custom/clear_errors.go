package custom

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// ClearErrorsResponse represents the response structure
type ClearErrorsResponse struct {
	Success bool `json:"success"`
}

// ClearErrorsByID clears or resolves specific enhanced errors by their IDs
// AUTHORIZATION: Only Admin and Owner can clear/resolve errors
func (custom *AppHandler) ClearErrorsByID(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	// AUTHORIZATION CHECK: Only Admin and Owner can clear errors
	if !requestDetails.User.IsOwner && requestDetails.User.Role != models.RoleAdmin {
		custom.Logger.Warnf("CLEAR-ERRORS: Permission denied for user %s (role: %s)",
			requestDetails.User.UserID, requestDetails.User.Role)
		return ClearErrorsResponse{Success: false}, fmt.Errorf("insufficient privileges: only admin and owner can clear errors")
	}

	// Get error IDs from metadata (passed from query params)
	errorIDsParam, exists := requestDetails.Metadata["error_ids"]
	if !exists || errorIDsParam == "" {
		return ClearErrorsResponse{Success: false}, fmt.Errorf("error_ids parameter is required")
	}

	// Get mode from metadata (clear or resolve)
	mode, exists := requestDetails.Metadata["mode"]
	if !exists || mode == "" {
		mode = "clear" // default to clear
	}
	if mode != "clear" && mode != "resolve" {
		return ClearErrorsResponse{Success: false}, fmt.Errorf("mode must be 'clear' or 'resolve'")
	}

	// Split comma-separated error IDs
	errorIDs := strings.Split(errorIDsParam, ",")
	if len(errorIDs) == 0 {
		return ClearErrorsResponse{Success: false}, fmt.Errorf("no error IDs provided")
	}

	// Clean up IDs (trim spaces)
	for i := range errorIDs {
		errorIDs[i] = strings.TrimSpace(errorIDs[i])
	}

	custom.Logger.Debugf("CLEAR-ERRORS: %s error IDs: %v from project: %s", strings.ToUpper(mode), errorIDs, requestDetails.Project)

	collection := custom.Context.Client.Collection("envoys")

	filter := bson.M{
		"project":             requestDetails.Project,
		"enhanced_errors._id": bson.M{"$in": errorIDs},
	}

	var update bson.M

	if mode == "clear" {
		// Remove errors completely
		update = bson.M{
			"$pull": bson.M{
				"enhanced_errors": bson.M{
					"_id": bson.M{"$in": errorIDs},
				},
			},
		}
	} else {
		// Mark errors as resolved
		now := time.Now()
		update = bson.M{
			"$set": bson.M{
				"enhanced_errors.$[elem].status":      "resolved",
				"enhanced_errors.$[elem].resolved_at": now,
				"enhanced_errors.$[elem].resolved_by": requestDetails.User.UserName,
			},
		}
	}

	var result *mongo.UpdateResult
	var err error

	if mode == "resolve" {
		// Use arrayFilters for resolve mode
		arrayFilters := options.ArrayFilters{
			Filters: []interface{}{
				bson.M{"elem._id": bson.M{"$in": errorIDs}},
			},
		}
		result, err = collection.UpdateMany(ctx, filter, update, options.Update().SetArrayFilters(arrayFilters))
	} else {
		result, err = collection.UpdateMany(ctx, filter, update)
	}

	if err != nil {
		custom.Logger.Errorf("Failed to %s enhanced errors: %v", mode, err)
		return ClearErrorsResponse{Success: false}, fmt.Errorf("failed to %s errors: %w", mode, err)
	}

	custom.Logger.Infof("CLEAR-ERRORS: Successfully %s %d errors from %d services",
		mode, len(errorIDs), result.ModifiedCount)

	return ClearErrorsResponse{Success: true}, nil
}
