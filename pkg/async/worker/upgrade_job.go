package worker

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	bridgeClient "github.com/CloudNativeWorks/elchi-backend/controller/bridge"
	"github.com/CloudNativeWorks/elchi-backend/controller/upgrade"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

// ClientRecord holds client information for upgrade operations
type ClientRecord struct {
	ClientID          string `bson:"client_id"`
	Name              string `bson:"name"`
	DownstreamAddress string `bson:"downstream_address"`
}

// processResourceUpgradeJob orchestrates the resource upgrade job execution
func (w *Worker) processResourceUpgradeJob(ctx context.Context, j *job.Job) {
	w.logger.Infof("Starting resource upgrade job %s", j.JobID)

	// Step 1: Validate job metadata
	meta, analysis, listenerNames, err := w.validateJobMetadata(j)
	if err != nil {
		if failErr := w.jobManager.FailJob(ctx, j.ID, err); failErr != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", failErr)
		}
		return
	}

	w.logger.Infof("Processing upgrade for %d listener(s)", len(listenerNames))

	// Create resource creator
	creator := upgrade.NewResourceCreator(w.dbContext)

	// Step 2: Create missing dependencies (Phase 1)
	createdResources, err := w.createMissingDependencies(ctx, j, creator, analysis)
	if err != nil {
		w.rollbackCreatedResources(ctx, createdResources)
		if failErr := w.jobManager.FailJob(ctx, j.ID, err); failErr != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", failErr)
		}
		return
	}

	// Step 3: Create listeners in target version (Phase 2)
	allCreatedResources, err := w.createListenersInTargetVersion(ctx, j, creator, meta, listenerNames, createdResources)
	if err != nil {
		w.rollbackCreatedResources(ctx, allCreatedResources)
		if failErr := w.jobManager.FailJob(ctx, j.ID, err); failErr != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", failErr)
		}
		return
	}

	// Step 4: Trigger snapshot updates for upgraded listeners (Phase 3).
	// Phase 3 is best-effort by design — failures are logged but never
	// block job completion (control-plane regenerates on demand).
	w.triggerSnapshotUpdates(ctx, meta, listenerNames)

	// Step 5: Update bootstraps and notify clients (Phase 4). Hard failures
	// here (no bootstrap advanced, no client notified, services collection
	// write failure) are real inconsistencies and must surface as FailJob
	// — otherwise the operator sees a green job over a half-done upgrade.
	if err := w.updateBootstrapsAndNotifyClients(ctx, j, meta, analysis, listenerNames); err != nil {
		w.logger.Errorf("Phase 4 (bootstrap+notify) failed: %v", err)
		// Persist whatever resources were created so the metadata stays
		// inspectable for the failed job.
		w.storeCreatedResources(ctx, j, allCreatedResources)
		if failErr := w.jobManager.FailJob(ctx, j.ID, err); failErr != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", failErr)
		}
		return
	}

	// Step 6: Store created resources and complete job
	w.storeCreatedResources(ctx, j, allCreatedResources)

	executionDetails := &job.ExecutionDetails{}
	if err := w.jobManager.CompleteJob(ctx, j.ID, executionDetails); err != nil {
		w.logger.Errorf("Failed to complete job: %v", err)
	}
	w.logger.Infof("Resource upgrade job %s completed: created %d resources (%d skipped)",
		j.JobID, countNonSkipped(allCreatedResources), countSkipped(allCreatedResources))
}

// validateJobMetadata validates job metadata and extracts required fields
func (w *Worker) validateJobMetadata(j *job.Job) (*job.JobMetadata, *job.UpgradeAnalysisResult, []string, error) {
	if j.Metadata == nil || j.Metadata.UpgradeConfig == nil {
		w.logger.Errorf("Job %s has invalid metadata", j.JobID)
		return nil, nil, nil, fmt.Errorf("invalid job metadata")
	}

	meta := j.Metadata
	upgradeConfig := meta.UpgradeConfig

	if upgradeConfig.Analysis == nil {
		w.logger.Errorf("Job %s has no analysis results", j.JobID)
		return nil, nil, nil, fmt.Errorf("missing analysis results")
	}

	listenerNames := meta.AffectedListeners
	if len(listenerNames) == 0 {
		w.logger.Errorf("No listeners specified for upgrade")
		return nil, nil, nil, fmt.Errorf("no listeners specified")
	}

	// CRITICAL: Check for incompatible clients (disconnected clients that require upgrade)
	if len(upgradeConfig.Analysis.IncompatibleClients) > 0 {
		w.logger.Errorf("Job %s has %d incompatible client(s)", j.JobID, len(upgradeConfig.Analysis.IncompatibleClients))
		for _, issue := range upgradeConfig.Analysis.IncompatibleClients {
			w.logger.Errorf("  - %s", issue)
		}
		return nil, nil, nil, fmt.Errorf("cannot upgrade: %d incompatible client(s) - %s",
			len(upgradeConfig.Analysis.IncompatibleClients),
			upgradeConfig.Analysis.IncompatibleClients[0])
	}

	return meta, upgradeConfig.Analysis, listenerNames, nil
}

// createMissingDependencies creates missing dependencies (Phase 1)
func (w *Worker) createMissingDependencies(ctx context.Context, j *job.Job, creator *upgrade.ResourceCreator, analysis *job.UpgradeAnalysisResult) ([]job.ResourceRef, error) {
	if len(analysis.MissingResources) == 0 {
		w.logger.Infof("No missing dependencies to create")
		return []job.ResourceRef{}, nil
	}

	w.logger.Infof("Creating %d missing dependencies", len(analysis.MissingResources))

	// No progress callback for dependencies - they don't count towards user-visible progress
	refs, err := creator.CreateMissingDependencies(ctx, j, nil)
	if err != nil {
		w.logger.Errorf("Failed to create dependencies: %v", err)
		return refs, err
	}

	return refs, nil
}

// createListenersInTargetVersion creates each listener in target version (Phase 2)
func (w *Worker) createListenersInTargetVersion(ctx context.Context, j *job.Job, creator *upgrade.ResourceCreator, meta *job.JobMetadata, listenerNames []string, createdResources []job.ResourceRef) ([]job.ResourceRef, error) {
	totalListeners := len(listenerNames)
	upgradeConfig := meta.UpgradeConfig

	for i, listenerName := range listenerNames {
		w.logger.Infof("Creating listener %d/%d: %s in version %s",
			i+1, totalListeners, listenerName, upgradeConfig.TargetVersion)

		// Temporarily set the listener name in metadata for creator
		originalName := meta.SourceResource.Name
		meta.SourceResource.Name = listenerName

		ref, err := creator.CreateListenerInTargetVersion(ctx, j)
		if err != nil {
			w.logger.Errorf("Failed to create listener %s: %v", listenerName, err)
			meta.SourceResource.Name = originalName // Restore original
			return createdResources, err
		}

		meta.SourceResource.Name = originalName // Restore original
		createdResources = append(createdResources, ref)

		// Update progress based on listener count only
		w.updateListenerProgress(ctx, j, i+1, totalListeners)
	}

	// Update final progress to 100%
	w.updateListenerProgress(ctx, j, totalListeners, totalListeners)

	return createdResources, nil
}

// updateListenerProgress updates job progress for listener creation
func (w *Worker) updateListenerProgress(ctx context.Context, j *job.Job, completed, total int) {
	percentage := float64(completed) / float64(total) * 100.0
	percentage = float64(int(percentage*10)) / 10.0 // Round to 1 decimal

	progress := &job.JobProgress{
		Total:      total,
		Completed:  completed,
		Failed:     0,
		Percentage: percentage,
	}
	if err := w.jobManager.UpdateJobProgress(ctx, j.ID, progress); err != nil {
		w.logger.Errorf("Failed to update job progress: %v", err)
	}
}

// updateBootstrapsAndNotifyClients updates bootstraps and notifies clients (Phase 4).
//
// Returns the first per-listener failure encountered. We keep going through
// remaining listeners after a failure so their bootstrap/notify steps have
// a chance to run and their outcomes are recorded in job metadata — but
// any failure ultimately surfaces as a job-level FailJob via the
// orchestrator.
func (w *Worker) updateBootstrapsAndNotifyClients(ctx context.Context, j *job.Job, meta *job.JobMetadata, analysis *job.UpgradeAnalysisResult, listenerNames []string) error {
	upgradeConfig := meta.UpgradeConfig

	if !analysis.BootstrapRequired || !upgradeConfig.UpdateBootstrap {
		w.logger.Infof("Bootstrap update not required or disabled")
		return nil
	}

	w.logger.Infof("Bootstrap update required for %d bootstraps", len(analysis.BootstrapNames))

	var firstErr error
	for _, listenerName := range listenerNames {
		requiresClientUpgrade := w.findRequiresClientUpgrade(analysis, listenerName)
		// Scope the bootstrap set to THIS listener; analysis.BootstrapNames
		// is the deduplicated global union and would cause each listener
		// iteration to re-update every other listener's bootstrap.
		listenerBootstraps := w.findListenerBootstrapNames(analysis, listenerName)
		if err := w.updateBootstrapsForListener(ctx, j, listenerName, listenerBootstraps, requiresClientUpgrade); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// findListenerBootstrapNames returns the bootstrap names recorded for a
// specific listener in the analysis. Falls back to nil so callers receive
// "no bootstraps" instead of the global union when the listener wasn't
// found (shouldn't happen — analyzer always records ListenerDetails).
func (w *Worker) findListenerBootstrapNames(analysis *job.UpgradeAnalysisResult, listenerName string) []string {
	for _, la := range analysis.ListenerDetails {
		if la.ListenerName == listenerName {
			return la.BootstrapNames
		}
	}
	return nil
}

// triggerSnapshotUpdates triggers snapshot updates for all upgraded listeners.
//
// Phase 3 is intentionally non-fatal: any failure here (transport, missing
// row, all-poke blackout) is logged at error level but does not stop the
// upgrade. The control-plane regenerates a snapshot on the next xDS
// request, and the operator can inspect logs if needed. Phase 4 still
// runs so client notification can proceed.
func (w *Worker) triggerSnapshotUpdates(ctx context.Context, meta *job.JobMetadata, listenerNames []string) {
	if w.pokeService == nil {
		w.logger.Warn("Poke service not available, snapshot updates will occur on next client request")
		return
	}

	project := meta.SourceResource.ProjectID
	toVersion := meta.UpgradeConfig.TargetVersion

	w.logger.Infof("📸 Triggering snapshot updates for %d upgraded listener(s)", len(listenerNames))

	for _, listenerName := range listenerNames {
		if err := w.pokeListenerSnapshot(ctx, listenerName, project, toVersion); err != nil {
			w.logger.Errorf("Failed to trigger snapshot for listener %s: %v", listenerName, err)
			// Do not abort upgrade — snapshot regeneration is best-effort.
		} else {
			w.logger.Infof("Snapshot update triggered for listener: %s", listenerName)
		}
	}
}

// pokeListenerSnapshot triggers snapshot regeneration via control-plane.
//
// For managed listeners we poke once per connected client. Individual poke
// failures are logged but not fatal; only when EVERY poke fails do we
// return an aggregate error so the caller can record a clear log line.
// The caller does NOT propagate this as a job failure — Phase 3 is
// always best-effort.
func (w *Worker) pokeListenerSnapshot(ctx context.Context, listenerName, project, toVersion string) error {
	// Get listener to determine if managed (NO version filter - listener name is unique per project)
	listenerCollection := w.dbContext.Client.Collection("listeners")
	var listener models.DBResource
	if err := listenerCollection.FindOne(ctx, bson.M{
		"general.name":    listenerName,
		"general.project": project,
	}).Decode(&listener); err != nil {
		return fmt.Errorf("failed to get listener %s: %w", listenerName, err)
	}

	// If managed, poke for each connected client
	if listener.General.Managed {
		// IMPORTANT: Services are still on old version at this point
		// We need to query by listener name + project only, NOT by version
		// Because updateServiceVersion happens AFTER client notifications
		clients, err := w.getConnectedClientsForListenerAnyVersion(ctx, listenerName, project)
		if err != nil {
			return fmt.Errorf("failed to get clients: %w", err)
		}

		if len(clients) == 0 {
			// No clients, send single poke with new version
			return w.sendPokeToControlPlane(ctx, listenerName, project, toVersion, "")
		}

		// Send poke for each client's downstream address with NEW version.
		// Track failures so a complete blackout is surfaced as an error.
		var lastErr error
		failed := 0
		for _, client := range clients {
			if err := w.sendPokeToControlPlane(ctx, listenerName, project, toVersion, client.DownstreamAddress); err != nil {
				w.logger.Errorf("Failed to poke for client %s: %v", client.ClientID, err)
				failed++
				lastErr = err
			}
		}
		if failed == len(clients) {
			return fmt.Errorf("all %d snapshot pokes failed for listener %s; last error: %w", failed, listenerName, lastErr)
		}
		return nil
	}

	// Unmanaged - single poke with new version
	return w.sendPokeToControlPlane(ctx, listenerName, project, toVersion, "")
}

// sendPokeToControlPlane sends poke request to control-plane via bridge
func (w *Worker) sendPokeToControlPlane(ctx context.Context, listenerName, project, version, downstreamAddress string) error {
	// Use the same PokeNode function that XDS updates use
	_, err := bridgeClient.PokeNode(ctx, *w.pokeService, listenerName, project, version, downstreamAddress)
	return err
}

// findRequiresClientUpgrade finds if a listener requires client upgrade
func (w *Worker) findRequiresClientUpgrade(analysis *job.UpgradeAnalysisResult, listenerName string) bool {
	for _, la := range analysis.ListenerDetails {
		if la.ListenerName == listenerName {
			return la.RequiresClientUpgrade
		}
	}
	return false
}

// storeCreatedResources stores created resources in job metadata
func (w *Worker) storeCreatedResources(ctx context.Context, j *job.Job, createdResources []job.ResourceRef) {
	if err := w.jobManager.UpdateJob(ctx, j.ID.Hex(), map[string]any{
		"$set": map[string]any{
			"metadata.upgrade_config.created_resources": createdResources,
		},
	}); err != nil {
		w.logger.Errorf("Failed to store created resources in job metadata: %v", err)
	}
}

// rollbackCreatedResources rolls back resources created during upgrade
func (w *Worker) rollbackCreatedResources(ctx context.Context, resources []job.ResourceRef) {
	if len(resources) == 0 {
		return
	}

	w.logger.Warnf("Rolling back %d created resources", len(resources))

	// Rollback in reverse order
	for i := len(resources) - 1; i >= 0; i-- {
		resource := resources[i]

		if resource.Skipped {
			w.logger.Debugf("Skipping rollback for %s/%s (was not created)",
				resource.Collection, resource.Name)
			continue
		}

		// Delete the resource - convert string ID to ObjectID
		objectID, err := primitive.ObjectIDFromHex(resource.ID)
		if err != nil {
			w.logger.Errorf("Failed to parse ObjectID %s for rollback: %v", resource.ID, err)
			continue
		}

		collection := w.dbContext.Client.Collection(resource.Collection)
		_, err = collection.DeleteOne(ctx, bson.M{"_id": objectID})
		if err != nil {
			w.logger.Errorf("Failed to rollback %s/%s: %v", resource.Collection, resource.Name, err)
		} else {
			w.logger.Infof("Rolled back %s/%s", resource.Collection, resource.Name)
		}
	}
}

// countNonSkipped counts resources that were actually created
func countNonSkipped(resources []job.ResourceRef) int {
	count := 0
	for _, r := range resources {
		if !r.Skipped {
			count++
		}
	}
	return count
}

// countSkipped counts resources that were skipped
func countSkipped(resources []job.ResourceRef) int {
	count := 0
	for _, r := range resources {
		if r.Skipped {
			count++
		}
	}
	return count
}

// getServicesForListener queries services collection for a listener
func (w *Worker) getServicesForListener(ctx context.Context, listenerName, project, version string) ([]struct {
	ServiceID string                  `bson:"_id"`
	Name      string                  `bson:"name"`
	Clients   []models.ServiceClients `bson:"clients"`
}, error,
) {
	serviceCollection := w.dbContext.Client.Collection("services")
	cursor, err := serviceCollection.Find(ctx, bson.M{
		"name":    listenerName,
		"project": project,
		"version": version,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query services for listener %s: %w", listenerName, err)
	}
	defer cursor.Close(ctx)

	var services []struct {
		ServiceID string                  `bson:"_id"`
		Name      string                  `bson:"name"`
		Clients   []models.ServiceClients `bson:"clients"`
	}
	if err := cursor.All(ctx, &services); err != nil {
		return nil, fmt.Errorf("failed to decode services for listener %s: %w", listenerName, err)
	}

	return services, nil
}

// getServicesForListenerAnyVersion queries services without version filter
// Used during upgrade when services haven't been updated to new version yet
func (w *Worker) getServicesForListenerAnyVersion(ctx context.Context, listenerName, project string) ([]struct {
	ServiceID string                  `bson:"_id"`
	Name      string                  `bson:"name"`
	Clients   []models.ServiceClients `bson:"clients"`
}, error,
) {
	serviceCollection := w.dbContext.Client.Collection("services")
	cursor, err := serviceCollection.Find(ctx, bson.M{
		"name":    listenerName,
		"project": project,
		// NO version filter - get services from any version
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query services for listener %s: %w", listenerName, err)
	}
	defer cursor.Close(ctx)

	var services []struct {
		ServiceID string                  `bson:"_id"`
		Name      string                  `bson:"name"`
		Clients   []models.ServiceClients `bson:"clients"`
	}
	if err := cursor.All(ctx, &services); err != nil {
		return nil, fmt.Errorf("failed to decode services for listener %s: %w", listenerName, err)
	}

	return services, nil
}

// collectClientIDs extracts all unique client IDs from services
func (w *Worker) collectClientIDs(services []struct {
	ServiceID string                  `bson:"_id"`
	Name      string                  `bson:"name"`
	Clients   []models.ServiceClients `bson:"clients"`
},
) []string {
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
	return clientIDs
}

// getConnectedClients queries clients collection for connected clients
func (w *Worker) getConnectedClients(ctx context.Context, clientIDs []string) ([]ClientRecord, error) {
	if len(clientIDs) == 0 {
		return nil, nil
	}

	clientCollection := w.dbContext.Client.Collection("clients")
	cursor, err := clientCollection.Find(ctx, bson.M{
		"client_id": bson.M{"$in": clientIDs},
		"connected": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query clients: %w", err)
	}
	defer cursor.Close(ctx)

	var clients []ClientRecord
	if err := cursor.All(ctx, &clients); err != nil {
		return nil, fmt.Errorf("failed to decode clients: %w", err)
	}

	return clients, nil
}

// updateBootstrapsForListener orchestrates bootstrap update and client notification.
//
// Strict-fail policy: any bootstrap update failure aborts the listener's
// Phase 4 — client notification and services.version advancement are
// skipped so a retry runs against a coherent fromVersion state. All
// bootstraps are still attempted (the inner loop never short-circuits),
// so the per-bootstrap outcomes are persisted in job metadata for the
// operator to inspect before clicking retry.
func (w *Worker) updateBootstrapsForListener(ctx context.Context, j *job.Job, listenerName string, bootstrapNames []string, requiresClientUpgrade bool) error {
	meta := j.Metadata
	project := meta.SourceResource.ProjectID
	fromVersion := meta.SourceResource.Version
	toVersion := meta.UpgradeConfig.TargetVersion

	w.logger.Infof("Updating %d bootstrap(s) for listener %s to version %s", len(bootstrapNames), listenerName, toVersion)

	// Step 1: Update all bootstraps (loop never short-circuits)
	bootstrapUpdates := w.performBootstrapUpdates(ctx, bootstrapNames, listenerName, project, fromVersion, toVersion)

	// Step 2: Store bootstrap updates in job metadata (best-effort audit)
	w.storeBootstrapUpdates(ctx, j, meta, bootstrapUpdates)

	// Step 3: Assess bootstrap outcomes — any failure aborts Phase 4 for this listener.
	// Notify/service-version updates are deliberately skipped so the listener
	// stays in a recoverable state for retry (services.version still matches
	// fromVersion, client binaries haven't been told to swap).
	if total := len(bootstrapUpdates); total > 0 {
		successes := 0
		var firstErr string
		for _, u := range bootstrapUpdates {
			if u.Success {
				successes++
				continue
			}
			if firstErr == "" {
				firstErr = u.Error
			}
		}
		if successes < total {
			return fmt.Errorf("bootstrap update incomplete for listener %s: %d/%d succeeded; first error: %s",
				listenerName, successes, total, firstErr)
		}
	}

	// Step 4: Update admin_port for managed listeners (advisory, stays non-fatal)
	if requiresClientUpgrade {
		w.updateAdminPortForListener(ctx, project, listenerName, toVersion)
	}

	// Step 5: Notify clients or update service version
	if requiresClientUpgrade {
		if err := w.notifyClientsForUpgrade(ctx, j, project, listenerName, toVersion); err != nil {
			w.logger.Errorf("Failed to notify clients for listener %s: %v", listenerName, err)
			return err
		}
		return nil
	}

	w.logger.Infof("Listener '%s' does not require client upgrade commands (unmanaged or no clients)", listenerName)
	if err := w.updateServiceVersion(ctx, project, listenerName, fromVersion, toVersion); err != nil {
		return err
	}
	return nil
}

// performBootstrapUpdates updates each bootstrap to target version
func (w *Worker) performBootstrapUpdates(ctx context.Context, bootstrapNames []string, listenerName, project, fromVersion, toVersion string) []job.BootstrapUpdate {
	collection := w.dbContext.Client.Collection("bootstrap")
	var bootstrapUpdates []job.BootstrapUpdate

	for _, bootstrapName := range bootstrapNames {
		bootstrapUpdate := job.BootstrapUpdate{
			BootstrapName: bootstrapName,
			ListenerName:  listenerName,
			FromVersion:   fromVersion,
			ToVersion:     toVersion,
		}

		// Get existing bootstrap
		existingBootstrap, err := w.getExistingBootstrap(ctx, collection, bootstrapName, project)
		if err != nil {
			w.logger.Errorf("Failed to find bootstrap %s: %v", bootstrapName, err)
			bootstrapUpdate.Success = false
			bootstrapUpdate.Error = fmt.Sprintf("bootstrap not found: %v", err)
			bootstrapUpdates = append(bootstrapUpdates, bootstrapUpdate)
			continue
		}

		// Update bootstrap version
		if err := w.updateBootstrapVersion(ctx, collection, bootstrapName, project, existingBootstrap.Resource.Version, toVersion); err != nil {
			w.logger.Errorf("Failed to update bootstrap %s: %v", bootstrapName, err)
			bootstrapUpdate.Success = false
			bootstrapUpdate.Error = err.Error()
			bootstrapUpdates = append(bootstrapUpdates, bootstrapUpdate)
			continue
		}

		w.logger.Infof("Updated bootstrap %s to version %s", bootstrapName, toVersion)
		bootstrapUpdate.Success = true
		bootstrapUpdates = append(bootstrapUpdates, bootstrapUpdate)
	}

	return bootstrapUpdates
}

// getExistingBootstrap retrieves existing bootstrap from database
func (w *Worker) getExistingBootstrap(ctx context.Context, collection *mongo.Collection, bootstrapName, project string) (*models.DBResource, error) {
	filter := bson.M{
		"general.name":    bootstrapName,
		"general.project": project,
	}

	var existingBootstrap models.DBResource
	if err := collection.FindOne(ctx, filter).Decode(&existingBootstrap); err != nil {
		return nil, err
	}

	return &existingBootstrap, nil
}

// updateBootstrapVersion updates bootstrap version fields in database
func (w *Worker) updateBootstrapVersion(ctx context.Context, collection *mongo.Collection, bootstrapName, project, currentVersion, toVersion string) error {
	// Increment resource.version (it's a string)
	newResourceVersion := w.incrementResourceVersion(currentVersion)

	filter := bson.M{
		"general.name":    bootstrapName,
		"general.project": project,
	}

	// CRITICAL: Also update the envoy-version in initial_metadata so clients request correct version
	update := bson.M{
		"$set": bson.M{
			"general.version":    toVersion,
			"general.updated_at": primitive.NewDateTimeFromTime(time.Now()),
			"resource.version":   newResourceVersion,
			// Update envoy-version in ADS config initial_metadata array
			"resource.resource.dynamic_resources.ads_config.grpc_services.0.initial_metadata.$[elem].value": toVersion,
		},
	}

	// Array filter to target the envoy-version metadata entry specifically
	arrayFilters := options.Update().SetArrayFilters(options.ArrayFilters{
		Filters: []any{
			bson.M{"elem.key": "envoy-version"},
		},
	})

	result, err := collection.UpdateOne(ctx, filter, update, arrayFilters)
	if err != nil {
		return err
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("bootstrap not found")
	}

	return nil
}

// incrementResourceVersion increments resource version string
func (w *Worker) incrementResourceVersion(currentVersion string) string {
	if currentVersion == "" {
		return "1"
	}

	var currentInt int
	if _, err := fmt.Sscanf(currentVersion, "%d", &currentInt); err == nil {
		return fmt.Sprintf("%d", currentInt+1)
	}

	w.logger.Warnf("Failed to parse resource.version '%s', using 1", currentVersion)
	return "1"
}

// storeBootstrapUpdates stores bootstrap update results in job metadata
func (w *Worker) storeBootstrapUpdates(ctx context.Context, j *job.Job, meta *job.JobMetadata, bootstrapUpdates []job.BootstrapUpdate) {
	if len(bootstrapUpdates) == 0 {
		return
	}

	existingUpdates := meta.UpgradeConfig.BootstrapUpdates
	allUpdates := make([]job.BootstrapUpdate, 0, len(existingUpdates)+len(bootstrapUpdates))
	allUpdates = append(allUpdates, existingUpdates...)
	allUpdates = append(allUpdates, bootstrapUpdates...)

	// Keep the in-memory metadata in sync with what we just wrote, so a
	// subsequent listener iteration sees the accumulated list instead of
	// reading a stale snapshot and clobbering earlier updates in the DB.
	meta.UpgradeConfig.BootstrapUpdates = allUpdates

	if err := w.jobManager.UpdateJob(ctx, j.ID.Hex(), map[string]any{
		"$set": map[string]any{
			"metadata.upgrade_config.bootstrap_updates": allUpdates,
		},
	}); err != nil {
		w.logger.Errorf("Failed to store bootstrap updates in job metadata: %v", err)
	}
}

// updateAdminPortForListener updates admin_port version for listener
func (w *Worker) updateAdminPortForListener(ctx context.Context, project, listenerName, toVersion string) {
	w.logger.Debugf("Updating admin_port for managed listener: %s", listenerName)
	if err := w.updateAdminPortVersion(ctx, project, listenerName, toVersion); err != nil {
		w.logger.Warnf("Failed to update admin_port (may not exist): %v", err)
		// Don't fail the whole operation - admin_port update is not critical
	} else {
		w.logger.Infof("Updated admin_port for listener %s to version %s", listenerName, toVersion)
	}
}

// notifyClientsForUpgrade orchestrates client notification for upgrade.
//
// Strict-fail policy: any client notification failure (even one of many)
// aborts the listener's Phase 4 — services.version is NOT advanced and
// the function returns an error so the orchestrator marks the job FAILED.
// All clients are still attempted (the inner loop never short-circuits),
// so partial outcomes are persisted in metadata.client_responses.
//
// Why keep services.version pinned to fromVersion on failure: retry
// queries services with `version: fromVersion` to find the affected
// clients; advancing services on partial success would orphan the unsent
// clients and prevent retry from finding them.
func (w *Worker) notifyClientsForUpgrade(ctx context.Context, j *job.Job, project, listenerName, toVersion string) error {
	meta := j.Metadata
	fromVersion := meta.SourceResource.Version

	// Step 1: Get connected clients first so we can decide whether the
	// missing commandHandler is a hard failure or a no-op.
	clients, err := w.getConnectedClientsForListener(ctx, listenerName, project, fromVersion)
	if err != nil {
		return err
	}
	originalConnectedCount := len(clients)

	// On retry, exclude clients that already completed UPGRADE_LISTENER in
	// the original job — they'd otherwise suffer a needless systemd restart
	// (elchi-client's ReplaceAll is a no-op once the file targets toVersion).
	allSkippedByRetryFilter := false
	if j.RetryInfo != nil && !j.RetryInfo.OriginalJobID.IsZero() {
		skip := w.collectAlreadyUpgradedClientIDs(ctx, j.RetryInfo.OriginalJobID)
		if len(skip) > 0 {
			filtered := make([]ClientRecord, 0, len(clients))
			for _, c := range clients {
				if !skip[c.ClientID] {
					filtered = append(filtered, c)
				}
			}
			if skipped := len(clients) - len(filtered); skipped > 0 {
				w.logger.Infof("Retry: skipping %d already-upgraded client(s) for listener %s", skipped, listenerName)
			}
			if len(filtered) == 0 && originalConnectedCount > 0 {
				allSkippedByRetryFilter = true
			}
			clients = filtered
		}
	}

	if len(clients) == 0 {
		// Two distinct paths land here:
		//   1. No connected clients exist (services empty, OR services
		//      records exist but every client is offline). Advancing
		//      services.version here would silently diverge the DB from
		//      offline-client reality — exactly the drift Bug #1 closes.
		//      Preserve the pre-retry behaviour: return nil and leave
		//      services pinned to fromVersion.
		//   2. Retry skip-filter removed every client because the previous
		//      job already notified all of them. services.version is still
		//      pinned to fromVersion (we keep it old on partial failure);
		//      now that every client is confirmed upgraded, it is safe to
		//      advance it so the DB reflects reality.
		if allSkippedByRetryFilter {
			w.logger.Infof("Retry: all clients already upgraded for listener %s; advancing services.version", listenerName)
			return w.updateServiceVersion(ctx, project, listenerName, fromVersion, toVersion)
		}
		w.logger.Warnf("No connected clients found for listener %s (precheck should have caught this)", listenerName)
		return nil
	}

	if w.commandHandler == nil {
		// We have clients to notify but cannot — the upgrade is incomplete.
		return fmt.Errorf("command handler not available; cannot notify %d client(s) for listener %s", len(clients), listenerName)
	}

	w.logger.Infof("Sending UPGRADE_LISTENER commands to %d client(s)", len(clients))

	// Step 2: Send upgrade commands to all clients (loop never short-circuits)
	clientResponses, successCount, failureCount := w.sendUpgradeCommandsToClients(ctx, clients, meta, project, listenerName, fromVersion, toVersion)

	// Step 3: Store client responses in job metadata
	w.storeClientResponses(ctx, j, clientResponses)

	// Step 4: Any failure aborts the listener — keep services.version on
	// fromVersion so retry can find the same client set.
	if failureCount > 0 {
		firstErr := extractFirstClientError(clientResponses)
		return fmt.Errorf("client notification incomplete for listener %s: %d/%d succeeded, %d failed; first error: %s",
			listenerName, successCount, len(clients), failureCount, firstErr)
	}

	w.logger.Infof("Upgrade notification summary: %d succeeded", successCount)
	if err := w.updateServiceVersion(ctx, project, listenerName, fromVersion, toVersion); err != nil {
		return err
	}
	return nil
}

// extractFirstClientError pulls the error message from the first failed
// client response so the aggregate error carries a hint about the cause.
func extractFirstClientError(responses []any) string {
	for _, r := range responses {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if success, _ := m["success"].(bool); success {
			continue
		}
		if msg, ok := m["error"].(string); ok && msg != "" {
			return msg
		}
	}
	return "no error details captured"
}

// getConnectedClientsForListener retrieves connected clients using the listener
func (w *Worker) getConnectedClientsForListener(ctx context.Context, listenerName, project, version string) ([]ClientRecord, error) {
	// Find services using this listener
	services, err := w.getServicesForListener(ctx, listenerName, project, version)
	if err != nil {
		return nil, err
	}

	if len(services) == 0 {
		w.logger.Infof("No services found for listener %s", listenerName)
		return []ClientRecord{}, nil
	}

	w.logger.Infof("Found %d service(s) using listener %s", len(services), listenerName)

	// Collect client IDs
	clientIDs := w.collectClientIDs(services)
	if len(clientIDs) == 0 {
		w.logger.Infof("No clients found in service records for listener %s", listenerName)
		return []ClientRecord{}, nil
	}

	// Get connected clients
	return w.getConnectedClients(ctx, clientIDs)
}

// getConnectedClientsForListenerAnyVersion retrieves connected clients without version filter
// Used during upgrade when services haven't been updated to new version yet
func (w *Worker) getConnectedClientsForListenerAnyVersion(ctx context.Context, listenerName, project string) ([]ClientRecord, error) {
	// Find services using this listener (ANY version)
	services, err := w.getServicesForListenerAnyVersion(ctx, listenerName, project)
	if err != nil {
		return nil, err
	}

	if len(services) == 0 {
		w.logger.Infof("No services found for listener %s (any version)", listenerName)
		return []ClientRecord{}, nil
	}

	w.logger.Infof("Found %d service(s) using listener %s (any version)", len(services), listenerName)

	// Collect client IDs
	clientIDs := w.collectClientIDs(services)
	if len(clientIDs) == 0 {
		w.logger.Infof("No clients found in service records for listener %s", listenerName)
		return []ClientRecord{}, nil
	}

	// Get connected clients
	return w.getConnectedClients(ctx, clientIDs)
}

// sendUpgradeCommandsToClients sends upgrade command to each client
func (w *Worker) sendUpgradeCommandsToClients(ctx context.Context, clients []ClientRecord, meta *job.JobMetadata, project, listenerName, fromVersion, toVersion string) ([]any, int, int) {
	var clientResponses []any
	successCount := 0
	failureCount := 0

	for _, client := range clients {
		w.logger.Infof("Sending upgrade command to client %s (%s): %s -> %s",
			client.Name, client.ClientID, fromVersion, toVersion)

		// Build upgrade operation
		upgradeOp := w.buildUpgradeOperation(client, meta, project, listenerName, fromVersion, toVersion)
		requestDetails := w.buildRequestDetails(meta, toVersion)

		// Send command
		response, err := w.commandHandler.HandleSendCommand(ctx, upgradeOp, requestDetails)
		if err != nil {
			w.logger.Errorf("Failed to send UPGRADE_LISTENER to client %s: %v", client.ClientID, err)
			failureCount++
			clientResponses = append(clientResponses, map[string]any{
				"client_id": client.ClientID,
				"error":     err.Error(),
				"success":   false,
			})
			continue
		}

		// Success — wrap in envelope so retry filter can identify per-client
		// outcomes deterministically (matches the {client_id, success, error}
		// shape used on the failure path).
		clientResponses = append(clientResponses, map[string]any{
			"client_id": client.ClientID,
			"success":   true,
			"result":    response,
		})
		w.logger.Infof("Successfully sent UPGRADE_LISTENER to client %s", client.ClientID)
		successCount++
	}

	return clientResponses, successCount, failureCount
}

// collectAlreadyUpgradedClientIDs walks the retry chain starting at the
// given job and returns the union of client IDs that successfully
// processed UPGRADE_LISTENER across every ancestor. Walking the chain is
// required because each retry only writes responses for the clients it
// actually notified — earlier successes are otherwise invisible to a
// chain-retry and would be re-notified (gratuitous restart).
//
// Bounded by maxRetryChainDepth so a corrupted RetryInfo cycle cannot
// pin a worker forever. Fail-open by design: any fetch error returns
// whatever the walk has collected so far, so the caller may still skip
// some — over-sending is the safer regression mode.
func (w *Worker) collectAlreadyUpgradedClientIDs(ctx context.Context, originalJobID primitive.ObjectID) map[string]bool {
	const maxRetryChainDepth = 10
	skip := make(map[string]bool)
	current := originalJobID
	visited := make(map[primitive.ObjectID]bool, maxRetryChainDepth)

	for depth := 0; depth < maxRetryChainDepth && !current.IsZero(); depth++ {
		if visited[current] {
			w.logger.Warnf("Retry chain cycle detected at job %s; aborting walk", current.Hex())
			break
		}
		visited[current] = true

		j, err := w.jobManager.GetJob(ctx, current.Hex())
		if err != nil {
			w.logger.Warnf("Retry: failed to fetch job %s while walking chain (continuing with %d already collected): %v", current.Hex(), len(skip), err)
			break
		}
		if j.Metadata == nil || j.Metadata.UpgradeConfig == nil {
			break
		}

		for _, raw := range j.Metadata.UpgradeConfig.ClientResponses {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if success, _ := m["success"].(bool); !success {
				continue
			}
			if cid, _ := m["client_id"].(string); cid != "" {
				skip[cid] = true
			}
		}

		if j.RetryInfo == nil || j.RetryInfo.OriginalJobID.IsZero() {
			break
		}
		current = j.RetryInfo.OriginalJobID
	}

	return skip
}

// buildUpgradeOperation creates upgrade operation for client
func (w *Worker) buildUpgradeOperation(client ClientRecord, _ *job.JobMetadata, project, listenerName, fromVersion, _ string) *models.Operations {
	return &models.Operations{
		Type: models.CommandTypeJSON(pb.CommandType_UPGRADE_LISTENER),
		Clients: []models.ServiceClients{
			{
				ClientID:          client.ClientID,
				DownstreamAddress: client.DownstreamAddress,
			},
		},
		Command: models.Command{
			Name:        listenerName,
			Project:     project,
			FromVersion: fromVersion,
		},
	}
}

// buildRequestDetails creates request details from job metadata
func (w *Worker) buildRequestDetails(meta *job.JobMetadata, toVersion string) models.RequestDetails {
	if meta == nil || meta.TriggerUser == nil {
		w.logger.Warn("Job metadata or trigger user is missing")
		return models.RequestDetails{Version: toVersion}
	}

	return models.RequestDetails{
		Version: toVersion,
		User: models.UserDetails{
			IsOwner:  meta.TriggerUser.Role == "owner",
			Role:     models.Role(meta.TriggerUser.Role),
			UserID:   meta.TriggerUser.ID,
			UserName: meta.TriggerUser.Username,
			// Project snapshot captured at trigger time — needed for the
			// internal forward token so Editor/Viewer authorization still
			// passes when the worker forwards to another controller pod.
			Projects: append([]string(nil), meta.TriggerUser.Projects...),
		},
	}
}

// storeClientResponses stores client responses in job metadata
func (w *Worker) storeClientResponses(ctx context.Context, j *job.Job, clientResponses []any) {
	if len(clientResponses) == 0 {
		return
	}

	// APPEND to existing responses instead of overwriting
	existingResponses := j.Metadata.UpgradeConfig.ClientResponses
	allResponses := make([]any, 0, len(existingResponses)+len(clientResponses))
	allResponses = append(allResponses, existingResponses...)
	allResponses = append(allResponses, clientResponses...)

	j.Metadata.UpgradeConfig.ClientResponses = allResponses
	err := w.jobManager.UpdateJobMetadata(ctx, j.ID, j.Metadata)
	if err != nil {
		w.logger.Errorf("Failed to update job metadata with client responses: %v", err)
	} else {
		w.logger.Infof("Stored %d client responses in job metadata (total: %d)", len(clientResponses), len(allResponses))
	}
}

// updateServiceVersion updates service version from fromVersion to toVersion.
// ModifiedCount == 0 is NOT treated as an error — a listener may have no
// services row at all (unmanaged path), or the service may already be at
// the new version from a previous run.
func (w *Worker) updateServiceVersion(ctx context.Context, project, listenerName, fromVersion, toVersion string) error {
	serviceCollection := w.dbContext.Client.Collection("services")
	updateResult, err := serviceCollection.UpdateMany(ctx, bson.M{
		"name":    listenerName,
		"project": project,
		"version": fromVersion, // Still on old version
	}, bson.M{
		"$set": bson.M{
			"version": toVersion, // Update to new version
		},
	})
	if err != nil {
		w.logger.Errorf("Failed to update service version: %v", err)
		return fmt.Errorf("update service version for %s: %w", listenerName, err)
	}
	if updateResult.ModifiedCount > 0 {
		w.logger.Infof("Updated %d service(s) to version %s", updateResult.ModifiedCount, toVersion)
	} else {
		w.logger.Debugf("No services found to update for listener %s", listenerName)
	}
	return nil
}

// updateAdminPortVersion updates admin_port version to target version
// Admin ports are not versioned like other resources - they only have name and project
// So we update by name and project only, without checking fromVersion
func (w *Worker) updateAdminPortVersion(ctx context.Context, project, listenerName, toVersion string) error {
	adminPortCollection := w.dbContext.Client.Collection("admin_ports")

	// Admin port uses name and project only (no version in filter)
	filter := bson.M{
		"name":    listenerName,
		"project": project,
	}

	update := bson.M{
		"$set": bson.M{
			"version":    toVersion,
			"updated_at": primitive.NewDateTimeFromTime(time.Now()),
		},
	}

	result, err := adminPortCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to update admin_port: %w", err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("admin_port not found for listener %s", listenerName)
	}

	return nil
}
