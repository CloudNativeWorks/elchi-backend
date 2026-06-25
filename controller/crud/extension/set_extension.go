package extension

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
	"github.com/CloudNativeWorks/elchi-backend/pkg/validation"
)

// parseDuplicateKeyError extracts a more user-friendly error message from MongoDB duplicate key errors
func parseDuplicateKeyError(err error, resourceName string) error {
	if err == nil {
		return nil
	}

	errStr := err.Error()
	if strings.Contains(errStr, "E11000 duplicate key error") {
		// Extract collection name from error message
		collection := "resource"
		if strings.Contains(errStr, "collection: elchi.") {
			parts := strings.Split(errStr, "collection: elchi.")
			if len(parts) > 1 {
				collectionPart := strings.Split(parts[1], " ")[0]
				collection = collectionPart
			}
		}

		return fmt.Errorf("an %s with the name \"%s\" already exists; please choose a different name", collection, resourceName)
	}

	return err
}

func (extension *AppHandler) SetExtension(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	// Viewers reach POST through checkRole (which permits POST so read-only
	// client/op commands can run); the create path therefore must reject them
	// explicitly — mirrors DelExtension. Without this a viewer with project
	// access can create extensions despite being read-only.
	if requestDetails.User.Role == models.RoleViewer {
		return nil, errors.New("insufficient privileges: viewers cannot create resources")
	}

	// Validate Extension resource name format
	if err := validation.ValidateExtensionResource(resource); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// Auto-validate resource based on GType if validation is requested
	if validationResult, err := validation.GlobalValidatorRegistry.ValidateByGType(resource, requestDetails, extension.Logger); err != nil {
		return validationResult, err
	}

	general := resource.GetGeneral()
	resourceID := ""

	// Sync the elchi-als access_log reference with general.api_discovery
	// before PrepareResource so it is validated and indexed into
	// general.typed_config alongside operator-defined access loggers.
	applyAPIDiscoveryALS(ctx, resource, extension.Context, extension.Logger)

	// Same for the elchi-shield ext_proc http_filter and general.api_security.
	applyAPISecurityExtProc(ctx, resource, extension.Context, extension.Logger)

	err := resources.PrepareResource(resource, requestDetails, extension.Logger, extension.ResourceService)
	if err != nil {
		return nil, err
	}

	// Process WAF injection for HTTP WASM extensions
	if err := ProcessWAFInjection(ctx, resource, extension.Context.Client, extension.Logger); err != nil {
		return nil, err
	}

	collection := extension.Context.Client.Collection(general.Collection)
	inserResult, err := collection.InsertOne(ctx, resource)
	if err != nil {
		if er := new(mongo.WriteException); errors.As(err, &er) && er.WriteErrors[0].Code == 11000 {
			return nil, parseDuplicateKeyError(err, general.Name)
		}
		return nil, err
	}

	if oid, ok := inserResult.InsertedID.(primitive.ObjectID); ok {
		resource.SetID(oid)
		resourceID = oid.Hex()
	}

	data := map[string]any{"resource_id": resourceID}

	return map[string]any{"message": "Success", "data": data}, nil
}
