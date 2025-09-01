package audit

import (
	"context"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Service handles audit logging operations
type Service struct {
	db     *db.AppContext
	logger *logger.Logger
}

// NewService creates a new audit service
func NewService(db *db.AppContext, logger *logger.Logger) *Service {
	return &Service{
		db:     db,
		logger: logger,
	}
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
		s.logger.Errorf("Failed to store audit entry: %v", err)
		// In production, consider async retry mechanism here
		return err
	}

	s.logger.Debugf("Stored audit entry: action=%s, resource=%s/%s, user=%s",
		entry.Action, entry.ResourceType, entry.ResourceID, entry.Username)

	return nil
}

// StoreAsync stores audit entry asynchronously (non-blocking)
func (s *Service) StoreAsync(entry *models.AuditEntry) {
	go func() {
		if err := s.Store(entry); err != nil {
			s.logger.Errorf("Async audit store failed: %v", err)
		}
	}()
}

// Helper functions for gin context

// GetAuditEntry retrieves the audit entry from gin context
func GetAuditEntry(c *gin.Context) *models.AuditEntry {
	if audit, exists := c.Get("audit_entry"); exists {
		return audit.(*models.AuditEntry)
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
		audit.Action = action
	}
}

// SetAuditResource sets resource information in the audit entry
func SetAuditResource(c *gin.Context, resourceType, resourceID, resourceName, project string) {
	if audit := GetAuditEntry(c); audit != nil {
		audit.ResourceType = resourceType
		audit.ResourceID = resourceID
		audit.ResourceName = resourceName
		audit.Project = project
	}
}

// SetAuditError sets error information in the audit entry
func SetAuditError(c *gin.Context, errorMsg string) {
	if audit := GetAuditEntry(c); audit != nil {
		audit.Success = false
		audit.ErrorMessage = errorMsg
	}
	c.Set("audit_error", errorMsg)
}

// SetAuditSuccess sets success status in the audit entry
func SetAuditSuccess(c *gin.Context, success bool) {
	if audit := GetAuditEntry(c); audit != nil {
		audit.Success = success
	}
}

// SetAuditChanges sets the changes map for PUT requests
func SetAuditChanges(c *gin.Context, changes map[string]interface{}) {
	if audit := GetAuditEntry(c); audit != nil {
		audit.Changes = changes
	}
}

// SetAuditCommand sets the command details for client commands
func SetAuditCommand(c *gin.Context, command map[string]any) {
	if audit := GetAuditEntry(c); audit != nil {
		audit.Command = command
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
		s.logger.Errorf("Failed to query audit entries: %v", err)
		return nil, err
	}
	defer cursor.Close(ctx)
	
	var entries []*models.AuditEntry
	if err = cursor.All(ctx, &entries); err != nil {
		s.logger.Errorf("Failed to decode audit entries: %v", err)
		return nil, err
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
		s.logger.Errorf("Failed to count audit entries: %v", err)
		return 0, err
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
		s.logger.Errorf("Failed to get audit stats: %v", err)
		return nil, err
	}
	defer cursor.Close(ctx)
	
	var results []map[string]any
	if err = cursor.All(ctx, &results); err != nil {
		return nil, err
	}
	
	// Debug log the results
	s.logger.Debugf("Audit stats aggregation results: %+v", results)
	
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
		s.logger.Errorf("Failed to cleanup old audit entries: %v", err)
		return 0, err
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
		filter["username"] = bson.M{"$regex": query.Username, "$options": "i"}
	}
	if query.Action != "" {
		filter["action"] = query.Action
	}
	if query.ResourceType != "" {
		// Support both exact match and prefix match for resource types
		// This allows filtering extensions like "extensions/http_protocol_options" with "extensions"
		filter["resource_type"] = bson.M{"$regex": "^" + query.ResourceType, "$options": "i"}
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
	
	// Parse overview stats - handle both []any and primitive.A
	var overview []any
	if overviewVal, exists := result["overview"]; exists {
		switch ov := overviewVal.(type) {
		case []any:
			overview = ov
		case primitive.A:
			overview = []any(ov)
		default:
			s.logger.Debugf("Unexpected overview type: %T, value: %+v", ov, ov)
		}
	}
	
	if len(overview) > 0 {
		s.logger.Debugf("Parsing overview: %+v", overview[0])
		if overviewData, ok := overview[0].(map[string]any); ok {
			totalEntries := getInt64(overviewData, "total_entries")
			successCount := getInt64(overviewData, "success_count")
			totalDuration := getInt64(overviewData, "total_duration")
			
			s.logger.Debugf("Overview data - total: %d, success: %d, duration: %d", totalEntries, successCount, totalDuration)
			s.logger.Debugf("Raw overview values - total_entries: %+v (type: %T), success_count: %+v (type: %T)", 
				overviewData["total_entries"], overviewData["total_entries"],
				overviewData["success_count"], overviewData["success_count"])
			
			stats.TotalEntries = totalEntries
			if totalEntries > 0 {
				stats.SuccessRate = float64(successCount) / float64(totalEntries)
				stats.ErrorRate = float64(totalEntries-successCount) / float64(totalEntries)
				stats.AverageResponse = totalDuration / totalEntries
			}
		} else if overviewPrimitive, ok := overview[0].(primitive.M); ok {
			// Handle primitive.M (BSON document)
			overviewData := map[string]any(overviewPrimitive)
			totalEntries := getInt64(overviewData, "total_entries")
			successCount := getInt64(overviewData, "success_count")
			totalDuration := getInt64(overviewData, "total_duration")
			
			s.logger.Debugf("Overview data (primitive.M) - total: %d, success: %d, duration: %d", totalEntries, successCount, totalDuration)
			
			stats.TotalEntries = totalEntries
			if totalEntries > 0 {
				stats.SuccessRate = float64(successCount) / float64(totalEntries)
				stats.ErrorRate = float64(totalEntries-successCount) / float64(totalEntries)
				stats.AverageResponse = totalDuration / totalEntries
			}
		} else {
			s.logger.Debugf("Failed to parse overview[0], type: %T, value: %+v", overview[0], overview[0])
		}
	} else {
		s.logger.Debugf("No overview found or empty array")
	}
	
	// Parse top actions - handle both []any and primitive.A
	var topActions []any
	if actionsVal, exists := result["top_actions"]; exists {
		switch ta := actionsVal.(type) {
		case []any:
			topActions = ta
		case primitive.A:
			topActions = []any(ta)
		}
	}
	
	for _, action := range topActions {
		if actionMap, ok := action.(map[string]any); ok {
			if name, ok := actionMap["_id"].(string); ok {
				stats.TopActions[name] = getInt64(actionMap, "count")
			}
		} else if actionPrimitive, ok := action.(primitive.M); ok {
			actionMap := map[string]any(actionPrimitive)
			if name, ok := actionMap["_id"].(string); ok {
				stats.TopActions[name] = getInt64(actionMap, "count")
			}
		}
	}
	
	// Parse top users - handle both []any and primitive.A
	var topUsers []any
	if usersVal, exists := result["top_users"]; exists {
		switch tu := usersVal.(type) {
		case []any:
			topUsers = tu
		case primitive.A:
			topUsers = []any(tu)
		}
	}
	
	for _, user := range topUsers {
		if userMap, ok := user.(map[string]any); ok {
			if name, ok := userMap["_id"].(string); ok {
				stats.TopUsers[name] = getInt64(userMap, "count")
			}
		} else if userPrimitive, ok := user.(primitive.M); ok {
			userMap := map[string]any(userPrimitive)
			if name, ok := userMap["_id"].(string); ok {
				stats.TopUsers[name] = getInt64(userMap, "count")
			}
		}
	}
	
	// Parse top resources - handle both []any and primitive.A
	var topResources []any
	if resourcesVal, exists := result["top_resources"]; exists {
		switch tr := resourcesVal.(type) {
		case []any:
			topResources = tr
		case primitive.A:
			topResources = []any(tr)
		}
	}
	
	for _, resource := range topResources {
		if resourceMap, ok := resource.(map[string]any); ok {
			if name, ok := resourceMap["_id"].(string); ok {
				stats.TopResources[name] = getInt64(resourceMap, "count")
			}
		} else if resourcePrimitive, ok := resource.(primitive.M); ok {
			resourceMap := map[string]any(resourcePrimitive)
			if name, ok := resourceMap["_id"].(string); ok {
				stats.TopResources[name] = getInt64(resourceMap, "count")
			}
		}
	}
	
	return stats
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

func getFloat64(m map[string]any, key string) float64 {
	if val, ok := m[key]; ok {
		switch v := val.(type) {
		case float64:
			return v
		case int64:
			return float64(v)
		case int32:
			return float64(v)
		case int:
			return float64(v)
		}
	}
	return 0
}

