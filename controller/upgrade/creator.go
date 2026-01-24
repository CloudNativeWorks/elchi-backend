package upgrade

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// ResourceCreator creates resources for upgrades
type ResourceCreator struct {
	dbContext *db.AppContext
	logger    *logger.Logger
}

// NewResourceCreator creates a new resource creator
func NewResourceCreator(dbContext *db.AppContext) *ResourceCreator {
	return &ResourceCreator{
		dbContext: dbContext,
		logger:    logger.NewLogger("controller/upgrade/creator"),
	}
}

// CreateMissingDependencies creates all missing dependencies in topological order
func (c *ResourceCreator) CreateMissingDependencies(ctx context.Context, j *job.Job,
	progressCallback func(int, int),
) ([]job.ResourceRef, error) {
	missingResources := j.Metadata.UpgradeConfig.Analysis.MissingResources
	sorted := TopologicalSort(missingResources)

	if sorted == nil {
		return nil, fmt.Errorf("circular dependency detected")
	}

	createdRefs := []job.ResourceRef{}
	fromVersion := j.Metadata.SourceResource.Version
	toVersion := j.Metadata.UpgradeConfig.TargetVersion
	project := j.Metadata.SourceResource.ProjectID

	c.logger.Infof("Creating %d missing dependencies in topological order", len(sorted))

	for i, missing := range sorted {
		// Double-check existence (race condition protection)
		collection := c.dbContext.Client.Collection(missing.Collection)
		count, _ := collection.CountDocuments(ctx, bson.M{
			"general.name":    missing.Name,
			"general.project": project,
			"general.version": toVersion,
		})

		if count > 0 {
			c.logger.Infof("SKIPPED: %s/%s already exists in version %s",
				missing.Collection, missing.Name, toVersion)

			// Still add to refs for tracking, but mark as skipped
			var existingDoc models.DBResource
			if err := collection.FindOne(ctx, bson.M{
				"general.name":    missing.Name,
				"general.project": project,
				"general.version": toVersion,
			}).Decode(&existingDoc); err != nil {
				// Resource not found or decode error - continue with empty ID
				c.logger.Warnf("Could not find existing resource %s in %s: %v", missing.Name, missing.Collection, err)
			}

			createdRefs = append(createdRefs, job.ResourceRef{
				Collection: missing.Collection,
				ID:         existingDoc.ID.Hex(),
				Name:       missing.Name,
				GType:      missing.GType,
				Skipped:    true,
			})

			if progressCallback != nil {
				progressCallback(i+1, len(sorted))
			}
			continue
		}

		// Get source resource
		var sourceResource models.DBResource
		sourceCollection := c.dbContext.Client.Collection(missing.Collection)
		err := sourceCollection.FindOne(ctx, bson.M{
			"general.name":    missing.Name,
			"general.project": project,
			"general.version": fromVersion,
		}).Decode(&sourceResource)
		if err != nil {
			return createdRefs, fmt.Errorf("source not found: %s/%s: %w", missing.Collection, missing.Name, err)
		}

		// Clone and create in target version
		targetResource := c.cloneForUpgrade(sourceResource, toVersion)

		result, err := sourceCollection.InsertOne(ctx, targetResource)
		if err != nil {
			return createdRefs, fmt.Errorf("failed to create %s/%s: %w", missing.Collection, missing.Name, err)
		}

		createdRefs = append(createdRefs, job.ResourceRef{
			Collection: missing.Collection,
			ID:         result.InsertedID.(primitive.ObjectID).Hex(),
			Name:       missing.Name,
			GType:      missing.GType,
			Skipped:    false,
		})

		c.logger.Infof("Created %s/%s in version %s", missing.Collection, missing.Name, toVersion)

		if progressCallback != nil {
			progressCallback(i+1, len(sorted))
		}
	}

	return createdRefs, nil
}

// cloneForUpgrade clones a resource for upgrade to target version
func (c *ResourceCreator) cloneForUpgrade(source models.DBResource, toVersion string) models.DBResource {
	target := source

	// 1. Generate new ID
	target.ID = primitive.NewObjectID()

	// 2. Update versions
	target.General.Version = toVersion // Envoy version (v1.34.2 -> v1.36.2)
	target.Resource.Version = "1"      // RESET incremental counter to 1

	// 3. Update timestamps
	now := primitive.NewDateTimeFromTime(time.Now())
	target.General.CreatedAt = now
	target.General.UpdatedAt = now

	// 4. Clear metadata that shouldn't be copied
	target.General.Metadata = make(map[string]interface{})

	// 5. NO TypedConfig modification needed
	// - Base64 encoded values in resource.resource have NO version field
	// - They only store: name, gtype, collection, parent_name
	// - Envoy runtime fetches resources based on listener version
	// - Dependency analyzer handles referenced resources separately

	c.logger.Debugf("Cloned resource %s/%s: %s -> %s (resource.version reset to 1)",
		source.General.Collection, source.General.Name, source.General.Version, toVersion)

	return target
}

// CreateListenerInTargetVersion updates the listener's version to target version
func (c *ResourceCreator) CreateListenerInTargetVersion(ctx context.Context, j *job.Job) (job.ResourceRef, error) {
	fromVersion := j.Metadata.SourceResource.Version
	toVersion := j.Metadata.UpgradeConfig.TargetVersion
	listenerName := j.Metadata.SourceResource.Name
	project := j.Metadata.SourceResource.ProjectID

	c.logger.Infof("Upgrading listener %s from %s to %s", listenerName, fromVersion, toVersion)

	// Get existing listener
	var existingListener models.DBResource
	listenerCollection := c.dbContext.Client.Collection("listeners")
	err := listenerCollection.FindOne(ctx, bson.M{
		"general.name":    listenerName,
		"general.project": project,
	}).Decode(&existingListener)
	if err != nil {
		return job.ResourceRef{}, fmt.Errorf("listener not found: %w", err)
	}

	// Update listener version
	// Listener is an EXISTING resource, so increment resource.version instead of resetting to 1
	// Note: resource.version is a string, so we need to read, parse, increment, and write back
	currentVersion := existingListener.Resource.Version
	newResourceVersion := "1" // Default if parsing fails
	if currentVersion != "" {
		// Try to parse as integer and increment
		var currentInt int
		if _, err := fmt.Sscanf(currentVersion, "%d", &currentInt); err == nil {
			newResourceVersion = fmt.Sprintf("%d", currentInt+1)
		} else {
			c.logger.Warnf("Failed to parse resource.version '%s', using 1", currentVersion)
		}
	}

	now := primitive.NewDateTimeFromTime(time.Now())
	filter := bson.M{
		"general.name":    listenerName,
		"general.project": project,
	}

	update := bson.M{
		"$set": bson.M{
			"general.version":    toVersion, // Update to target version
			"general.updated_at": now,
			"resource.version":   newResourceVersion, // Increment existing version as string
		},
	}

	result, err := listenerCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		return job.ResourceRef{}, fmt.Errorf("failed to update listener version: %w", err)
	}

	if result.MatchedCount == 0 {
		return job.ResourceRef{}, fmt.Errorf("listener %s not found for update", listenerName)
	}

	c.logger.Infof("Upgraded listener %s to version %s", listenerName, toVersion)

	return job.ResourceRef{
		Collection: "listeners",
		ID:         existingListener.ID.Hex(),
		Name:       listenerName,
		GType:      string(models.Listener),
		Skipped:    false,
	}, nil
}

// TopologicalSort sorts resources in dependency order using Kahn's algorithm
func TopologicalSort(resources []job.MissingResource) []job.MissingResource {
	// Build adjacency map and name-to-key mapping
	graph := make(map[string][]string)
	inDegree := make(map[string]int)
	resourceMap := make(map[string]job.MissingResource)
	nameToKey := make(map[string]string) // Map resource name to "collection/name" key

	// Initialize - build maps
	for _, res := range resources {
		key := fmt.Sprintf("%s/%s", res.Collection, res.Name)
		resourceMap[key] = res
		nameToKey[res.Name] = key
		inDegree[key] = 0
	}

	// Build dependency graph - convert dependency names to keys
	for _, res := range resources {
		key := fmt.Sprintf("%s/%s", res.Collection, res.Name)
		dependencies := []string{}

		// Convert each dependency name to "collection/name" format
		for _, depName := range res.DependsOn {
			if depKey, exists := nameToKey[depName]; exists {
				dependencies = append(dependencies, depKey)
			}
			// If dependency not found in nameToKey, it's either:
			// - A listener (already excluded in analyzer)
			// - An existing resource (not in missing_resources)
			// Either way, we skip it
		}

		graph[key] = dependencies
	}

	// Calculate in-degrees
	for _, neighbors := range graph {
		for _, neighbor := range neighbors {
			inDegree[neighbor]++
		}
	}

	// Find nodes with no incoming edges
	queue := []string{}
	for key, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, key)
		}
	}

	// Kahn's algorithm
	sorted := []job.MissingResource{}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		sorted = append(sorted, resourceMap[current])

		// Reduce in-degree for neighbors
		for _, neighbor := range graph[current] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
	}

	// Check for cycles
	if len(sorted) != len(resources) {
		return nil // Circular dependency
	}

	return sorted
}
