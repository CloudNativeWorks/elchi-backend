package envoys

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

// ErrorSeverity levels for better categorization
type ErrorSeverity string

const (
	SeverityCritical ErrorSeverity = "critical" // Service down, major config issues
	SeverityError    ErrorSeverity = "error"    // Config rejected, partial failures
	SeverityWarning  ErrorSeverity = "warning"  // Deprecated configs, performance issues
	SeverityInfo     ErrorSeverity = "info"     // Configuration changes, notifications
)

// ErrorStatus tracks error lifecycle
type ErrorStatus string

const (
	StatusActive   ErrorStatus = "active"   // Error is current and unresolved
	StatusResolved ErrorStatus = "resolved" // Error has been fixed
	StatusIgnored  ErrorStatus = "ignored"  // User acknowledged but didn't fix
)

// Enhanced error entry with rich metadata
type EnhancedErrorEntry struct {
	ID               string        `bson:"_id" json:"id"`
	Message          string        `bson:"message" json:"message"`
	UserFriendlyMsg  string        `bson:"user_friendly_message" json:"userFriendlyMessage"`
	ResourceType     string        `bson:"resource_type" json:"resourceType"`
	ResourceName     string        `bson:"resource_name" json:"resourceName"`
	ResponseNonce    string        `bson:"response_nonce" json:"responseNonce"`
	NodeID           string        `bson:"nodeid" json:"nodeId"`
	Severity         ErrorSeverity `bson:"severity" json:"severity"`
	Status           ErrorStatus   `bson:"status" json:"status"`
	Timestamp        time.Time     `bson:"timestamp" json:"timestamp"`
	ResolvedAt       *time.Time    `bson:"resolved_at,omitempty" json:"resolvedAt,omitempty"`
	ResolvedBy       string        `bson:"resolved_by,omitempty" json:"resolvedBy,omitempty"`
	FirstOccurred    time.Time     `bson:"first_occurred" json:"firstOccurred"`
	OccurrenceCount  int           `bson:"occurrence_count" json:"occurrenceCount"`
	LastOccurred     time.Time     `bson:"last_occurred" json:"lastOccurred"`
	AffectedServices []string      `bson:"affected_services" json:"affectedServices"`
	SuggestedFix     string        `bson:"suggested_fix,omitempty" json:"suggestedFix,omitempty"`
	DocumentationURL string        `bson:"documentation_url,omitempty" json:"documentationUrl,omitempty"`
}

// Service-level error summary for dashboard
type ServiceErrorSummary struct {
	ServiceName    string     `bson:"service_name" json:"serviceName"`
	Project        string     `bson:"project" json:"project"`
	CriticalCount  int        `bson:"critical_count" json:"criticalCount"`
	ErrorCount     int        `bson:"error_count" json:"errorCount"`
	WarningCount   int        `bson:"warning_count" json:"warningCount"`
	LastErrorTime  *time.Time `bson:"last_error_time,omitempty" json:"lastErrorTime,omitempty"`
	HealthStatus   string     `bson:"health_status" json:"healthStatus"` // healthy, degraded, critical
	ActiveErrorIDs []string   `bson:"active_error_ids" json:"activeErrorIds"`
}

// Enhanced error handler with intelligent categorization
func InsertEnhancedError(ctx context.Context, dbClient *mongo.Database, nodeID, resourceType, errorMsg, nonce string, logger *logger.Logger) {
	name, project, downstreamAddress := GetNodeIDParts(nodeID)
	if downstreamAddress == "" {
		return
	}

	// Analyze error and determine severity + user-friendly message
	severity, userFriendlyMsg, suggestedFix := analyzeError(errorMsg, resourceType)

	// Check if this is a duplicate error (same message + resource)
	errorID := generateErrorID(nodeID, resourceType, errorMsg)

	collection := dbClient.Collection("envoys")
	filter := bson.M{"name": name, "project": project}

	// Check for existing error
	existingErrorFilter := bson.M{
		"name":                name,
		"project":             project,
		"enhanced_errors._id": errorID,
	}

	var existing bson.M
	err := collection.FindOne(ctx, existingErrorFilter).Decode(&existing)

	now := time.Now()

	if errors.Is(err, mongo.ErrNoDocuments) {
		// New error - create entry
		newError := EnhancedErrorEntry{
			ID:               errorID,
			Message:          errorMsg,
			UserFriendlyMsg:  userFriendlyMsg,
			ResourceType:     resourceType,
			ResourceName:     name, // Resource name is the first part of nodeID (listener name)
			ResponseNonce:    nonce,
			NodeID:           nodeID,
			Severity:         severity,
			Status:           StatusActive,
			Timestamp:        now,
			FirstOccurred:    now,
			OccurrenceCount:  1,
			LastOccurred:     now,
			AffectedServices: []string{name},
			SuggestedFix:     suggestedFix,
		}

		// Add new error to array
		update := bson.M{
			"$push": bson.M{
				"enhanced_errors": newError,
			},
		}

		opts := options.Update().SetUpsert(true)
		_, err = collection.UpdateOne(ctx, filter, update, opts)
		if err != nil {
			logger.Errorf("Error adding enhanced error for nodeID %s: %v", nodeID, err)
			return
		}

		// Log critical errors for alerting
		if severity == SeverityCritical {
			logger.Errorf("🚨 CRITICAL ERROR: Service %s - %s", name, userFriendlyMsg)
		}

	} else {
		// Existing error - update occurrence count and timestamp
		update := bson.M{
			"$inc": bson.M{
				"enhanced_errors.$.occurrence_count": 1,
			},
			"$set": bson.M{
				"enhanced_errors.$.last_occurred": now,
				"enhanced_errors.$.status":        StatusActive, // Reactivate if it was resolved
			},
		}

		_, err = collection.UpdateOne(ctx, existingErrorFilter, update)
		if err != nil {
			logger.Errorf("Error updating enhanced error occurrence for nodeID %s: %v", nodeID, err)
		}
	}

	// Update service-level error summary
	updateServiceErrorSummary(ctx, dbClient, name, project, severity, now, errorID, logger)
}

// Simple keyword-based error analysis
func analyzeError(errorMsg, resourceType string) (ErrorSeverity, string, string) {
	// Use keyword-based error categorization
	analyzer := NewErrorAnalyzer()
	return analyzer.AnalyzeError(errorMsg, resourceType)
}

// ErrorAnalyzer provides simple keyword-based error analysis
type ErrorAnalyzer struct {
	criticalKeywords []string
	errorKeywords    []string
	warningKeywords  []string
}

func NewErrorAnalyzer() *ErrorAnalyzer {
	analyzer := &ErrorAnalyzer{
		criticalKeywords: []string{
			"failed to load", "certificate", "no healthy upstream", "connection refused",
			"timeout", "ssl handshake failed", "tls", "authority check failed",
		},
		errorKeywords: []string{
			"not found", "invalid", "exactly one of", "missing", "required", "duplicate",
			"conflicting", "unknown", "unsupported", "malformed",
		},
		warningKeywords: []string{
			"deprecated", "performance", "suboptimal", "should use", "recommended",
		},
	}

	return analyzer
}

func (ea *ErrorAnalyzer) AnalyzeError(errorMsg, resourceType string) (ErrorSeverity, string, string) {
	lowercaseError := strings.ToLower(errorMsg)

	// Simple keyword-based severity detection
	severity := ea.detectSeverityByKeywords(lowercaseError)

	// Generic user-friendly message
	userMsg := "Configuration error detected"

	// Generic fix message
	fixMsg := "Please review the configuration and resolve the issue"

	return severity, userMsg, fixMsg
}

func (ea *ErrorAnalyzer) detectSeverityByKeywords(errorMsg string) ErrorSeverity {
	// Count critical keywords
	criticalScore := 0
	for _, keyword := range ea.criticalKeywords {
		if strings.Contains(errorMsg, keyword) {
			criticalScore += 2
		}
	}

	// Count error keywords
	errorScore := 0
	for _, keyword := range ea.errorKeywords {
		if strings.Contains(errorMsg, keyword) {
			errorScore += 1
		}
	}

	// Count warning keywords
	warningScore := 0
	for _, keyword := range ea.warningKeywords {
		if strings.Contains(errorMsg, keyword) {
			warningScore += 1
		}
	}

	// Determine severity based on scores
	if criticalScore >= 2 {
		return SeverityCritical
	} else if criticalScore > 0 || errorScore >= 2 {
		return SeverityError
	} else if warningScore > 0 {
		return SeverityWarning
	}

	return SeverityError // Default
}

// Removed complex message generation functions - keeping it simple
// No need for detailed templates, fixes, or documentation URLs for every possible error

// Generate unique error ID for deduplication
func generateErrorID(nodeID, resourceType, errorMsg string) string {
	// Create a consistent hash of key error components
	content := nodeID + "::" + resourceType + "::" + strings.TrimSpace(errorMsg)
	// Simple hash - in production, use crypto/md5 or similar
	hash := 0
	for _, char := range content {
		hash = (hash*31 + int(char)) % 1000000
	}
	return fmt.Sprintf("err_%d", hash)
}

// Resource name is extracted from nodeID format: "resource-name::project::downstream-address"
// No need for error message parsing since nodeID already contains the resource name

// AutoResolveAllErrors optimistically resolves all active errors for a node
// If config is still problematic, Envoy will NACK and create new errors
// Also increments version to force Envoy re-evaluation of the same config
func AutoResolveAllErrors(ctx context.Context, dbClient *mongo.Database, nodeID string, logger *logger.Logger) {
	name, project, _ := GetNodeIDParts(nodeID)
	if name == "" || project == "" {
		logger.Debugf("Invalid nodeID format for auto-resolve: %s", nodeID)
		return
	}

	logger.Debugf("🔄 AUTO-RESOLVE: Optimistically resolving errors for node %s (name=%s, project=%s)", nodeID, name, project)

	// Update all active errors to resolved status using pipeline update
	collection := dbClient.Collection("envoys")
	now := time.Now()

	// Use aggregation pipeline to update all matching array elements
	pipeline := []bson.M{
		{
			"$set": bson.M{
				"enhanced_errors": bson.M{
					"$map": bson.M{
						"input": "$enhanced_errors",
						"as":    "error",
						"in": bson.M{
							"$cond": bson.M{
								"if": bson.M{"$eq": []interface{}{"$$error.status", string(StatusActive)}},
								"then": bson.M{
									"$mergeObjects": []interface{}{
										"$$error",
										bson.M{
											"status":      string(StatusResolved),
											"resolved_at": now,
											"resolved_by": "auto_resolve_snapshot",
										},
									},
								},
								"else": "$$error",
							},
						},
					},
				},
			},
		},
	}

	logger.Debugf("🔄 AUTO-RESOLVE: Executing MongoDB update for name=%s, project=%s", name, project)

	result, err := collection.UpdateOne(ctx,
		bson.M{
			"name":                   name,
			"project":                project,
			"enhanced_errors.status": StatusActive,
		},
		pipeline,
	)
	if err != nil {
		logger.Errorf("🔄 AUTO-RESOLVE ERROR: Failed to resolve errors for %s: %v", nodeID, err)
		logger.Errorf("🔄 AUTO-RESOLVE ERROR DETAILS: name=%s, project=%s, collection=%s", name, project, collection.Name())
		return
	}

	logger.Debugf("🔄 AUTO-RESOLVE: MongoDB update completed for %s, modified=%d", nodeID, result.ModifiedCount)

	if result.ModifiedCount > 0 {
		logger.Infof("🔄 AUTO-RESOLVE SUCCESS: Optimistically resolved errors for %s (modified: %d)", nodeID, result.ModifiedCount)

		// Also update service error summary to clear counters
		updateServiceErrorSummaryOnResolve(ctx, dbClient, name, project, logger)
	} else {
		logger.Debugf("🔄 AUTO-RESOLVE: No active errors found for %s", nodeID)
	}
}

// Update service-level error summary for dashboard
func updateServiceErrorSummary(ctx context.Context, dbClient *mongo.Database, serviceName, project string, severity ErrorSeverity, errorTime time.Time, errorID string, logger *logger.Logger) {
	collection := dbClient.Collection("service_error_summary")
	filter := bson.M{"service_name": serviceName, "project": project}

	// Build update based on severity
	update := bson.M{
		"$set": bson.M{
			"service_name":    serviceName,
			"project":         project,
			"last_error_time": errorTime,
		},
		"$addToSet": bson.M{
			"active_error_ids": errorID,
		},
	}

	// Increment appropriate counter
	switch severity {
	case SeverityCritical:
		update["$inc"] = bson.M{"critical_count": 1}
		update["$set"].(bson.M)["health_status"] = "critical"
	case SeverityError:
		update["$inc"] = bson.M{"error_count": 1}
		// Only update health status if not already critical
		update["$setOnInsert"] = bson.M{"health_status": "degraded"}
	case SeverityWarning:
		update["$inc"] = bson.M{"warning_count": 1}
	}

	opts := options.Update().SetUpsert(true)
	_, err := collection.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		logger.Errorf("Error updating service error summary for %s: %v", serviceName, err)
	}
}

// Update service error summary when errors are resolved
func updateServiceErrorSummaryOnResolve(ctx context.Context, dbClient *mongo.Database, serviceName, project string, logger *logger.Logger) {
	collection := dbClient.Collection("service_error_summary")
	filter := bson.M{"service_name": serviceName, "project": project}

	// Reset counters and update health status
	update := bson.M{
		"$set": bson.M{
			"critical_count":   0,
			"error_count":      0,
			"warning_count":    0,
			"health_status":    "healthy",
			"active_error_ids": []string{},
		},
	}

	_, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		logger.Errorf("Error updating service error summary on resolve for %s: %v", serviceName, err)
	}
}
