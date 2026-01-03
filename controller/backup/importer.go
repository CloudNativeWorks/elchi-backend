package backup

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// indexFieldMappings maps collection names and index names to human-readable field descriptions
var indexFieldMappings = map[string]map[string]string{
	"users": {
		"username_1": "username",
	},
	"groups": {
		"groupname_project_1": "group name",
	},
	"services": {
		"name_project_1": "service name",
	},
	"clusters": {
		"general_name_version_project_1": "cluster name",
	},
	"listeners": {
		"general_name_project_1": "listener name",
	},
	"routes": {
		"general_name_version_project_1": "route name",
	},
	"endpoints": {
		"general_name_version_project_1": "endpoint name",
	},
	"filters": {
		"general_name_version_project_1": "filter name",
	},
	"extensions": {
		"general_name_version_project_1": "extension name",
	},
	"secrets": {
		"general_name_version_project_1": "secret name",
	},
	"bootstrap": {
		"general_name_version_project_1": "bootstrap config name",
	},
	"tls": {
		"general_name_version_project_1": "TLS config name",
	},
	"virtual_hosts": {
		"general_name_version_project_1": "virtual host name",
	},
	"scenarios": {
		"scenario_id_1": "scenario ID",
	},
	"clients": {
		"client_id_1": "client ID",
	},
	"resource_templates": {
		"gtype_version_project_1": "template type",
	},
	"snippets": {
		"name_project_gtype_1": "snippet name",
	},
	"acme_accounts": {
		"email_ca_provider_environment_project_1": "account email/provider/environment",
	},
	"acme_certificates": {
		"secret_name_project_1": "certificate secret name",
	},
	"acme_dns_credentials": {
		"name_project_1": "DNS credential name",
	},
	"acme_temp_keys": {
		"cert_request_id_1": "certificate request ID",
	},
}

// duplicateKeyMessages provides collection-specific error messages for duplicate key conflicts
var duplicateKeyMessages = map[string]string{
	"users":               "Username already exists in database (case-insensitive). The existing user was preserved. To import this user, please rename in the backup or remove the existing user.",
	"groups":              "Group name already exists in target project (case-insensitive). The existing group was preserved. To import this group, please rename in the backup or remove the existing group.",
	"services":            "Service name already exists in target project (case-insensitive). The existing service was preserved.",
	"clusters":            "Cluster already exists in target project with same name and version (case-insensitive). The existing cluster was preserved.",
	"listeners":           "Listener name already exists in target project (case-insensitive, no version). The existing listener was preserved. Note: Listeners don't use version in unique constraint.",
	"routes":              "Route already exists in target project with same name and version (case-insensitive). The existing route was preserved.",
	"endpoints":           "Endpoint already exists in target project with same name and version (case-insensitive). The existing endpoint was preserved.",
	"filters":             "Filter already exists in target project with same name and version (case-insensitive). The existing filter was preserved.",
	"extensions":          "Extension already exists in target project with same name and version (case-insensitive). The existing extension was preserved.",
	"secrets":             "Secret already exists in target project with same name and version (case-insensitive). The existing secret was preserved.",
	"bootstrap":           "Bootstrap config already exists in target project with same name and version (case-insensitive). The existing config was preserved.",
	"tls":                 "TLS config already exists in target project with same name and version (case-insensitive). The existing config was preserved.",
	"virtual_hosts":       "Virtual host already exists in target project with same name and version (case-insensitive). The existing virtual host was preserved.",
	"scenarios":           "Scenario ID already exists. The existing scenario was preserved.",
	"clients":             "Client ID already exists (case-insensitive). The existing client was preserved.",
	"resource_templates":  "Template already exists in target project with same type and version (case-insensitive). The existing template was preserved.",
	"snippets":            "Snippet name already exists in target project for this resource type (case-insensitive). The existing snippet was preserved.",
	"acme_accounts":       "ACME account already exists with same email/provider/environment in target project. The existing account was preserved.",
	"acme_certificates":   "ACME certificate already exists with same secret name in target project. The existing certificate was preserved.",
	"acme_dns_credentials": "ACME DNS credential already exists with same name in target project. The existing credential was preserved.",
	"acme_temp_keys":      "ACME temporary key already exists with same certificate request ID. The existing key was preserved.",
}

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
func (i *Importer) Import(ctx context.Context, backup *BackupData, username string, dryRun bool, targetProjectID string) (*ImportResponse, error) {
	// STEP 1: Validate target project (REQUIRED)
	if targetProjectID == "" {
		return nil, fmt.Errorf("target_project is required for import")
	}

	if err := i.validateTargetProject(ctx, targetProjectID); err != nil {
		return nil, fmt.Errorf("target project validation failed: %w", err)
	}

	i.Logger.Infof("📥 Starting backup import - backup_id=%s, dry_run=%v, user=%s, target_project=%s",
		backup.Metadata.BackupID, dryRun, username, targetProjectID)

	// STEP 2: Skip projects collection (we don't import projects, only resources)
	if len(backup.Settings.Projects) > 0 {
		projectCount := len(backup.Settings.Projects)
		backup.Settings.Projects = nil
		i.Logger.Infof("⏭️  Skipped projects collection (%d projects) - only importing resources to target project", projectCount)
	}

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

		// Process collections in this phase in parallel (pass targetProjectID)
		i.importPhaseParallel(ctx, backup, collections, dryRun, targetProjectID, response)
	}

	if response.Success {
		i.Logger.Infof("✅ Backup import completed - total=%d, created=%d, updated=%d, skipped=%d, failed=%d",
			response.Summary.TotalResources, response.Summary.Created, response.Summary.Updated, response.Summary.Skipped, response.Summary.Failed)
	} else {
		i.Logger.Warnf("⚠️ Backup import completed with errors - total=%d, created=%d, updated=%d, skipped=%d, failed=%d, errors=%d",
			response.Summary.TotalResources, response.Summary.Created, response.Summary.Updated, response.Summary.Skipped, response.Summary.Failed, len(response.Errors))
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
func (i *Importer) importPhaseParallel(ctx context.Context, backup *BackupData, collections []CollectionMetadata, dryRun bool, targetProjectID string, response *ImportResponse) {
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

			detail, err := i.importCollection(ctx, collName, documents, dryRun, targetProjectID)

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
			response.Summary.Skipped += detail.Skipped
		}(collMeta.Name, docs)
	}

	wg.Wait() // Wait for all collections in this phase to complete
}

// importCollection imports a single collection with ID-based overwrite
func (i *Importer) importCollection(ctx context.Context, collectionName string, docs []primitive.M, dryRun bool, targetProjectID string) (CollectionImportDetail, error) {
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
			// Skip default/system resources in dry-run as well
			if i.isDefaultResource(doc) {
				detail.Skipped++
				continue
			}

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

		// STEP 0: Skip default/system resources (they already exist in target system)
		if i.isDefaultResource(doc) {
			i.Logger.Debugf("⏭️  Skipping default resource %s in %s (system resource)", objectID.Hex(), collectionName)
			detail.Skipped++
			continue
		}

		// STEP 1: Remap project IDs to target project
		if err := i.remapProjectIDs(doc, targetProjectID, collectionName); err != nil {
			i.Logger.Warnf("Failed to remap project IDs for document %s in %s: %v",
				objectID.Hex(), collectionName, err)
			detail.Failed++
			detail.Warnings = append(detail.Warnings, fmt.Sprintf("Project remapping failed for %s: %v", objectID.Hex(), err))
			continue
		}

		// STEP 2: Clear permissions (always, for security)
		if err := i.clearPermissions(doc); err != nil {
			i.Logger.Warnf("Failed to clear permissions for document %s: %v", objectID.Hex(), err)
			// Non-fatal, continue with import
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
				// Check if this is a duplicate key error
				if mongo.IsDuplicateKeyError(writeErr) {
					// Extract index name and generate user-friendly message
					indexName := i.parseDuplicateKeyError(writeErr)
					if indexName != "" {
						conflictMsg := i.formatDuplicateKeyMessage(collectionName, indexName, writeErr.Index)
						detail.Warnings = append(detail.Warnings, conflictMsg)

						// Log for debugging (includes raw error)
						i.Logger.Debugf("Duplicate key conflict in %s at index %d: index=%s, raw_error=%v",
							collectionName, writeErr.Index, indexName, writeErr.Message)
					} else {
						// Fallback if index name extraction failed
						detail.Warnings = append(detail.Warnings,
							fmt.Sprintf("Document %d: Duplicate key error. The existing resource was preserved. Raw error: %v",
								writeErr.Index, writeErr.Message))
					}
				} else {
					// Non-duplicate error - use generic handling
					detail.Warnings = append(detail.Warnings,
						fmt.Sprintf("BulkWrite error at index %d: %v", writeErr.Index, writeErr.Message))
				}
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

// validateTargetProject validates that the target project exists in the database
func (i *Importer) validateTargetProject(ctx context.Context, projectID string) error {
	projectsCollection := i.Context.Client.Collection("projects")

	// Convert string ID to ObjectID
	objectID, err := primitive.ObjectIDFromHex(projectID)
	if err != nil {
		return fmt.Errorf("invalid project ID format: %w", err)
	}

	// Check if project exists
	filter := bson.M{"_id": objectID}
	count, err := projectsCollection.CountDocuments(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to query projects collection: %w", err)
	}

	if count == 0 {
		return fmt.Errorf("target project %s does not exist - please create the project first", projectID)
	}

	i.Logger.Infof("✅ Target project validation successful: %s", projectID)
	return nil
}

// remapProjectIDs remaps project IDs in a document to the target project
func (i *Importer) remapProjectIDs(doc bson.M, targetProjectID string, collectionName string) error {
	// Find collection category from RestoreOrder
	var category string
	for _, coll := range RestoreOrder {
		if coll.Name == collectionName {
			category = coll.Category
			break
		}
	}

	// If category not found, log warning and skip (might be a new collection)
	if category == "" {
		i.Logger.Debugf("Collection %s not found in RestoreOrder, skipping project remapping", collectionName)
		return nil
	}

	remapped := false

	// Handle project remapping based on collection category
	switch category {
	case "xds":
		// XDS resources have general.project field
		if generalAny, exists := doc["general"]; exists {
			if general, ok := generalAny.(primitive.M); ok {
				if _, hasProject := general["project"]; hasProject {
					general["project"] = targetProjectID
					remapped = true
				}
			} else if general, ok := generalAny.(map[string]interface{}); ok {
				if _, hasProject := general["project"]; hasProject {
					general["project"] = targetProjectID
					remapped = true
				}
			}
		}

	case "settings":
		// Settings resources (except users) have root-level project field
		if collectionName == "users" {
			// Users have base_project field
			if _, exists := doc["base_project"]; exists {
				doc["base_project"] = targetProjectID
				remapped = true
			}
		} else {
			// Other settings resources (groups, settings)
			if _, exists := doc["project"]; exists {
				doc["project"] = targetProjectID
				remapped = true
			}
		}

	case "templates", "services", "acme":
		// Templates, services, and ACME resources have root-level project field
		if _, exists := doc["project"]; exists {
			doc["project"] = targetProjectID
			remapped = true
		}
	}

	if remapped {
		i.Logger.Debugf("🔄 Remapped project ID for %s document (category: %s) to target project: %s",
			collectionName, category, targetProjectID)
	}

	return nil
}

// clearPermissions clears all permissions from a document for security
func (i *Importer) clearPermissions(doc bson.M) error {
	// Check if document has general.permissions field
	if generalAny, exists := doc["general"]; exists {
		if general, ok := generalAny.(primitive.M); ok {
			if _, hasPermissions := general["permissions"]; hasPermissions {
				// Clear permissions - set empty arrays
				general["permissions"] = primitive.M{
					"users":  []string{},
					"groups": []string{},
				}
				i.Logger.Debugf("🔒 Cleared permissions for document (security best practice)")
				return nil
			}
		} else if general, ok := generalAny.(map[string]interface{}); ok {
			if _, hasPermissions := general["permissions"]; hasPermissions {
				// Clear permissions - set empty arrays
				general["permissions"] = map[string]interface{}{
					"users":  []string{},
					"groups": []string{},
				}
				i.Logger.Debugf("🔒 Cleared permissions for document (security best practice)")
				return nil
			}
		}
	}

	// No permissions field found - this is normal for most resources
	return nil
}

// parseDuplicateKeyError extracts index name from MongoDB duplicate key error
func (i *Importer) parseDuplicateKeyError(writeErr mongo.BulkWriteError) string {
	// MongoDB error format: "E11000 duplicate key error collection: elchi.<collection> index: <index_name> dup key: { ... }"
	errMsg := writeErr.Message

	// Extract index name from error message
	indexStart := strings.Index(errMsg, "index: ")
	if indexStart == -1 {
		i.Logger.Debugf("Could not find 'index:' in error message: %s", errMsg)
		return ""
	}

	indexStart += len("index: ")
	indexEnd := strings.Index(errMsg[indexStart:], " ")
	if indexEnd == -1 {
		// Index name is at the end of the message
		return errMsg[indexStart:]
	}

	return errMsg[indexStart : indexStart+indexEnd]
}

// getIndexFieldMapping returns human-readable field description for an index
func getIndexFieldMapping(collectionName, indexName string) string {
	if collectionMappings, exists := indexFieldMappings[collectionName]; exists {
		if fieldDesc, exists := collectionMappings[indexName]; exists {
			return fieldDesc
		}
	}

	// Fallback: try to extract field name from index name (e.g., "username_1" -> "username")
	// Remove trailing _1, _-1, etc.
	if idx := strings.LastIndex(indexName, "_"); idx > 0 {
		suffix := indexName[idx+1:]
		if suffix == "1" || suffix == "-1" {
			return strings.ReplaceAll(indexName[:idx], "_", " ")
		}
	}

	return indexName
}

// isDefaultResource checks if a document is a default/system resource
func (i *Importer) isDefaultResource(doc primitive.M) bool {
	// Check general.metadata.is_default for XDS resources
	if generalAny, exists := doc["general"]; exists {
		if general, ok := generalAny.(map[string]any); ok {
			if metadataAny, exists := general["metadata"]; exists {
				if metadata, ok := metadataAny.(map[string]any); ok {
					if isDefault, exists := metadata["is_default"]; exists {
						if defaultBool, ok := isDefault.(bool); ok && defaultBool {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// formatDuplicateKeyMessage generates user-friendly error message for duplicate key conflicts
func (i *Importer) formatDuplicateKeyMessage(collectionName, indexName string, docIndex int) string {
	// Get collection-specific message template
	if msg, exists := duplicateKeyMessages[collectionName]; exists {
		fieldDesc := getIndexFieldMapping(collectionName, indexName)
		return fmt.Sprintf("Document %d: %s (Field: %s, Index: %s)", docIndex, msg, fieldDesc, indexName)
	}

	// Fallback: Generic message
	fieldDesc := getIndexFieldMapping(collectionName, indexName)
	return fmt.Sprintf("Document %d: Duplicate %s in %s collection. The existing resource was preserved. To import, please modify the conflicting value in the backup.",
		docIndex, fieldDesc, collectionName)
}
