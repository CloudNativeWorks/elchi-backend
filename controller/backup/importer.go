package backup

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Importer handles backup import/restore operations
type Importer struct {
	Context *db.AppContext
	Logger  *logger.Logger
}

// NewImporter creates a new backup importer
func NewImporter(context *db.AppContext, logger *logger.Logger) *Importer {
	return &Importer{
		Context: context,
		Logger:  logger,
	}
}

// Import restores data from backup
func (i *Importer) Import(ctx context.Context, backup *BackupData, username string, dryRun bool) (*ImportResponse, error) {
	i.Logger.Infof("📥 Starting backup import - backup_id=%s, dry_run=%v, user=%s",
		backup.Metadata.BackupID, dryRun, username)

	response := &ImportResponse{
		Success:    true,
		DryRun:     dryRun,
		Summary:    ImportSummary{},
		Details:    make(map[string]CollectionImportDetail),
		Errors:     []string{},
		ImportedBy: username,
		ImportedAt: time.Now(),
	}

	// Get ordered collections for restore
	orderedCollections := i.getOrderedCollections(backup)

	// Group collections by phase (category) for parallel processing
	phaseGroups := i.groupCollectionsByPhase(orderedCollections)

	// Process each phase sequentially, but collections within a phase in parallel
	for _, phaseName := range []string{"settings", "xds", "templates", "services", "acme"} {
		collections, exists := phaseGroups[phaseName]
		if !exists || len(collections) == 0 {
			continue
		}

		i.Logger.Infof("📦 Processing phase: %s (%d collections)", phaseName, len(collections))

		// Process collections in this phase in parallel
		i.importPhaseParallel(ctx, backup, collections, dryRun, response)
	}

	if response.Success {
		i.Logger.Infof("✅ Backup import completed - total=%d, created=%d, updated=%d, failed=%d",
			response.Summary.TotalResources, response.Summary.Created, response.Summary.Updated, response.Summary.Failed)
	} else {
		i.Logger.Warnf("⚠️ Backup import completed with errors - total=%d, created=%d, updated=%d, failed=%d, errors=%d",
			response.Summary.TotalResources, response.Summary.Created, response.Summary.Updated, response.Summary.Failed, len(response.Errors))
	}

	return response, nil
}

// groupCollectionsByPhase groups collections by their phase/category
func (i *Importer) groupCollectionsByPhase(collections []CollectionMetadata) map[string][]CollectionMetadata {
	phaseGroups := make(map[string][]CollectionMetadata)

	for _, coll := range collections {
		phaseGroups[coll.Category] = append(phaseGroups[coll.Category], coll)
	}

	return phaseGroups
}

// importPhaseParallel imports all collections in a phase in parallel with thread-safe response updates
func (i *Importer) importPhaseParallel(ctx context.Context, backup *BackupData, collections []CollectionMetadata, dryRun bool, response *ImportResponse) {
	var wg sync.WaitGroup
	var mu sync.Mutex // Protect response object from concurrent writes

	for _, collMeta := range collections {
		docs := i.getCollectionDocuments(backup, collMeta.Name)
		if len(docs) == 0 {
			continue // Skip empty collections
		}

		wg.Add(1)
		go func(collName string, documents []primitive.M) {
			defer wg.Done()

			i.Logger.Infof("Restoring %s (%d documents)...", collName, len(documents))

			detail, err := i.importCollection(ctx, collName, documents, dryRun)

			// Thread-safe response update
			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				errMsg := fmt.Sprintf("Failed to import %s: %v", collName, err)
				i.Logger.Errorf("%s", errMsg)
				response.Errors = append(response.Errors, errMsg)
				response.Success = false
			}

			response.Details[collName] = detail
			response.Summary.TotalResources += detail.Total
			response.Summary.Created += detail.Created
			response.Summary.Updated += detail.Updated
			response.Summary.Failed += detail.Failed
		}(collMeta.Name, docs)
	}

	wg.Wait() // Wait for all collections in this phase to complete
}

// importCollection imports a single collection with ID-based overwrite
func (i *Importer) importCollection(ctx context.Context, collectionName string, docs []primitive.M, dryRun bool) (CollectionImportDetail, error) {
	detail := CollectionImportDetail{
		Total:    len(docs),
		Created:  0,
		Updated:  0,
		Failed:   0,
		Warnings: []string{},
	}

	if dryRun {
		// In dry-run mode, check which documents exist to provide accurate preview
		collection := i.Context.Client.Collection(collectionName)

		// First pass: validate and collect all IDs
		var validIDs []primitive.ObjectID
		idToIndex := make(map[primitive.ObjectID]int)

		for idx, doc := range docs {
			id, ok := doc["_id"]
			if !ok {
				detail.Failed++
				detail.Warnings = append(detail.Warnings, "Document missing _id field")
				continue
			}

			// Convert _id from string to ObjectID if needed
			var objectID primitive.ObjectID
			switch v := id.(type) {
			case string:
				oid, err := primitive.ObjectIDFromHex(v)
				if err != nil {
					detail.Failed++
					detail.Warnings = append(detail.Warnings, fmt.Sprintf("Invalid _id format: %v", id))
					continue
				}
				objectID = oid
			case primitive.ObjectID:
				objectID = v
			default:
				detail.Failed++
				detail.Warnings = append(detail.Warnings, fmt.Sprintf("Unknown _id type: %T", id))
				continue
			}

			validIDs = append(validIDs, objectID)
			idToIndex[objectID] = idx
		}

		// Single query to check which IDs exist in database
		if len(validIDs) > 0 {
			cursor, err := collection.Find(ctx, bson.M{"_id": bson.M{"$in": validIDs}}, options.Find().SetProjection(bson.M{"_id": 1}))
			if err != nil {
				return detail, fmt.Errorf("failed to check existing documents: %w", err)
			}
			defer cursor.Close(ctx)

			// Mark existing IDs
			existingIDs := make(map[primitive.ObjectID]bool)
			for cursor.Next(ctx) {
				var result struct {
					ID primitive.ObjectID `bson:"_id"`
				}
				if err := cursor.Decode(&result); err != nil {
					continue
				}
				existingIDs[result.ID] = true
			}

			// Count created vs updated
			for _, oid := range validIDs {
				if existingIDs[oid] {
					detail.Updated++
				} else {
					detail.Created++
				}
			}
		}

		return detail, nil
	}

	collection := i.Context.Client.Collection(collectionName)

	// Prepare bulk write operations
	var operations []mongo.WriteModel

	// Pre-process documents: validate IDs and fix date fields
	for idx, doc := range docs {
		id, ok := doc["_id"]
		if !ok {
			detail.Failed++
			detail.Warnings = append(detail.Warnings, fmt.Sprintf("Document at index %d missing _id field", idx))
			continue
		}

		// Convert _id from string to ObjectID if needed
		var objectID primitive.ObjectID
		switch v := id.(type) {
		case string:
			// Backup stores _id as hex string, convert to ObjectID
			oid, err := primitive.ObjectIDFromHex(v)
			if err != nil {
				i.Logger.Errorf("Failed to convert _id %v to ObjectID: %v", id, err)
				detail.Failed++
				detail.Warnings = append(detail.Warnings, fmt.Sprintf("Invalid _id format: %v", id))
				continue
			}
			objectID = oid
			// Update doc with ObjectID for proper insertion
			doc["_id"] = objectID
		case primitive.ObjectID:
			// Already ObjectID
			objectID = v
		default:
			i.Logger.Errorf("Unknown _id type %T for value %v", id, id)
			detail.Failed++
			detail.Warnings = append(detail.Warnings, fmt.Sprintf("Unknown _id type: %T", id))
			continue
		}

		// Fix date fields that might be stored as strings
		i.fixDateFields(doc)

		// Add ReplaceOne operation with upsert=true (handles both insert and update)
		replaceModel := mongo.NewReplaceOneModel().
			SetFilter(bson.M{"_id": objectID}).
			SetReplacement(doc).
			SetUpsert(true)

		operations = append(operations, replaceModel)
	}

	// If no valid operations, return early
	if len(operations) == 0 {
		if detail.Failed > 0 {
			return detail, fmt.Errorf("all %d documents failed validation", detail.Failed)
		}
		return detail, nil
	}

	// Execute bulk write with unordered mode (continue on errors)
	bulkOpts := options.BulkWrite().SetOrdered(false)
	result, err := collection.BulkWrite(ctx, operations, bulkOpts)

	if err != nil {
		// Handle bulk write errors
		if bulkErr, ok := err.(mongo.BulkWriteException); ok {
			// Partial success - some operations succeeded, some failed
			detail.Failed += len(bulkErr.WriteErrors)
			for _, writeErr := range bulkErr.WriteErrors {
				detail.Warnings = append(detail.Warnings, fmt.Sprintf("BulkWrite error at index %d: %v", writeErr.Index, writeErr.Message))
			}

			// Count successes from result
			if result != nil {
				detail.Created = int(result.UpsertedCount)
				// MatchedCount includes both modified and unmodified existing documents
				detail.Updated = int(result.MatchedCount)
			}
		} else {
			// Complete failure
			i.Logger.Errorf("BulkWrite failed for %s: %v", collectionName, err)
			detail.Failed = len(operations)
			return detail, fmt.Errorf("bulk write failed: %w", err)
		}
	} else {
		// Complete success
		if result != nil {
			detail.Created = int(result.UpsertedCount)
			// MatchedCount includes both modified and unmodified existing documents
			detail.Updated = int(result.MatchedCount)
		}
	}

	// Verify counts match
	expectedTotal := detail.Created + detail.Updated + detail.Failed
	if expectedTotal != detail.Total {
		i.Logger.Warnf("Count mismatch in %s: expected=%d, got=%d (created=%d, updated=%d, failed=%d)",
			collectionName, detail.Total, expectedTotal, detail.Created, detail.Updated, detail.Failed)
	}

	i.Logger.Infof("  %s: created=%d, updated=%d, failed=%d", collectionName, detail.Created, detail.Updated, detail.Failed)

	return detail, nil
}

// getOrderedCollections returns collections in dependency-aware restore order
func (i *Importer) getOrderedCollections(backup *BackupData) []CollectionMetadata {
	// Get all collections that have data
	collectionsWithData := make(map[string]bool)

	if len(backup.Settings.Projects) > 0 {
		collectionsWithData["projects"] = true
	}
	if len(backup.Settings.Users) > 0 {
		collectionsWithData["users"] = true
	}
	if len(backup.Settings.Groups) > 0 {
		collectionsWithData["groups"] = true
	}
	if len(backup.Settings.Settings) > 0 {
		collectionsWithData["settings"] = true
	}
	if len(backup.XDSResources.Secrets) > 0 {
		collectionsWithData["secrets"] = true
	}
	if len(backup.XDSResources.Endpoints) > 0 {
		collectionsWithData["endpoints"] = true
	}
	if len(backup.XDSResources.Extensions) > 0 {
		collectionsWithData["extensions"] = true
	}
	if len(backup.XDSResources.Clusters) > 0 {
		collectionsWithData["clusters"] = true
	}
	if len(backup.XDSResources.VirtualHosts) > 0 {
		collectionsWithData["virtual_hosts"] = true
	}
	if len(backup.XDSResources.Routes) > 0 {
		collectionsWithData["routes"] = true
	}
	if len(backup.XDSResources.Filters) > 0 {
		collectionsWithData["filters"] = true
	}
	if len(backup.XDSResources.Listeners) > 0 {
		collectionsWithData["listeners"] = true
	}
	if len(backup.XDSResources.Bootstrap) > 0 {
		collectionsWithData["bootstrap"] = true
	}
	if len(backup.XDSResources.TLS) > 0 {
		collectionsWithData["tls"] = true
	}
	if len(backup.Templates.ResourceTemplates) > 0 {
		collectionsWithData["resource_templates"] = true
	}
	if len(backup.Templates.Snippets) > 0 {
		collectionsWithData["snippets"] = true
	}
	if len(backup.Templates.Scenarios) > 0 {
		collectionsWithData["scenarios"] = true
	}
	if len(backup.Services.Clients) > 0 {
		collectionsWithData["clients"] = true
	}
	if len(backup.Services.Services) > 0 {
		collectionsWithData["services"] = true
	}
	if len(backup.Services.AdminPorts) > 0 {
		collectionsWithData["admin_ports"] = true
	}
	if len(backup.Services.WAF) > 0 {
		collectionsWithData["waf"] = true
	}
	if len(backup.ACME.ACMEAccounts) > 0 {
		collectionsWithData["acme_accounts"] = true
	}
	if len(backup.ACME.ACMEDNSCredentials) > 0 {
		collectionsWithData["acme_dns_credentials"] = true
	}
	if len(backup.ACME.ACMETempKeys) > 0 {
		collectionsWithData["acme_temp_keys"] = true
	}
	if len(backup.ACME.ACMECertificates) > 0 {
		collectionsWithData["acme_certificates"] = true
	}

	// Filter RestoreOrder to only include collections with data
	var orderedCollections []CollectionMetadata
	for _, meta := range RestoreOrder {
		if collectionsWithData[meta.Name] {
			orderedCollections = append(orderedCollections, meta)
		}
	}

	// Sort by restore order
	sort.Slice(orderedCollections, func(i, j int) bool {
		return orderedCollections[i].Order < orderedCollections[j].Order
	})

	return orderedCollections
}

// getCollectionDocuments retrieves documents for a specific collection from backup
func (i *Importer) getCollectionDocuments(backup *BackupData, collectionName string) []primitive.M {
	switch collectionName {
	case "projects":
		return backup.Settings.Projects
	case "users":
		return backup.Settings.Users
	case "groups":
		return backup.Settings.Groups
	case "settings":
		return backup.Settings.Settings
	case "secrets":
		return backup.XDSResources.Secrets
	case "endpoints":
		return backup.XDSResources.Endpoints
	case "extensions":
		return backup.XDSResources.Extensions
	case "clusters":
		return backup.XDSResources.Clusters
	case "virtual_hosts":
		return backup.XDSResources.VirtualHosts
	case "routes":
		return backup.XDSResources.Routes
	case "filters":
		return backup.XDSResources.Filters
	case "listeners":
		return backup.XDSResources.Listeners
	case "bootstrap":
		return backup.XDSResources.Bootstrap
	case "tls":
		return backup.XDSResources.TLS
	case "resource_templates":
		return backup.Templates.ResourceTemplates
	case "snippets":
		return backup.Templates.Snippets
	case "scenarios":
		return backup.Templates.Scenarios
	case "clients":
		return backup.Services.Clients
	case "services":
		return backup.Services.Services
	case "admin_ports":
		return backup.Services.AdminPorts
	case "waf":
		return backup.Services.WAF
	case "acme_accounts":
		return backup.ACME.ACMEAccounts
	case "acme_dns_credentials":
		return backup.ACME.ACMEDNSCredentials
	case "acme_temp_keys":
		return backup.ACME.ACMETempKeys
	case "acme_certificates":
		return backup.ACME.ACMECertificates
	default:
		return nil
	}
}

// fixDateFields converts string date fields to MongoDB DateTime
func (i *Importer) fixDateFields(doc primitive.M) {
	// Fix root-level date fields (users, projects, groups, clients, services, etc.)
	rootDateFields := []string{"created_at", "updated_at", "last_sync", "last_seen"}

	for _, field := range rootDateFields {
		if value, exists := doc[field]; exists {
			if strValue, ok := value.(string); ok {
				if parsedTime, err := time.Parse(time.RFC3339, strValue); err == nil {
					doc[field] = primitive.NewDateTimeFromTime(parsedTime)
				}
			}
		}
	}

	// Fix XDS resource date fields (general.created_at and general.updated_at)
	// Handle both primitive.M and map[string]interface{} (from JSON unmarshal)
	if generalAny, exists := doc["general"]; exists {
		// Try primitive.M first
		if general, ok := generalAny.(primitive.M); ok {
			for _, field := range []string{"created_at", "updated_at"} {
				if value, exists := general[field]; exists {
					if strValue, ok := value.(string); ok {
						if parsedTime, err := time.Parse(time.RFC3339, strValue); err == nil {
							general[field] = primitive.NewDateTimeFromTime(parsedTime)
						}
					}
				}
			}
		} else if general, ok := generalAny.(map[string]interface{}); ok {
			// Handle map[string]interface{} (from JSON)
			for _, field := range []string{"created_at", "updated_at"} {
				if value, exists := general[field]; exists {
					if strValue, ok := value.(string); ok {
						if parsedTime, err := time.Parse(time.RFC3339, strValue); err == nil {
							general[field] = primitive.NewDateTimeFromTime(parsedTime)
						}
					}
				}
			}
		}
	}
}
