// Package upgrade provides Envoy version upgrade analysis and execution
// including dependency checking and resource migration.
package upgrade

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/CloudNativeWorks/elchi-backend/controller/dependency"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

// CommandHandler interface for sending commands to clients
type CommandHandler interface {
	HandleSendCommand(ctx context.Context, op models.OperationClass, requestDetails models.RequestDetails) (any, error)
}

// UpgradeAnalyzer analyzes dependencies for resource upgrades
type UpgradeAnalyzer struct {
	dbContext      *db.AppContext
	commandHandler CommandHandler
	logger         *logger.Logger
	// triggerUser is captured at the start of AnalyzeDependencies so the
	// version-check command issued during ValidateClients carries the
	// originating operator identity. Without it the cross-pod forward
	// fails with "internal forward token: trigger user id is empty"
	// because GenerateInternalForwardToken refuses an empty UserID.
	// Each upgrade gets its own analyzer instance (NewUpgradeAnalyzer is
	// called per request in handlers/upgrade.go), so per-instance state
	// is race-free.
	triggerUser *job.TriggerUser
}

// NewUpgradeAnalyzer creates a new upgrade analyzer
func NewUpgradeAnalyzer(dbContext *db.AppContext, commandHandler CommandHandler) *UpgradeAnalyzer {
	return &UpgradeAnalyzer{
		dbContext:      dbContext,
		commandHandler: commandHandler,
		logger:         logger.NewLogger("controller/upgrade/analyzer"),
	}
}

// AnalyzeDependencies analyzes all dependencies for the upgrade and determines what needs to be created
func (a *UpgradeAnalyzer) AnalyzeDependencies(ctx context.Context, j *job.Job) (*job.UpgradeAnalysisResult, error) {
	meta := j.Metadata
	fromVersion := meta.SourceResource.Version
	toVersion := meta.UpgradeConfig.TargetVersion

	// Capture the trigger user once so downstream version-check forwards
	// can mint an internal token that carries the operator identity.
	a.triggerUser = meta.TriggerUser

	// Get all listeners to upgrade
	listenerNames := meta.AffectedListeners
	if len(listenerNames) == 0 {
		return nil, fmt.Errorf("no listeners specified for upgrade")
	}

	a.logger.Infof("Starting dependency analysis for %d listeners: %s -> %s",
		len(listenerNames), fromVersion, toVersion)

	// Aggregate results from all listeners
	allMissingResources := make(map[string]job.MissingResource)  // Use map to deduplicate
	allSkippedResources := make(map[string]job.ExistingResource) // Use map to deduplicate
	allBootstrapNames := make(map[string]bool)
	listenersToUpgrade := []string{}
	listenersAlreadyExist := []string{}
	listenerDetails := []job.ListenerAnalysis{} // Per-listener analysis
	totalUpstreamDeps := 0
	totalExistingInTarget := 0
	allIncompatibleClients := []string{}
	totalClientsValidated := 0

	// Process each listener
	for _, listenerName := range listenerNames {
		a.logger.Infof("Analyzing listener: %s", listenerName)

		// Initialize listener-specific tracking
		listenerMissing := []job.MissingResource{}
		listenerExisting := []job.ExistingResource{}

		// Check if listener exists in target version
		listenerCollection := a.dbContext.Client.Collection("listeners")
		listenerCount, _ := listenerCollection.CountDocuments(ctx, bson.M{
			"general.name":    listenerName,
			"general.project": meta.SourceResource.ProjectID,
			"general.version": toVersion,
		})

		existsInTarget := listenerCount > 0
		if existsInTarget {
			listenersAlreadyExist = append(listenersAlreadyExist, listenerName)
			a.logger.Infof("Listener %s already exists in version %s", listenerName, toVersion)
		} else {
			listenersToUpgrade = append(listenersToUpgrade, listenerName)
			a.logger.Infof("Listener %s needs to be upgraded to version %s", listenerName, toVersion)
		}

		// Build upstream dependency graph using existing dependency handler
		depHandler := dependency.NewDependencyHandler(a.dbContext)

		sourceResource := dependency.Depend{
			Name:       listenerName,
			Gtype:      models.GType(meta.SourceResource.Type),
			Collection: models.GType(meta.SourceResource.Type).CollectionString(),
			Project:    meta.SourceResource.ProjectID,
			First:      true,
			Direction:  "upstream",
		}

		// Process resource dependencies (this resets visited maps internally)
		depHandler.ProcessResource(ctx, sourceResource, fromVersion)

		// Get dependency graph - nodes are stored in depHandler.Dependencies
		graph := depHandler.Dependencies

		// Count upstream dependencies (excluding listener itself)
		// Ensure count is never negative
		upstreamCount := len(graph.Nodes) - 1
		if upstreamCount < 0 {
			upstreamCount = 0
		}
		totalUpstreamDeps += upstreamCount

		// Iterate through graph nodes for this listener
		for _, dep := range graph.Nodes {
			nodeData := dep.Data
			gtype := models.GType(nodeData.Gtype)

			// Skip listener itself (we'll create it last)
			if gtype == models.Listener {
				continue
			}

			// Create unique key for deduplication across all listeners
			resourceKey := fmt.Sprintf("%s/%s/%s", nodeData.Category, nodeData.Label, nodeData.Gtype)

			// CRITICAL: Check if exists in target version
			collection := a.dbContext.Client.Collection(nodeData.Category)
			count, _ := collection.CountDocuments(ctx, bson.M{
				"general.name":    nodeData.Label, // Label is the resource name
				"general.project": meta.SourceResource.ProjectID,
				"general.version": toVersion,
			})

			if count > 0 {
				// Resource already exists in target version - SKIP
				existingRes := job.ExistingResource{
					GType:      nodeData.Gtype,
					Name:       nodeData.Label,
					Collection: nodeData.Category,
				}

				// Add to global dedup map if not already there
				if _, exists := allSkippedResources[resourceKey]; !exists {
					allSkippedResources[resourceKey] = existingRes
					totalExistingInTarget++
					a.logger.Infof("Resource %s/%s already exists in version %s - will be skipped",
						nodeData.Category, nodeData.Label, toVersion)
				}

				// Add to listener-specific existing resources
				listenerExisting = append(listenerExisting, existingRes)
			} else {
				// Resource missing - needs to be created (deduplicate across all listeners)
				missingRes := job.MissingResource{
					GType:      nodeData.Gtype,
					Name:       nodeData.Label,
					Collection: nodeData.Category, // Category is the collection name
					DependsOn:  a.extractDependencies(nodeData.ID, graph),
				}

				// Add to listener-specific missing resources
				listenerMissing = append(listenerMissing, missingRes)

				// Add to global dedup map
				if _, exists := allMissingResources[resourceKey]; !exists {
					allMissingResources[resourceKey] = missingRes
				}
			}
		}

		// Check bootstrap requirement for this listener
		bootstrapNames, bootstrapRequired := a.checkBootstrapRequirement(ctx,
			listenerName, meta.SourceResource.ProjectID, fromVersion)

		if bootstrapRequired {
			for _, name := range bootstrapNames {
				allBootstrapNames[name] = true
			}
		}

		// Validate clients if managed
		var validationResult *ClientValidationResult
		if meta.UpgradeConfig.ValidateClients {
			validationResult = a.validateClients(ctx,
				listenerName, meta.SourceResource.ProjectID, fromVersion, toVersion)

			totalClientsValidated += validationResult.ValidCount
			allIncompatibleClients = append(allIncompatibleClients, validationResult.IncompatibleClients...)
		} else {
			// ValidateClients is disabled — skip connectivity & target-version
			// availability checks, but STILL query services to determine if
			// the listener has clients that must be notified. Otherwise the
			// worker would take the unmanaged branch and advance services.version
			// without sending UPGRADE_LISTENER, silently diverging DB state
			// from real client binaries.
			_, totalClientsForListener, err := a.getClientIDsFromServices(ctx,
				listenerName, meta.SourceResource.ProjectID, fromVersion)
			if err != nil {
				// Fail-closed: an unreachable services collection must not be
				// downgraded to "no clients" — that would re-introduce the
				// silent drift this whole else-branch was added to close.
				// Surface as IncompatibleClients so validateJobMetadata aborts
				// the upgrade with a clear reason.
				errMsg := fmt.Sprintf("services query failed for listener %s (ValidateClients=off): %v", listenerName, err)
				a.logger.Errorf("%s", errMsg)
				validationResult = &ClientValidationResult{
					ValidCount:            0,
					IncompatibleClients:   []string{errMsg},
					RequiresClientUpgrade: true,
					ConnectedClients:      0,
					TotalClients:          0,
				}
			} else {
				validationResult = &ClientValidationResult{
					ValidCount:            0,
					IncompatibleClients:   []string{},
					RequiresClientUpgrade: totalClientsForListener > 0,
					ConnectedClients:      0,
					TotalClients:          totalClientsForListener,
				}
			}

			// IncompatibleClients must propagate to the aggregate slice so
			// validateJobMetadata sees it (true branch above does this via
			// validateClients's return).
			allIncompatibleClients = append(allIncompatibleClients, validationResult.IncompatibleClients...)
		}

		// Create listener-specific analysis entry
		listenerDetails = append(listenerDetails, job.ListenerAnalysis{
			ListenerName:          listenerName,
			ExistsInTarget:        existsInTarget,
			UpstreamDependencies:  upstreamCount,
			MissingResources:      listenerMissing,
			ExistingResources:     listenerExisting,
			BootstrapRequired:     bootstrapRequired,
			BootstrapNames:        bootstrapNames,
			RequiresClientUpgrade: validationResult.RequiresClientUpgrade,
			ConnectedClients:      validationResult.ConnectedClients,
			TotalClients:          validationResult.TotalClients,
		})
	}

	// Convert maps to slices
	missingResourcesList := make([]job.MissingResource, 0, len(allMissingResources))
	for _, res := range allMissingResources {
		missingResourcesList = append(missingResourcesList, res)
	}

	skippedResourcesList := make([]job.ExistingResource, 0, len(allSkippedResources))
	for _, res := range allSkippedResources {
		skippedResourcesList = append(skippedResourcesList, res)
	}

	bootstrapNamesList := make([]string, 0, len(allBootstrapNames))
	for name := range allBootstrapNames {
		bootstrapNamesList = append(bootstrapNamesList, name)
	}

	a.logger.Infof("Dependency analysis complete for %d listeners: %d total dependencies, %d missing, %d existing",
		len(listenerNames), totalUpstreamDeps, len(missingResourcesList), totalExistingInTarget)

	// Generate human-readable summary
	summary := a.generateSummary(
		len(listenerNames),
		len(listenersToUpgrade),
		len(listenersAlreadyExist),
		totalUpstreamDeps,
		len(missingResourcesList),
		totalExistingInTarget,
		len(allBootstrapNames),
		totalClientsValidated,
		len(allIncompatibleClients),
	)

	return &job.UpgradeAnalysisResult{
		ListenersToUpgrade:    listenersToUpgrade,
		ListenersAlreadyExist: listenersAlreadyExist,
		ListenerDetails:       listenerDetails,
		UpstreamDependencies:  totalUpstreamDeps,
		MissingInTarget:       len(missingResourcesList),
		ExistingInTarget:      totalExistingInTarget,
		MissingResources:      missingResourcesList,
		SkippedResources:      skippedResourcesList,
		BootstrapRequired:     len(allBootstrapNames) > 0,
		BootstrapNames:        bootstrapNamesList,
		ClientsValidated:      totalClientsValidated,
		IncompatibleClients:   allIncompatibleClients,
		Summary:               summary,
	}, nil
}

// extractDependencies extracts dependency names from graph edges
func (a *UpgradeAnalyzer) extractDependencies(nodeID string, graph *dependency.Graph) []string {
	dependsOn := []string{}

	// First, find the current node's name to avoid self-dependencies
	var currentNodeName string
	for _, node := range graph.Nodes {
		if node.Data.ID == nodeID {
			currentNodeName = node.Data.Label
			break
		}
	}

	// Find all edges where this node is the target
	for _, edge := range graph.Edges {
		if edge.Data.Target == nodeID {
			// Find the source node to get its name
			for _, node := range graph.Nodes {
				if node.Data.ID == edge.Data.Source {
					// Skip listeners - they are created separately and are not in missing_resources
					// Including listeners in depends_on causes circular dependency errors
					if models.GType(node.Data.Gtype) == models.Listener {
						continue
					}
					// Skip self-dependencies (when source name == current node name)
					// This happens when Cluster and Endpoint share the same name
					if node.Data.Label == currentNodeName {
						continue
					}
					// Use resource name (Label) instead of ID for dependency tracking
					dependsOn = append(dependsOn, node.Data.Label)
					break
				}
			}
		}
	}

	return dependsOn
}

// checkBootstrapRequirement checks if bootstrap update is required
func (a *UpgradeAnalyzer) checkBootstrapRequirement(ctx context.Context, listenerName, project, version string) ([]string, bool) {
	// Query bootstrap collection to find bootstraps referencing this listener
	collection := a.dbContext.Client.Collection("bootstrap")

	// Bootstrap and listener have the same name - just query by name
	filter := bson.M{
		"general.name":    listenerName, // Bootstrap name = Listener name
		"general.project": project,
		"general.version": version, // Check in source version
	}

	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		a.logger.Errorf("Error checking bootstrap requirement: %v", err)
		return nil, false
	}
	defer cursor.Close(ctx)

	var bootstraps []models.DBResource
	if err := cursor.All(ctx, &bootstraps); err != nil {
		a.logger.Errorf("Error decoding bootstraps: %v", err)
		return nil, false
	}

	if len(bootstraps) == 0 {
		return nil, false
	}

	names := make([]string, len(bootstraps))
	for i, b := range bootstraps {
		names[i] = b.General.Name
	}

	a.logger.Infof("Bootstrap update required for %d bootstraps: %v", len(bootstraps), names)
	return names, true
}

// ClientValidationResult contains results of client validation
type ClientValidationResult struct {
	ValidCount            int
	IncompatibleClients   []string
	RequiresClientUpgrade bool
	ConnectedClients      int
	TotalClients          int
}

// ClientInfo holds client information from database
type ClientInfo struct {
	ClientID string `bson:"client_id"`
	Name     string `bson:"name"`
	Version  string `bson:"version"` // Client software version
}

// emptyValidationResult returns an empty validation result
func emptyValidationResult() *ClientValidationResult {
	return &ClientValidationResult{
		ValidCount:            0,
		IncompatibleClients:   []string{},
		RequiresClientUpgrade: false,
		ConnectedClients:      0,
		TotalClients:          0,
	}
}

// validateClients validates that all clients support the target version
// This is the main orchestrator that coordinates all validation steps
func (a *UpgradeAnalyzer) validateClients(ctx context.Context, listenerName, project, fromVersion, toVersion string) *ClientValidationResult {
	// Step 1: Get client IDs from services
	clientIDs, totalClients, err := a.getClientIDsFromServices(ctx, listenerName, project, fromVersion)
	if err != nil {
		a.logger.Errorf("Failed to get client IDs: %v", err)
		return emptyValidationResult()
	}

	if totalClients == 0 {
		a.logger.Infof("No clients found for listener %s", listenerName)
		return emptyValidationResult()
	}

	// Step 2: Check connectivity
	connectedClients, err := a.getConnectedClients(ctx, clientIDs)
	if err != nil {
		a.logger.Errorf("Failed to check connectivity: %v", err)
		return &ClientValidationResult{
			ValidCount:            0,
			IncompatibleClients:   []string{fmt.Sprintf("connectivity check failed: %v", err)},
			RequiresClientUpgrade: totalClients > 0,
			ConnectedClients:      0,
			TotalClients:          totalClients,
		}
	}

	connectedCount := len(connectedClients)

	// STRICT: Require ALL clients to be connected
	if connectedCount != totalClients && totalClients > 0 {
		var errMsg string
		if connectedCount == 0 {
			errMsg = fmt.Sprintf("CRITICAL: Listener '%s' has %d deployed client(s) but NONE are connected - cannot upgrade",
				listenerName, totalClients)
		} else {
			errMsg = fmt.Sprintf("CRITICAL: Listener '%s' has %d/%d clients connected - ALL clients must be connected for upgrade",
				listenerName, connectedCount, totalClients)
		}
		a.logger.Errorf("%s", errMsg)
		return &ClientValidationResult{
			ValidCount:            0,
			IncompatibleClients:   []string{errMsg},
			RequiresClientUpgrade: true,
			ConnectedClients:      connectedCount,
			TotalClients:          totalClients,
		}
	}

	a.logger.Infof("Client connectivity: %d/%d clients connected for listener '%s'",
		connectedCount, totalClients, listenerName)

	// Step 3: Validate version availability
	validCount, incompatible := a.validateVersionAvailability(ctx, connectedClients, fromVersion, toVersion)

	return &ClientValidationResult{
		ValidCount:            validCount,
		IncompatibleClients:   incompatible,
		RequiresClientUpgrade: totalClients > 0,
		ConnectedClients:      connectedCount,
		TotalClients:          totalClients,
	}
}

// getClientIDsFromServices retrieves all client IDs from services using the listener
func (a *UpgradeAnalyzer) getClientIDsFromServices(ctx context.Context, listenerName, project, version string) ([]string, int, error) {
	serviceCollection := a.dbContext.Client.Collection("services")

	cursor, err := serviceCollection.Find(ctx, bson.M{
		"name":    listenerName,
		"project": project,
		"version": version,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to find services: %w", err)
	}
	defer cursor.Close(ctx)

	var services []struct {
		ServiceID string                  `bson:"_id"`
		Name      string                  `bson:"name"`
		Clients   []models.ServiceClients `bson:"clients"`
	}

	if err := cursor.All(ctx, &services); err != nil {
		return nil, 0, fmt.Errorf("failed to decode services: %w", err)
	}

	if len(services) == 0 {
		a.logger.Infof("No services found using listener %s", listenerName)
		return nil, 0, nil
	}

	a.logger.Infof("Found %d service(s) using listener %s", len(services), listenerName)

	// Collect unique client IDs
	clientIDsMap := make(map[string]bool)
	for _, svc := range services {
		for _, client := range svc.Clients {
			clientIDsMap[client.ClientID] = true
		}
	}

	clientIDs := make([]string, 0, len(clientIDsMap))
	for clientID := range clientIDsMap {
		clientIDs = append(clientIDs, clientID)
	}

	return clientIDs, len(clientIDs), nil
}

// getConnectedClients returns list of connected clients from given client IDs
func (a *UpgradeAnalyzer) getConnectedClients(ctx context.Context, clientIDs []string) ([]ClientInfo, error) {
	if len(clientIDs) == 0 {
		return []ClientInfo{}, nil
	}

	clientCollection := a.dbContext.Client.Collection("clients")
	cursor, err := clientCollection.Find(ctx, bson.M{
		"client_id": bson.M{"$in": clientIDs},
		"connected": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query connected clients: %w", err)
	}
	defer cursor.Close(ctx)

	var connectedClients []ClientInfo
	if err := cursor.All(ctx, &connectedClients); err != nil {
		return nil, fmt.Errorf("failed to decode connected clients: %w", err)
	}

	return connectedClients, nil
}

// validateVersionAvailability checks if target version is available on each connected client
func (a *UpgradeAnalyzer) validateVersionAvailability(ctx context.Context, clients []ClientInfo, fromVersion, toVersion string) (int, []string) {
	incompatible := []string{}
	validCount := 0

	for _, client := range clients {
		a.logger.Infof("Checking client %s (software v%s) - currently on Envoy %s, target: %s",
			client.ClientID, client.Version, fromVersion, toVersion)

		// Check if target version is available on this client
		versionAvailable, err := a.checkClientVersionAvailability(ctx, client.ClientID, "", toVersion)
		if err != nil {
			a.logger.Errorf("Failed to check version availability for client %s: %v", client.ClientID, err)
			incompatible = append(incompatible,
				fmt.Sprintf("%s (%s) - version check failed: %v",
					client.Name, client.ClientID, err))
			continue
		}

		if !versionAvailable {
			a.logger.Warnf("Client %s does not have target version %s available",
				client.ClientID, toVersion)
			incompatible = append(incompatible,
				fmt.Sprintf("%s (%s) - target version %s not available",
					client.Name, client.ClientID, toVersion))
		} else {
			a.logger.Infof("Client %s has target version %s available",
				client.ClientID, toVersion)
			validCount++
		}
	}

	if len(incompatible) > 0 {
		a.logger.Warnf("%d client(s) do not have target version available", len(incompatible))
	}

	a.logger.Infof("Client validation: %d compatible, %d incompatible out of %d connected",
		validCount, len(incompatible), len(clients))

	return validCount, incompatible
}

// generateSummary creates a human-readable summary of the analysis results
func (a *UpgradeAnalyzer) generateSummary(
	totalListeners, listenersToUpgrade, listenersAlreadyExist,
	upstreamDeps, missingResources, existingResources,
	bootstrapCount, clientsValidated, incompatibleClients int,
) string {
	summary := fmt.Sprintf("Upgrade Analysis: %d listener(s) total", totalListeners)

	if listenersAlreadyExist > 0 {
		summary += fmt.Sprintf(", %d already exist in target version", listenersAlreadyExist)
	}

	if listenersToUpgrade > 0 {
		summary += fmt.Sprintf(", %d need upgrade", listenersToUpgrade)
	}

	if upstreamDeps > 0 {
		summary += fmt.Sprintf(" | %d upstream dependencies", upstreamDeps)
	}

	if missingResources > 0 {
		summary += fmt.Sprintf(", %d need to be created", missingResources)
	}

	if existingResources > 0 {
		summary += fmt.Sprintf(", %d already exist (will skip)", existingResources)
	}

	if bootstrapCount > 0 {
		summary += fmt.Sprintf(" | %d bootstrap(s) need update", bootstrapCount)
	}

	if clientsValidated > 0 {
		summary += fmt.Sprintf(" | %d client(s) validated", clientsValidated)
		if incompatibleClients > 0 {
			summary += fmt.Sprintf(" (%d incompatible)", incompatibleClients)
		}
	}

	return summary
}

// buildAnalyzerUserDetails composes the UserDetails passed alongside
// commands the analyzer issues during validation. We mirror the worker's
// buildRequestDetails shape so internal forward tokens minted on the
// receiving side carry the same identity (id, username, role, project
// scope) as the operator that triggered the upgrade. The defensive nil
// branch returns IsOwner=true so single-pod / synthetic-test setups
// without a real trigger user remain functional.
func (a *UpgradeAnalyzer) buildAnalyzerUserDetails() models.UserDetails {
	if a.triggerUser == nil || a.triggerUser.ID == "" {
		a.logger.Warn("Analyzer trigger user is missing; falling back to system-privileged stub (multi-pod forwards may fail)")
		return models.UserDetails{IsOwner: true}
	}
	return models.UserDetails{
		IsOwner:  a.triggerUser.Role == "owner",
		Role:     models.Role(a.triggerUser.Role),
		UserID:   a.triggerUser.ID,
		UserName: a.triggerUser.Username,
		Projects: append([]string(nil), a.triggerUser.Projects...),
	}
}

// checkClientVersionAvailability checks if a client has the target Envoy version available
func (a *UpgradeAnalyzer) checkClientVersionAvailability(ctx context.Context, clientID, downstreamAddress, targetVersion string) (bool, error) {
	// If command handler is not available, skip the check
	if a.commandHandler == nil {
		a.logger.Warn("Command handler not available, skipping version check")
		return true, nil // Assume available if we can't check
	}

	// Create ENVOY_VERSION GET_VERSIONS operation
	versionCheckOp := &models.Operations{
		Type: models.CommandTypeJSON(pb.CommandType_ENVOY_VERSION),
		Clients: []models.ServiceClients{
			{
				ClientID:          clientID,
				DownstreamAddress: downstreamAddress,
			},
		},
		EnvoyVersionOp: &models.RequestEnvoyVersionJSON{
			Operation: "GET_VERSIONS",
		},
	}

	// Build request details carrying the trigger user's identity. The
	// command may be forwarded to another controller pod for clients not
	// registered locally; GenerateInternalForwardToken refuses an empty
	// UserID, so we must propagate the operator's identity captured in
	// AnalyzeDependencies. Falling back to a system-privilege stub is
	// only used when no trigger user was captured (defensive — should
	// not occur in the regular upgrade flow).
	requestDetails := models.RequestDetails{
		Version: targetVersion,
		User:    a.buildAnalyzerUserDetails(),
	}

	// Send command to client
	response, err := a.commandHandler.HandleSendCommand(ctx, versionCheckOp, requestDetails)
	if err != nil {
		return false, fmt.Errorf("failed to get versions from client: %w", err)
	}

	// Parse response to check if target version is available
	return a.parseVersionCheckResponse(response, targetVersion), nil
}

// parseVersionCheckResponse parses the version check response
func (a *UpgradeAnalyzer) parseVersionCheckResponse(response any, targetVersion string) bool {
	if response == nil {
		a.logger.Warn("Version check response is nil")
		return false
	}

	// HandleSendCommand returns []any when using executeDirectCommand
	// Extract first element if it's an array
	if arr, ok := response.([]any); ok && len(arr) > 0 {
		response = arr[0]
	}

	// Response can be a map from HandleSendCommand
	switch v := response.(type) {
	case map[string]any:
		// Check if it's a successful response
		if success, ok := v["success"].(bool); ok && !success {
			a.logger.Warnf("Version check returned unsuccessful response")
			return false
		}

		// Check for envoy_version nested structure from responser
		if envoyVersionData, ok := v["envoy_version"].(map[string]any); ok {
			// Try different type assertions for downloaded_versions
			if dv, exists := envoyVersionData["downloaded_versions"]; exists {
				switch versions := dv.(type) {
				case []any:
					for _, ver := range versions {
						if verStr, ok := ver.(string); ok && verStr == targetVersion {
							return true
						}
					}
				case []string:
					for _, ver := range versions {
						if ver == targetVersion {
							return true
						}
					}
				}
			}
			return false
		}

		// Fallback: check root level downloaded_versions (legacy format)
		if downloadedVersions, ok := v["downloaded_versions"].([]any); ok {
			for _, ver := range downloadedVersions {
				if verStr, ok := ver.(string); ok && verStr == targetVersion {
					return true
				}
			}
		}

		return false

	default:
		a.logger.Warnf("Unexpected version check response type: %T", response)
		return false
	}
}
