package gslb

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// IPHealthManager handles CRUD operations for gslb_ip_health collection
// This is the primary interface for managing IP health state separate from GSLB records
type IPHealthManager struct {
	db     *mongo.Database
	logger *logger.Logger
}

// NewIPHealthManager creates a new IP health manager instance
func NewIPHealthManager(db *mongo.Database, logger *logger.Logger) *IPHealthManager {
	return &IPHealthManager{
		db:     db,
		logger: logger,
	}
}

// AddIP creates a new IP health record in the gslb_ip_health collection
// Called when a client is deployed to a service with GSLB enabled
//
// Parameters:
//   - recordID: Parent GSLB record ObjectID
//   - fqdn: Fully qualified domain name (denormalized for fast DNS queries)
//   - ip: Target IP address
//   - clientID: Client identifier that owns this IP
//   - shardID: Top-level shard (0-127)
//   - subShardID: Sub-shard for load distribution (0-7)
//
// Returns error if IP already exists for the record (unique constraint)
func (ihm *IPHealthManager) AddIP(ctx context.Context, recordID primitive.ObjectID, fqdn, ip, clientID string, shardID, subShardID int) error {
	// Add timeout protection for MongoDB operation
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Validate shard ID bounds (0-127 for top-level, 0-7 for sub-shard)
	if shardID < 0 || shardID >= models.GSLBNumShards {
		return fmt.Errorf("invalid shard ID %d: must be in range [0, %d)", shardID, models.GSLBNumShards)
	}
	if subShardID < 0 || subShardID >= 8 { // Sub-shard range: 0-7
		return fmt.Errorf("invalid sub-shard ID %d: must be in range [0, 8)", subShardID)
	}

	// Create new IP health record with optimistic initial state
	ipHealth := models.NewGSLBIPHealth(recordID, fqdn, ip, clientID, shardID, subShardID)

	// Validate before insert
	if err := ipHealth.Validate(); err != nil {
		return fmt.Errorf("invalid IP health data: %w", err)
	}

	// Insert into MongoDB
	collection := ihm.db.Collection("gslb_ip_health")
	_, err := collection.InsertOne(ctx, ipHealth)
	if err != nil {
		// Check for duplicate key error
		if mongo.IsDuplicateKeyError(err) {
			return fmt.Errorf("IP %s already exists for record %s", ip, recordID.Hex())
		}
		return fmt.Errorf("failed to insert IP health record: %w", err)
	}

	ihm.logger.Infof("Added IP health record: %s for FQDN %s (shard %d/%d)", ip, fqdn, shardID, subShardID)
	return nil
}

// AddIPWithHealth adds a new IP health record with specified initial health state
// Allows manual control over initial health status (for manual drain/maintenance)
//
// Parameters:
//   - recordID: Parent GSLB record ObjectID
//   - fqdn: Fully qualified domain name
//   - ip: Target IP address
//   - clientID: Client identifier (can be empty string)
//   - shardID: Top-level shard (0-127)
//   - subShardID: Sub-shard (0-7)
//   - initialHealthState: Initial health state (passing/warning/critical)
//
// Returns error if IP already exists or insertion fails
func (ihm *IPHealthManager) AddIPWithHealth(ctx context.Context, recordID primitive.ObjectID, fqdn, ip, clientID string, shardID, subShardID int, initialHealthState models.HealthState) error {
	// Add timeout protection for MongoDB operation
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Validate shard ID bounds (0-127 for top-level, 0-7 for sub-shard)
	if shardID < 0 || shardID >= models.GSLBNumShards {
		return fmt.Errorf("invalid shard ID %d: must be in range [0, %d)", shardID, models.GSLBNumShards)
	}
	if subShardID < 0 || subShardID >= 8 { // Sub-shard range: 0-7
		return fmt.Errorf("invalid sub-shard ID %d: must be in range [0, 8)", subShardID)
	}

	collection := ihm.db.Collection("gslb_ip_health")

	now := time.Now()

	// Validate health state (must be one of: passing, warning, critical)
	if initialHealthState != models.HealthStatePassing &&
		initialHealthState != models.HealthStateWarning &&
		initialHealthState != models.HealthStateCritical {
		// Default to passing if invalid state provided
		initialHealthState = models.HealthStatePassing
	}

	// Create initial status history entry
	initialHistory := models.GSLBStatusHistory{
		State:        initialHealthState.String(),
		DateTime:     now,
		ResponseCode: 0,
		ResponseTime: 0,
	}

	ipHealth := models.GSLBIPHealth{
		RecordID:         recordID,
		FQDN:             fqdn,
		IP:               ip,
		ClientID:         clientID,
		ShardID:          shardID,
		SubShardID:       subShardID,
		HealthState:      initialHealthState,
		BackoffUntil:     time.Time{}, // No backoff initially (zero time)
		CurrentBackoff:   0,
		LastStatusChange: now,
		StatusHistory:    []models.GSLBStatusHistory{initialHistory},
		IsManual:         true, // Manually added by admin
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	_, err := collection.InsertOne(ctx, ipHealth)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return fmt.Errorf("IP %s already exists for record %s", ip, recordID.Hex())
		}
		return fmt.Errorf("failed to insert IP health record: %w", err)
	}

	ihm.logger.Infof("Added IP health record: %s for FQDN %s (shard %d/%d, initial state: %s)", ip, fqdn, shardID, subShardID, initialHealthState.String())
	return nil
}

// RemoveIP deletes an IP health record from the gslb_ip_health collection
// Called when a client is undeployed from a service
//
// Parameters:
//   - recordID: Parent GSLB record ObjectID
//   - ip: Target IP address to remove
//
// Returns error if IP not found or deletion fails
func (ihm *IPHealthManager) RemoveIP(ctx context.Context, recordID primitive.ObjectID, ip string) error {
	// Add timeout protection for MongoDB operation
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	collection := ihm.db.Collection("gslb_ip_health")

	filter := bson.M{
		"record_id": recordID,
		"ip":        ip,
	}

	result, err := collection.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to delete IP health record: %w", err)
	}

	if result.DeletedCount == 0 {
		return fmt.Errorf("IP %s not found for record %s", ip, recordID.Hex())
	}

	ihm.logger.Infof("Removed IP health record: %s for record %s", ip, recordID.Hex())
	return nil
}

// RemoveIPsByClientID removes all IP health records for a specific client
// Called when a client is completely removed from the system
//
// Parameters:
//   - clientID: Client identifier
//
// Returns number of IPs removed and any error
func (ihm *IPHealthManager) RemoveIPsByClientID(ctx context.Context, clientID string) (int64, error) {
	// Add timeout protection for MongoDB operation
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	collection := ihm.db.Collection("gslb_ip_health")

	filter := bson.M{"client_id": clientID}

	result, err := collection.DeleteMany(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("failed to delete IPs for client %s: %w", clientID, err)
	}

	ihm.logger.Infof("Removed %d IP health records for client %s", result.DeletedCount, clientID)
	return result.DeletedCount, nil
}

// DeleteByRecordID removes all IP health records for a deleted GSLB record
// Called when a GSLB record is deleted (cascading delete)
//
// Parameters:
//   - recordID: Parent GSLB record ObjectID
//
// Returns number of IPs removed and any error
func (ihm *IPHealthManager) DeleteByRecordID(ctx context.Context, recordID primitive.ObjectID) (int64, error) {
	// Add timeout protection for MongoDB operation
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	collection := ihm.db.Collection("gslb_ip_health")

	filter := bson.M{"record_id": recordID}

	result, err := collection.DeleteMany(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("failed to delete IPs for record %s: %w", recordID.Hex(), err)
	}

	ihm.logger.Infof("Removed %d IP health records for GSLB record %s", result.DeletedCount, recordID.Hex())
	return result.DeletedCount, nil
}

// GetIPsForShard fetches all IP health records for owned shards
// Used by health checker to get IPs that need probing
//
// Parameters:
//   - shardID: Top-level shard (0-127)
//   - subShardID: Sub-shard (0-7)
//
// Returns slice of IP health records and any error
func (ihm *IPHealthManager) GetIPsForShard(ctx context.Context, shardID, subShardID int) ([]models.GSLBIPHealth, error) {
	// Add timeout for each operation
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	collection := ihm.db.Collection("gslb_ip_health")

	filter := bson.M{
		"shard_id":     shardID,
		"sub_shard_id": subShardID,
	}

	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to query IPs for shard %d/%d: %w", shardID, subShardID, err)
	}
	defer cursor.Close(ctx)

	var ips []models.GSLBIPHealth
	if err := cursor.All(ctx, &ips); err != nil {
		return nil, fmt.Errorf("failed to decode IP health records: %w", err)
	}

	return ips, nil
}

// GetIPsReadyToProbe filters IPs excluding circuit breaker backoffs
// Used by health checker to get only IPs that should be probed this cycle
//
// Parameters:
//   - shardID: Top-level shard (0-127)
//   - subShardID: Sub-shard (0-7)
//
// Returns slice of IP health records that are ready to probe
func (ihm *IPHealthManager) GetIPsReadyToProbe(ctx context.Context, shardID, subShardID int) ([]models.GSLBIPHealth, error) {
	// Add timeout for each operation
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	collection := ihm.db.Collection("gslb_ip_health")

	now := time.Now()

	filter := bson.M{
		"shard_id":     shardID,
		"sub_shard_id": subShardID,
		"$or": []bson.M{
			{"backoff_until": bson.M{"$exists": false}}, // No backoff set
			{"backoff_until": bson.M{"$lte": now}},      // Backoff expired
			{"backoff_until": primitive.DateTime(0)},    // Zero time (no backoff)
		},
	}

	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to query ready IPs for shard %d/%d: %w", shardID, subShardID, err)
	}
	defer cursor.Close(ctx)

	var ips []models.GSLBIPHealth
	if err := cursor.All(ctx, &ips); err != nil {
		return nil, fmt.Errorf("failed to decode IP health records: %w", err)
	}

	return ips, nil
}

// GetIPsByFQDN fetches all IP health records for a specific FQDN
// Used by DNS API to get healthy IPs for DNS responses
//
// Parameters:
//   - fqdn: Fully qualified domain name
//   - healthyOnly: If true, only return IPs with healthy=true (excludes critical state)
//
// Returns slice of IP health records
func (ihm *IPHealthManager) GetIPsByFQDN(ctx context.Context, fqdn string, healthyOnly bool) ([]models.GSLBIPHealth, error) {
	// Add timeout for each operation
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	collection := ihm.db.Collection("gslb_ip_health")

	filter := bson.M{"fqdn": fqdn}

	if healthyOnly {
		// Only return passing and warning states (exclude critical)
		filter["health_state"] = bson.M{"$ne": models.HealthStateCritical}
	}

	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to query IPs for FQDN %s: %w", fqdn, err)
	}
	defer cursor.Close(ctx)

	var ips []models.GSLBIPHealth
	if err := cursor.All(ctx, &ips); err != nil {
		return nil, fmt.Errorf("failed to decode IP health records: %w", err)
	}

	return ips, nil
}

// GetIPsByRecordID fetches all IP health records for a specific GSLB record
// Used by API list operations and record detail views
//
// Parameters:
//   - recordID: Parent GSLB record ObjectID
//
// Returns slice of IP health records
func (ihm *IPHealthManager) GetIPsByRecordID(ctx context.Context, recordID primitive.ObjectID) ([]models.GSLBIPHealth, error) {
	// Add timeout for each operation
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	collection := ihm.db.Collection("gslb_ip_health")

	filter := bson.M{"record_id": recordID}

	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to query IPs for record %s: %w", recordID.Hex(), err)
	}
	defer cursor.Close(ctx)

	var ips []models.GSLBIPHealth
	if err := cursor.All(ctx, &ips); err != nil {
		return nil, fmt.Errorf("failed to decode IP health records: %w", err)
	}

	return ips, nil
}

// GetIPsByRecordIDs retrieves all IPs for multiple record IDs in a single batch query
// This solves the N+1 query problem: 5,000 records × 1 query each = 5,000 queries
// OPTIMIZED: Uses simple find() instead of aggregation for 100x performance improvement
//
// Parameters:
//   - recordIDs: List of GSLB record ObjectIDs to query
//
// Returns map[recordID][]IPHealth for fast lookup during Time Wheel slot execution
func (ihm *IPHealthManager) GetIPsByRecordIDs(ctx context.Context, recordIDs []primitive.ObjectID) (map[primitive.ObjectID][]models.GSLBIPHealth, error) {
	if len(recordIDs) == 0 {
		return make(map[primitive.ObjectID][]models.GSLBIPHealth), nil
	}

	// PERFORMANCE DEBUG: Track total query time
	queryStart := time.Now()

	// Timeout for MongoDB Atlas: Network latency + query execution
	// Atlas requires longer timeout due to geographic distance and network variability
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	collection := ihm.db.Collection("gslb_ip_health")

	// OPTIMIZED: Simple find with $in operator (uses index, no aggregation overhead)
	filter := bson.M{
		"record_id": bson.M{"$in": recordIDs},
	}

	// CRITICAL OPTIMIZATION: Exclude status_history array
	// status_history can be 100+ entries × 840 IPs = 10MB+ data transfer!
	// Only fetch fields needed for health checking (reduces transfer by 90%)
	projection := bson.M{
		"status_history": 0, // EXCLUDE - massive array slows down query
	}

	opts := options.Find().SetProjection(projection)

	// PERFORMANCE DEBUG: Track Find() execution time
	findStart := time.Now()
	cursor, err := collection.Find(ctx, filter, opts)
	findDuration := time.Since(findStart)

	if err != nil {
		return nil, fmt.Errorf("batch IP query failed: %w", err)
	}
	defer cursor.Close(ctx)

	// Group results in-memory (faster than MongoDB $group aggregation)
	result := make(map[primitive.ObjectID][]models.GSLBIPHealth)

	// PERFORMANCE DEBUG: Track cursor iteration time
	decodingStart := time.Now()
	ipCount := 0
	for cursor.Next(ctx) {
		var ip models.GSLBIPHealth
		if err := cursor.Decode(&ip); err != nil {
			ihm.logger.Warnf("Failed to decode IP health record: %v", err)
			continue
		}

		// Group by record_id in-memory
		result[ip.RecordID] = append(result[ip.RecordID], ip)
		ipCount++
	}
	decodingDuration := time.Since(decodingStart)

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("cursor error during batch IP query: %w", err)
	}

	// Performance monitoring: Warn if query takes too long (indicates network/performance issue)
	totalDuration := time.Since(queryStart)
	if totalDuration > 2*time.Second {
		ihm.logger.Warnf("Slow MongoDB query detected | Total=%v Find()=%v Decoding=%v | Records=%d IPs=%d",
			totalDuration, findDuration, decodingDuration, len(recordIDs), ipCount)
	}

	return result, nil
}

// GetRecordsByIDs fetches GSLB records by their IDs and returns them as a map
// Used by executeCurrentSlot to get fresh probe config from DB on every tick
// This ensures probe config changes from other controllers are picked up immediately
// without requiring inter-controller notification
func (ihm *IPHealthManager) GetRecordsByIDs(ctx context.Context, recordIDs []primitive.ObjectID) (map[primitive.ObjectID]*models.GSLBRecord, error) {
	if len(recordIDs) == 0 {
		return make(map[primitive.ObjectID]*models.GSLBRecord), nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	collection := ihm.db.Collection("gslb_records")

	filter := bson.M{
		"_id": bson.M{"$in": recordIDs},
	}

	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("batch record query failed: %w", err)
	}
	defer cursor.Close(ctx)

	result := make(map[primitive.ObjectID]*models.GSLBRecord, len(recordIDs))
	for cursor.Next(ctx) {
		var record models.GSLBRecord
		if err := cursor.Decode(&record); err != nil {
			ihm.logger.Warnf("Failed to decode GSLB record: %v", err)
			continue
		}
		result[record.ID] = &record
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("cursor error during batch record query: %w", err)
	}

	return result, nil
}

// UpdateHealthState updates the health state and related fields for an IP
// Called by health checker after probing
//
// Parameters:
//   - ip: Target IP address
//   - healthState: New health state (passing/warning/critical)
//   - responseCode: HTTP status code or 0 for connection errors
//   - responseTime: Response time in seconds
//
// Returns error if update fails
func (ihm *IPHealthManager) UpdateHealthState(ctx context.Context, ip string, healthState models.HealthState, responseCode int, responseTime float64) error {
	// Add timeout protection for MongoDB operation
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	collection := ihm.db.Collection("gslb_ip_health")

	now := time.Now()

	// Build status history entry
	historyEntry := models.GSLBStatusHistory{
		State:        healthState.String(),
		DateTime:     now,
		ResponseCode: responseCode,
		ResponseTime: responseTime,
	}

	// Update with $set for current state and $push for history (with $slice to limit to 100)
	update := bson.M{
		"$set": bson.M{
			"health_state":       healthState,
			"last_status_change": now,
			"updated_at":         now,
		},
		"$push": bson.M{
			"status_history": bson.M{
				"$each":  []models.GSLBStatusHistory{historyEntry},
				"$slice": -models.GSLBMaxStatusHistorySize, // Keep only last 50 entries
			},
		},
	}

	filter := bson.M{"ip": ip}

	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to update health state for IP %s: %w", ip, err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("IP %s not found", ip)
	}

	return nil
}

// UpdateCircuitBreaker updates circuit breaker state for an IP
// Called by health checker when applying backoff
//
// Parameters:
//   - ip: Target IP address
//   - backoffUntil: Time until which probes should be skipped
//   - currentBackoff: Current backoff duration in seconds
//
// Returns error if update fails
func (ihm *IPHealthManager) UpdateCircuitBreaker(ctx context.Context, ip string, backoffUntil time.Time, currentBackoff int64) error {
	// Add timeout protection for MongoDB operation
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	collection := ihm.db.Collection("gslb_ip_health")

	update := bson.M{
		"$set": bson.M{
			"backoff_until":   backoffUntil,
			"current_backoff": currentBackoff,
			"updated_at":      time.Now(),
		},
	}

	filter := bson.M{"ip": ip}

	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to update circuit breaker for IP %s: %w", ip, err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("IP %s not found", ip)
	}

	return nil
}

// ResetCircuitBreaker clears circuit breaker state for an IP
// Called when IP transitions to warning or passing state
//
// Parameters:
//   - ip: Target IP address
//
// Returns error if update fails
func (ihm *IPHealthManager) ResetCircuitBreaker(ctx context.Context, ip string) error {
	// Add timeout protection for MongoDB operation
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	collection := ihm.db.Collection("gslb_ip_health")

	update := bson.M{
		"$set": bson.M{
			"backoff_until":   time.Time{},
			"current_backoff": 0,
			"updated_at":      time.Now(),
		},
	}

	filter := bson.M{"ip": ip}

	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to reset circuit breaker for IP %s: %w", ip, err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("IP %s not found", ip)
	}

	return nil
}

// CountIPsByFQDN returns the total number of IPs for a FQDN and how many are healthy
// Used for monitoring and alerting
//
// Returns (totalCount, healthyCount, error)
func (ihm *IPHealthManager) CountIPsByFQDN(ctx context.Context, fqdn string) (int64, int64, error) {
	// Add timeout for each operation
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	collection := ihm.db.Collection("gslb_ip_health")

	// Total count
	totalCount, err := collection.CountDocuments(ctx, bson.M{"fqdn": fqdn})
	if err != nil {
		return 0, 0, fmt.Errorf("failed to count total IPs: %w", err)
	}

	// Healthy count (passing + warning, exclude critical)
	healthyCount, err := collection.CountDocuments(ctx, bson.M{
		"fqdn":         fqdn,
		"health_state": bson.M{"$ne": models.HealthStateCritical},
	})
	if err != nil {
		return 0, 0, fmt.Errorf("failed to count healthy IPs: %w", err)
	}

	return totalCount, healthyCount, nil
}

// GetHealthSummary returns health state distribution for a FQDN
// Used for monitoring dashboards
//
// Returns map[HealthState]count
func (ihm *IPHealthManager) GetHealthSummary(ctx context.Context, fqdn string) (map[models.HealthState]int, error) {
	// Add timeout for each operation
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	collection := ihm.db.Collection("gslb_ip_health")

	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.M{"fqdn": fqdn}}},
		bson.D{{Key: "$group", Value: bson.M{
			"_id":   "$health_state",
			"count": bson.M{"$sum": 1},
		}}},
	}

	cursor, err := collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate health summary: %w", err)
	}
	defer cursor.Close(ctx)

	summary := make(map[models.HealthState]int)

	// Initialize all states to 0
	summary[models.HealthStatePassing] = 0
	summary[models.HealthStateWarning] = 0
	summary[models.HealthStateCritical] = 0

	// Populate from aggregation results
	for cursor.Next(ctx) {
		var result struct {
			ID    string `bson:"_id"`
			Count int    `bson:"count"`
		}
		if err := cursor.Decode(&result); err != nil {
			return nil, fmt.Errorf("failed to decode aggregation result: %w", err)
		}

		healthState := models.HealthState(result.ID)
		summary[healthState] = result.Count
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("cursor error: %w", err)
	}

	return summary, nil
}

// ============================================================================
// TIME WHEEL QUERY METHODS
// ============================================================================

// GetRecordsByShards fetches GSLB records for owned shards filtered by probe interval
// Used by TimeWheel to load records during initialization and rebalancing
//
// Parameters:
//   - shards: List of owned shards (each shard has shard_id and sub_shard_id)
//   - interval: Probe interval in seconds (10, 20, 30, 60, 90, 120, 180, 300)
//
// # Returns slice of GSLB records that match the shard ownership AND interval
//
// This implements intelligent shard distribution:
//   - Shards are assigned across Time Wheel scheduling
//   - Time Wheel queries records filtered by probe.interval
//   - Records naturally distributed by interval in Time Wheel slots
//   - Balanced load across Time Wheel slots
//
// Example: Shard 0/0 has records with 10s, 30s, 60s intervals
//   - Time Wheel schedules 10s records every 10 seconds
//   - Time Wheel schedules 30s records every 30 seconds
func (ihm *IPHealthManager) GetRecordsByShards(ctx context.Context, shards []ShardOwnership, interval int) ([]models.GSLBRecord, error) {
	// PERFORMANCE: Add timeout for query
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if len(shards) == 0 {
		return []models.GSLBRecord{}, nil
	}

	collection := ihm.db.Collection("gslb_records")

	// Build shard filter: $or condition for all owned shards
	// CRITICAL: gslb_records collection only has shard_id (no sub_shard_id)
	// Extract unique shard IDs only (ignore sub_shard_id for gslb_records query)
	uniqueShardIDs := make(map[int]bool)
	for _, shard := range shards {
		uniqueShardIDs[shard.ShardID] = true
	}

	shardConditions := make([]bson.M, 0, len(uniqueShardIDs))
	for shardID := range uniqueShardIDs {
		shardConditions = append(shardConditions, bson.M{
			"shard_id": shardID,
		})
	}

	// Use $and to properly combine shard filter + interval filter + enabled
	// NOTE: gslb_records has "enabled" at top level (not probe.enabled)
	filter := bson.M{
		"$and": []bson.M{
			{"$or": shardConditions},     // Match any owned shard
			{"enabled": true},            // Record must be enabled (top-level field)
			{"probe.interval": interval}, // Match specific interval
		},
	}

	// DEBUG: Log query details
	ihm.logger.Debugf("Querying records: interval=%ds, unique_shards=%d, enabled=true", interval, len(uniqueShardIDs))

	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to query records for %d shards with interval %ds: %w", len(shards), interval, err)
	}
	defer cursor.Close(ctx)

	var records []models.GSLBRecord
	if err := cursor.All(ctx, &records); err != nil {
		return nil, fmt.Errorf("failed to decode GSLB records: %w", err)
	}

	if len(records) == 0 {
		ihm.logger.Debugf("No records found for interval %ds from %d shards (filter may not match)", interval, len(shards))
	} else {
		ihm.logger.Debugf("Loaded %d records for interval %ds from %d shards", len(records), interval, len(shards))
	}
	return records, nil
}
