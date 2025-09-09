package audit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/security"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Service handles audit logging operations
type Service struct {
	db       *db.AppContext
	logger   *logger.Logger
	wg       sync.WaitGroup  // Track goroutines for graceful shutdown
	shutdown chan struct{}   // Signal for shutdown
	mu       sync.RWMutex    // Protect concurrent access
	batch    chan *models.AuditEntry // Batch processing channel
	batchSize int // Number of entries to batch
	batchInterval time.Duration // Max time to wait before flushing batch
}

// ================== STANDARDIZED ERROR HANDLING ==================

// AuditError represents a standardized audit system error with context
type AuditError struct {
	Operation string // Operation that failed (e.g., "store", "query", "stats")
	Context   string // Additional context (e.g., "batch_flush", "user_filter")
	Err       error  // Original error
}

func (e *AuditError) Error() string {
	if e.Context != "" {
		return fmt.Sprintf("audit %s failed (%s): %v", e.Operation, e.Context, e.Err)
	}
	return fmt.Sprintf("audit %s failed: %v", e.Operation, e.Err)
}

// Unwrap returns the underlying error for error chain compatibility
func (e *AuditError) Unwrap() error {
	return e.Err
}

// newAuditError creates a new standardized audit error
func newAuditError(operation, context string, err error) *AuditError {
	return &AuditError{
		Operation: operation,
		Context:   context,
		Err:       err,
	}
}

// logAndWrapError logs error with context and returns wrapped error
func (s *Service) logAndWrapError(operation, context string, err error) *AuditError {
	auditErr := newAuditError(operation, context, err)
	s.logger.Errorf("%s", auditErr.Error())
	return auditErr
}

// NewService creates a new audit service
func NewService(db *db.AppContext, logger *logger.Logger) *Service {
	s := &Service{
		db:       db,
		logger:   logger,
		shutdown: make(chan struct{}),
		batch:    make(chan *models.AuditEntry, 1000), // Buffer up to 1000 entries
		batchSize: 10, // Batch 10 entries at a time
		batchInterval: 5 * time.Second, // Flush every 5 seconds
	}
	
	// Start batch processor
	s.startBatchProcessor()
	
	return s
}

// Store saves an audit entry to the database with async batching capability
func (s *Service) Store(entry *models.AuditEntry) error {
	if entry == nil {
		return nil
	}

	// Skip if action is empty (non-actionable requests)
	if entry.Action == "" {
		return nil
	}

	// Generate ID if not set
	if entry.ID == "" {
		entry.ID = primitive.NewObjectID().Hex()
	}

	// Ensure timestamp
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	// Use context with timeout for better error handling
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	collection := s.db.Client.Collection("audit_logs")
	_, err := collection.InsertOne(ctx, entry)
	if err != nil {
		return s.logAndWrapError("store", "single_entry", err)
	}

	s.logger.Debugf("Stored audit entry: action=%s, resource=%s/%s, user=%s",
		entry.Action, entry.ResourceType, entry.ResourceID, entry.Username)

	return nil
}

// StoreAsync stores audit entry asynchronously (non-blocking)
func (s *Service) StoreAsync(entry *models.AuditEntry) {
	if entry == nil || entry.Action == "" {
		return
	}
	
	// Generate ID if not set
	if entry.ID == "" {
		entry.ID = primitive.NewObjectID().Hex()
	}
	
	// Ensure timestamp
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	
	// Try to add to batch channel (non-blocking)
	select {
	case s.batch <- entry:
		// Successfully queued
	case <-s.shutdown:
		s.logger.Debugf("Audit service shutting down, skipping async store")
	default:
		// Channel full, fallback to direct store in goroutine with shutdown monitoring
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			
			// Check for shutdown signal before processing
			select {
			case <-s.shutdown:
				s.logger.Debugf("Audit service shutting down, skipping fallback store")
				return
			default:
				if err := s.Store(entry); err != nil {
					// Already logged by Store method, just continue
				}
			}
		}()
	}
}

// Shutdown gracefully shuts down the audit service
func (s *Service) Shutdown(ctx context.Context) error {
	// First signal all goroutines to stop accepting new work
	close(s.shutdown)
	
	// Give a moment for goroutines to detect shutdown signal
	time.Sleep(100 * time.Millisecond)
	
	// Then close batch channel to stop accepting new entries (after goroutines are aware of shutdown)
	func() {
		defer func() {
			if recover() != nil {
				// Channel already closed, ignore
			}
		}()
		close(s.batch)
	}()
	
	// Wait for all goroutines to finish or context to timeout
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	
	select {
	case <-done:
		s.logger.Info("Audit service shutdown completed")
		return nil
	case <-ctx.Done():
		s.logger.Warn("Audit service shutdown timeout")
		return ctx.Err()
	}
}

// startBatchProcessor starts the background processor for batch inserts
func (s *Service) startBatchProcessor() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		
		var batch []*models.AuditEntry
		ticker := time.NewTicker(s.batchInterval)
		defer ticker.Stop()
		
		for {
			select {
			case <-s.shutdown:
				// Flush remaining entries before shutdown
				if len(batch) > 0 {
					s.flushBatch(batch)
				}
				return
				
			case entry, ok := <-s.batch:
				if !ok {
					// Channel closed, flush and exit
					if len(batch) > 0 {
						s.flushBatch(batch)
					}
					return
				}
				
				batch = append(batch, entry)
				
				// Flush if batch size reached
				if len(batch) >= s.batchSize {
					s.flushBatch(batch)
					batch = nil
				}
				
			case <-ticker.C:
				// Flush on interval
				if len(batch) > 0 {
					s.flushBatch(batch)
					batch = nil
				}
			}
		}
	}()
}

// flushBatch inserts a batch of audit entries to the database
func (s *Service) flushBatch(entries []*models.AuditEntry) {
	if len(entries) == 0 {
		return
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	
	// Convert to interface slice for InsertMany
	docs := make([]interface{}, len(entries))
	for i, entry := range entries {
		docs[i] = entry
	}
	
	collection := s.db.Client.Collection("audit_logs")
	_, err := collection.InsertMany(ctx, docs)
	if err != nil {
		s.logger.Errorf("audit batch_flush failed (%d entries): %v", len(entries), err)
	} else {
		s.logger.Debugf("Flushed %d audit entries to database", len(entries))
	}
}

// Helper functions for gin context

// GetAuditEntry retrieves the audit entry from gin context
func GetAuditEntry(c *gin.Context) *models.AuditEntry {
	if audit, exists := c.Get("audit_entry"); exists {
		if entry, ok := audit.(*models.AuditEntry); ok {
			return entry
		}
	}
	return nil
}

// SetAuditEntry sets the audit entry in gin context
func SetAuditEntry(c *gin.Context, entry *models.AuditEntry) {
	c.Set("audit_entry", entry)
}

// SetAuditAction sets the action in the audit entry
func SetAuditAction(c *gin.Context, action string) {
	if audit := GetAuditEntry(c); audit != nil {
		// Get or create mutex for this context
		mu := getContextMutex(c)
		mu.Lock()
		defer mu.Unlock()
		audit.Action = action
	}
}

// SetAuditResource sets resource information in the audit entry
func SetAuditResource(c *gin.Context, resourceType, resourceID, resourceName, project string) {
	if audit := GetAuditEntry(c); audit != nil {
		// Get or create mutex for this context
		mu := getContextMutex(c)
		mu.Lock()
		defer mu.Unlock()
		audit.ResourceType = resourceType
		audit.ResourceID = resourceID
		audit.ResourceName = resourceName
		audit.Project = project
	}
}

// SetAuditError sets error information in the audit entry
func SetAuditError(c *gin.Context, errorMsg string) {
	if audit := GetAuditEntry(c); audit != nil {
		// Get or create mutex for this context
		mu := getContextMutex(c)
		mu.Lock()
		defer mu.Unlock()
		audit.Success = false
		audit.ErrorMessage = errorMsg
	}
	c.Set("audit_error", errorMsg)
}

// SetAuditSuccess sets success status in the audit entry
func SetAuditSuccess(c *gin.Context, success bool) {
	if audit := GetAuditEntry(c); audit != nil {
		// Get or create mutex for this context
		mu := getContextMutex(c)
		mu.Lock()
		defer mu.Unlock()
		audit.Success = success
	}
}

// SetAuditChanges sets the changes map for PUT requests
func SetAuditChanges(c *gin.Context, changes map[string]any) {
	if audit := GetAuditEntry(c); audit != nil {
		// Get or create mutex for this context
		mu := getContextMutex(c)
		mu.Lock()
		defer mu.Unlock()
		audit.Changes = changes
	}
}

// SetAuditSaveOrPublish sets the save_or_publish information in the audit entry
func SetAuditSaveOrPublish(c *gin.Context, saveOrPublish string) {
	if audit := GetAuditEntry(c); audit != nil {
		// Get or create mutex for this context
		mu := getContextMutex(c)
		mu.Lock()
		defer mu.Unlock()
		audit.SaveOrPublish = saveOrPublish
	}
}

// SetAuditCommand sets the command details for client commands
func SetAuditCommand(c *gin.Context, command map[string]any) {
	if audit := GetAuditEntry(c); audit != nil {
		// Get or create mutex for this context
		mu := getContextMutex(c)
		mu.Lock()
		defer mu.Unlock()
		audit.Command = command
	}
}

// mutexPool reuses mutexes to prevent memory leaks
var mutexPool = sync.Pool{
	New: func() interface{} {
		return &sync.Mutex{}
	},
}

// getContextMutex gets or creates a mutex for this request context using sync.Pool
func getContextMutex(c *gin.Context) *sync.Mutex {
	const mutexKey = "_audit_mutex"
	
	if mu, exists := c.Get(mutexKey); exists {
		if mutex, ok := mu.(*sync.Mutex); ok {
			return mutex
		}
	}
	
	// Get mutex from pool to prevent memory leaks
	mutex := mutexPool.Get().(*sync.Mutex)
	c.Set(mutexKey, mutex)
	
	// Set cleanup function to return mutex to pool when context is done
	c.Set("_audit_mutex_cleanup", func() {
		mutexPool.Put(mutex)
	})
	
	return mutex
}

// CleanupContextMutex should be called when request context is done (in middleware)
func CleanupContextMutex(c *gin.Context) {
	if cleanup, exists := c.Get("_audit_mutex_cleanup"); exists {
		if cleanupFunc, ok := cleanup.(func()); ok {
			cleanupFunc()
		}
	}
}

// ================== AUDIT QUERY AND ANALYTICS ==================

// AuditQuery represents parameters for querying audit logs
type AuditQuery struct {
	UserID       string    `json:"user_id,omitempty"`
	Username     string    `json:"username,omitempty"`
	Action       string    `json:"action,omitempty"`
	ResourceType string    `json:"resource_type,omitempty"`
	Project      string    `json:"project,omitempty"`
	StartTime    time.Time `json:"start_time,omitempty"`
	EndTime      time.Time `json:"end_time,omitempty"`
	Success      *bool     `json:"success,omitempty"`
	Limit        int       `json:"limit,omitempty"`
	Skip         int       `json:"skip,omitempty"`
}

// AuditStats represents audit statistics
type AuditStats struct {
	TotalEntries    int64            `json:"total_entries"`
	SuccessRate     float64          `json:"success_rate"`
	TopActions      map[string]int64 `json:"top_actions"`
	TopUsers        map[string]int64 `json:"top_users"`
	TopResources    map[string]int64 `json:"top_resources"`
	ErrorRate       float64          `json:"error_rate"`
	AverageResponse int64            `json:"average_response_ms"`
}

// Query retrieves audit entries based on given criteria
func (s *Service) Query(ctx context.Context, query AuditQuery) ([]*models.AuditEntry, error) {
	collection := s.db.Client.Collection("audit_logs")
	
	// Build MongoDB filter
	filter := s.buildAuditFilter(query)
	
	// Set default limit
	if query.Limit == 0 {
		query.Limit = 100
	}
	
	// Build options
	opts := s.buildQueryOptions(query)
	
	cursor, err := collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, s.logAndWrapError("query", "find_entries", err)
	}
	defer cursor.Close(ctx)
	
	var entries []*models.AuditEntry
	if err = cursor.All(ctx, &entries); err != nil {
		return nil, s.logAndWrapError("query", "decode_entries", err)
	}
	
	return entries, nil
}

// Count retrieves total count of audit entries matching query (for pagination)
func (s *Service) Count(ctx context.Context, query AuditQuery) (int64, error) {
	collection := s.db.Client.Collection("audit_logs")
	
	// Build MongoDB filter  
	filter := s.buildAuditFilter(query)
	
	count, err := collection.CountDocuments(ctx, filter)
	if err != nil {
		return 0, s.logAndWrapError("count", "count_documents", err)
	}
	
	return count, nil
}

// GetStats retrieves audit statistics for dashboard/monitoring
func (s *Service) GetStats(ctx context.Context, query AuditQuery) (*AuditStats, error) {
	collection := s.db.Client.Collection("audit_logs")
	filter := s.buildAuditFilter(query)
	
	// Debug log the filter
	s.logger.Debugf("Audit stats filter: %+v", filter)
	
	// This is a complex aggregation - in production, consider using views or scheduled aggregations
	pipeline := s.buildStatsPipeline(filter)
	
	cursor, err := collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, s.logAndWrapError("stats", "aggregate_pipeline", err)
	}
	defer cursor.Close(ctx)
	
	var results []map[string]any
	if err = cursor.All(ctx, &results); err != nil {
		return nil, err
	}
	
	return s.parseStatsResult(results), nil
}

// CleanupOldEntries removes audit entries older than specified duration
func (s *Service) CleanupOldEntries(ctx context.Context, olderThan time.Duration) (int64, error) {
	collection := s.db.Client.Collection("audit_logs")
	cutoffTime := time.Now().Add(-olderThan)
	
	filter := map[string]any{
		"timestamp": map[string]any{"$lt": cutoffTime},
	}
	
	result, err := collection.DeleteMany(ctx, filter)
	if err != nil {
		return 0, s.logAndWrapError("cleanup", "delete_old_entries", err)
	}
	
	s.logger.Infof("Cleaned up %d old audit entries", result.DeletedCount)
	return result.DeletedCount, nil
}

// ================== HELPER METHODS ==================

// buildAuditFilter constructs MongoDB filter from query parameters
func (s *Service) buildAuditFilter(query AuditQuery) bson.M {
	filter := bson.M{}
	
	if query.UserID != "" {
		filter["user_id"] = query.UserID
	}
	if query.Username != "" {
		if sanitizedUsername, valid := security.SanitizeSearchInput(query.Username); valid && sanitizedUsername != "" {
			filter["username"] = bson.M{"$regex": sanitizedUsername, "$options": "i"}
		}
	}
	if query.Action != "" {
		filter["action"] = query.Action
	}
	if query.ResourceType != "" {
		// Support both exact match and prefix match for resource types
		// This allows filtering extensions like "extensions/http_protocol_options" with "extensions"
		if sanitizedResourceType, valid := security.SanitizeSearchInput(query.ResourceType); valid && sanitizedResourceType != "" {
			filter["resource_type"] = bson.M{"$regex": "^" + sanitizedResourceType, "$options": "i"}
		}
	}
	if query.Project != "" {
		filter["project"] = query.Project
	}
	if query.Success != nil {
		filter["success"] = *query.Success
	}
	
	// Time range filter
	if !query.StartTime.IsZero() || !query.EndTime.IsZero() {
		timeFilter := bson.M{}
		if !query.StartTime.IsZero() {
			timeFilter["$gte"] = query.StartTime
		}
		if !query.EndTime.IsZero() {
			timeFilter["$lte"] = query.EndTime
		}
		filter["timestamp"] = timeFilter
	}
	
	return filter
}

// buildQueryOptions constructs MongoDB options from query parameters
func (s *Service) buildQueryOptions(query AuditQuery) *options.FindOptions {
	opts := options.Find()
	
	// Pagination
	if query.Skip > 0 {
		opts.SetSkip(int64(query.Skip))
	}
	if query.Limit > 0 {
		opts.SetLimit(int64(query.Limit))
	}
	
	// Sort by timestamp descending (newest first)
	opts.SetSort(bson.D{{Key: "timestamp", Value: -1}})
	
	return opts
}

// buildStatsPipeline constructs aggregation pipeline for statistics
func (s *Service) buildStatsPipeline(matchFilter bson.M) []bson.M {
	return []bson.M{
		{"$match": matchFilter},
		{
			"$facet": bson.M{
				"overview": []bson.M{
					{
						"$group": bson.M{
							"_id": nil,
							"total_entries": bson.M{"$sum": 1},
							"success_count": bson.M{
								"$sum": bson.M{
									"$cond": []any{"$success", 1, 0},
								},
							},
							"total_duration": bson.M{"$sum": "$duration_ms"},
						},
					},
				},
				"top_actions": []bson.M{
					{"$match": bson.M{"action": bson.M{"$ne": ""}}},
					{"$group": bson.M{
						"_id":   "$action",
						"count": bson.M{"$sum": 1},
					}},
					{"$sort": bson.M{"count": -1}},
					{"$limit": 20},
				},
				"top_users": []bson.M{
					{"$match": bson.M{"username": bson.M{"$ne": ""}}},
					{"$group": bson.M{
						"_id":   "$username",
						"count": bson.M{"$sum": 1},
					}},
					{"$sort": bson.M{"count": -1}},
					{"$limit": 20},
				},
				"top_resources": []bson.M{
					{"$match": bson.M{"resource_type": bson.M{"$ne": ""}}},
					{"$group": bson.M{
						"_id":   "$resource_type",
						"count": bson.M{"$sum": 1},
					}},
					{"$sort": bson.M{"count": -1}},
					{"$limit": 20},
				},
			},
		},
	}
}

// parseStatsResult parses aggregation results into AuditStats
func (s *Service) parseStatsResult(results []map[string]any) *AuditStats {
	if len(results) == 0 {
		s.logger.Debugf("No aggregation results found")
		return &AuditStats{}
	}
	
	result := results[0]
	s.logger.Debugf("Parsing stats result: %+v", result)
	
	stats := &AuditStats{
		TopActions:   make(map[string]int64),
		TopUsers:     make(map[string]int64),
		TopResources: make(map[string]int64),
	}
	
	// Parse each section using specialized helper functions
	s.parseOverviewStats(result, stats)
	s.parseTopStats(result, "top_actions", stats.TopActions)
	s.parseTopStats(result, "top_users", stats.TopUsers)
	s.parseTopStats(result, "top_resources", stats.TopResources)
	
	return stats
}

// parseOverviewStats extracts overview statistics (total, success rate, etc.)
func (s *Service) parseOverviewStats(result map[string]any, stats *AuditStats) {
	overview := s.extractArray(result, "overview")
	
	if len(overview) == 0 {
		s.logger.Debugf("No overview found or empty array")
		return
	}
	
	s.logger.Debugf("Parsing overview: %+v", overview[0])
	
	// Extract overview data - handle both map[string]any and primitive.M
	overviewData := s.convertToStringAnyMap(overview[0])
	if overviewData == nil {
		s.logger.Debugf("Failed to parse overview[0], type: %T, value: %+v", overview[0], overview[0])
		return
	}
	
	// Extract metrics
	totalEntries := getInt64(overviewData, "total_entries")
	successCount := getInt64(overviewData, "success_count")
	totalDuration := getInt64(overviewData, "total_duration")
	
	s.logger.Debugf("Overview data - total: %d, success: %d, duration: %d", totalEntries, successCount, totalDuration)
	s.logger.Debugf("Raw overview values - total_entries: %+v (type: %T), success_count: %+v (type: %T)", 
		overviewData["total_entries"], overviewData["total_entries"],
		overviewData["success_count"], overviewData["success_count"])
	
	// Set stats
	stats.TotalEntries = totalEntries
	if totalEntries > 0 {
		stats.SuccessRate = float64(successCount) / float64(totalEntries)
		stats.ErrorRate = float64(totalEntries-successCount) / float64(totalEntries)
		stats.AverageResponse = totalDuration / totalEntries
	}
}

// parseTopStats extracts top N statistics (actions, users, resources)
func (s *Service) parseTopStats(result map[string]any, fieldName string, targetMap map[string]int64) {
	items := s.extractArray(result, fieldName)
	
	for _, item := range items {
		itemData := s.convertToStringAnyMap(item)
		if itemData == nil {
			continue
		}
		
		if name, ok := itemData["_id"].(string); ok {
			targetMap[name] = getInt64(itemData, "count")
		}
	}
}

// extractArray extracts array from aggregation result, handling both []any and primitive.A types
func (s *Service) extractArray(result map[string]any, fieldName string) []any {
	value, exists := result[fieldName]
	if !exists {
		return nil
	}
	
	switch arr := value.(type) {
	case []any:
		return arr
	case primitive.A:
		return []any(arr)
	default:
		s.logger.Debugf("Unexpected %s type: %T, value: %+v", fieldName, arr, arr)
		return nil
	}
}

// convertToStringAnyMap converts various MongoDB result types to map[string]any
func (s *Service) convertToStringAnyMap(data any) map[string]any {
	switch d := data.(type) {
	case map[string]any:
		return d
	case primitive.M:
		return map[string]any(d)
	default:
		return nil
	}
}


// Utility functions for safe type conversion
func getInt64(m map[string]any, key string) int64 {
	if val, ok := m[key]; ok {
		switch v := val.(type) {
		case int64:
			return v
		case int32:
			return int64(v)
		case int:
			return int64(v)
		case float64:
			return int64(v)
		case primitive.A:
			// Handle array case - should not happen for counts but just in case
			if len(v) > 0 {
				return getInt64Value(v[0])
			}
		default:
			return getInt64Value(v)
		}
	}
	return 0
}

// Helper function to handle primitive type conversion
func getInt64Value(val any) int64 {
	switch v := val.(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case primitive.Decimal128:
		// For now, skip Decimal128 handling as it's complex
		// In practice, MongoDB aggregation $sum should return int/float, not Decimal128
	}
	return 0
}


