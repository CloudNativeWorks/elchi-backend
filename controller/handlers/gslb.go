package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/gslb"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// GSLBHandler handles GSLB record CRUD operations
type GSLBHandler struct {
	db         *mongo.Database
	logger     *logger.Logger
	gslbSystem *gslb.System // GSLB system for triggering Time Wheel reloads
}

// NewGSLBHandler creates a new GSLB handler
func NewGSLBHandler(appContext *db.AppContext, gslbSystem *gslb.System) *GSLBHandler {
	return &GSLBHandler{
		db:         appContext.Client,
		logger:     logger.NewLogger("gslb/gslb-handler"),
		gslbSystem: gslbSystem,
	}
}

// ListGSLBRecords returns all GSLB records for a project with IP statistics
// GET /api/v3/gslb?project=X&page=1&limit=10&search=fqdn&status=enabled&probe_type=https&probe_interval=30&ttl=60
// Uses aggregation to join with gslb_ip_health collection
func (h *GSLBHandler) ListGSLBRecords(c *gin.Context) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "project parameter is required",
		})
		return
	}

	// Get user details from context (using standard helper)
	_, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "User not authenticated",
		})
		return
	}

	// Parse pagination parameters (default: page=1, limit=10)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))

	// Enforce limit constraints (max 100)
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	if page <= 0 {
		page = 1
	}

	// Optional filters
	search := strings.TrimSpace(c.Query("search"))      // Search in FQDN
	statusFilter := strings.ToLower(c.Query("status"))  // enabled/disabled
	probeType := strings.ToLower(c.Query("probe_type")) // http/https/tcp
	probeInterval := c.Query("probe_interval")          // 10/20/30/60/90/120/180/300
	ttlFilter := c.Query("ttl")                         // TTL value

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	collection := h.db.Collection("gslb_records")

	// Build match filter
	matchFilter := bson.D{
		{Key: "project", Value: project},
	}

	// Add FQDN search filter (case-insensitive regex)
	if search != "" {
		matchFilter = append(matchFilter, bson.E{
			Key: "fqdn",
			Value: bson.D{
				{Key: "$regex", Value: search},
				{Key: "$options", Value: "i"}, // case-insensitive
			},
		})
	}

	// Add status filter
	switch statusFilter {
	case "enabled":
		matchFilter = append(matchFilter, bson.E{Key: "enabled", Value: true})
	case "disabled":
		matchFilter = append(matchFilter, bson.E{Key: "enabled", Value: false})
		// default: no filter (show all)
	}

	// Add probe type filter
	if probeType != "" {
		if probeType == "http" || probeType == "https" || probeType == "tcp" {
			matchFilter = append(matchFilter, bson.E{Key: "probe.type", Value: probeType})
		}
	}

	// Add probe interval filter
	if probeInterval != "" {
		if interval, err := strconv.Atoi(probeInterval); err == nil {
			matchFilter = append(matchFilter, bson.E{Key: "probe.interval", Value: interval})
		}
	}

	// Add TTL filter
	if ttlFilter != "" {
		if ttl, err := strconv.Atoi(ttlFilter); err == nil {
			matchFilter = append(matchFilter, bson.E{Key: "ttl", Value: uint32(ttl)})
		}
	}

	// Calculate skip for pagination
	skip := (page - 1) * limit

	// Build aggregation pipeline
	pipeline := mongo.Pipeline{
		// Stage 1: Match filters
		{{Key: "$match", Value: matchFilter}},

		// Stage 2: Lookup IPs from gslb_ip_health collection
		// OPTIMIZATION: Use pipeline to exclude status_history (can be 100+ entries per IP)
		{{Key: "$lookup", Value: bson.D{
			{Key: "from", Value: "gslb_ip_health"},
			{Key: "localField", Value: "_id"},
			{Key: "foreignField", Value: "record_id"},
			{Key: "pipeline", Value: bson.A{
				bson.D{{Key: "$project", Value: bson.D{
					{Key: "status_history", Value: 0}, // Exclude - massive array
				}}},
			}},
			{Key: "as", Value: "ips"},
		}}},

		// Stage 3: Add computed fields (total_ips, healthy_ips, unhealthy_ips)
		// healthy_ips = count where health_state != "critical"
		// unhealthy_ips = count where health_state == "critical"
		{{Key: "$addFields", Value: bson.D{
			{Key: "total_ips", Value: bson.D{{Key: "$size", Value: "$ips"}}},
			{Key: "healthy_ips", Value: bson.D{
				{Key: "$size", Value: bson.D{
					{Key: "$filter", Value: bson.D{
						{Key: "input", Value: "$ips"},
						{Key: "as", Value: "ip"},
						{Key: "cond", Value: bson.D{{Key: "$ne", Value: []any{"$$ip.health_state", "critical"}}}},
					}},
				}},
			}},
			{Key: "unhealthy_ips", Value: bson.D{
				{Key: "$size", Value: bson.D{
					{Key: "$filter", Value: bson.D{
						{Key: "input", Value: "$ips"},
						{Key: "as", Value: "ip"},
						{Key: "cond", Value: bson.D{{Key: "$eq", Value: []any{"$$ip.health_state", "critical"}}}},
					}},
				}},
			}},
		}}},

		// Stage 4: Sort by fqdn ascending (A to Z)
		{{Key: "$sort", Value: bson.D{{Key: "fqdn", Value: 1}}}},

		// Stage 5: Facet for pagination + total count
		{{Key: "$facet", Value: bson.D{
			{Key: "metadata", Value: bson.A{
				bson.D{{Key: "$count", Value: "total"}},
			}},
			{Key: "records", Value: bson.A{
				bson.D{{Key: "$skip", Value: skip}},
				bson.D{{Key: "$limit", Value: limit}},
				// Project to include all fields except ips, and rename _id to id
				bson.D{{Key: "$project", Value: bson.D{
					{Key: "id", Value: "$_id"},
					{Key: "fqdn", Value: 1},
					{Key: "service_id", Value: 1},
					{Key: "project", Value: 1},
					{Key: "version", Value: 1},
					{Key: "zone", Value: 1},
					{Key: "shard_id", Value: 1},
					{Key: "enabled", Value: 1},
					{Key: "ttl", Value: 1},
					{Key: "total_ips", Value: 1},
					{Key: "healthy_ips", Value: 1},
					{Key: "unhealthy_ips", Value: 1},
					{Key: "probe", Value: 1},
					{Key: "created_at", Value: 1},
					{Key: "updated_at", Value: 1},
					{Key: "created_by", Value: 1},
					{Key: "_id", Value: 0}, // Exclude original _id since we renamed it to id
				}}},
			}},
		}}},
	}

	cursor, err := collection.Aggregate(ctx, pipeline)
	if err != nil {
		h.logger.Errorf("Failed to aggregate GSLB records: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to query GSLB records",
		})
		return
	}
	defer cursor.Close(ctx)

	var results []bson.M
	if err := cursor.All(ctx, &results); err != nil {
		h.logger.Errorf("Failed to decode aggregation results: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to decode GSLB records",
		})
		return
	}

	// Extract results from facet
	records := []bson.M{}
	total := 0

	if len(results) > 0 {
		result := results[0]

		// Get records
		if recordsData, ok := result["records"].(bson.A); ok {
			for _, r := range recordsData {
				if record, ok := r.(bson.M); ok {
					records = append(records, record)
				}
			}
		}

		// Get total count
		if metadata, ok := result["metadata"].(bson.A); ok && len(metadata) > 0 {
			if meta, ok := metadata[0].(bson.M); ok {
				if totalCount, ok := meta["total"].(int32); ok {
					total = int(totalCount)
				}
			}
		}
	}

	// Calculate pagination info
	totalPages := (total + limit - 1) / limit // Ceiling division

	c.JSON(http.StatusOK, gin.H{
		"records":     records,
		"count":       total, // Total count (backward compatibility)
		"page":        page,
		"limit":       limit,
		"total_pages": totalPages,
	})
}

// GetGSLBRecord returns a single GSLB record by ID
// GET /api/v3/gslb/:id?project=X
func (h *GSLBHandler) GetGSLBRecord(c *gin.Context) {
	id := c.Param("id")
	project := c.Query("project")

	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "project parameter is required",
		})
		return
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid record ID",
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	collection := h.db.Collection("gslb_records")

	// NOTE: GSLB records have no Permissions field - use project-based filtering only
	filter := bson.M{
		"_id":     objectID,
		"project": project,
	}

	var record models.GSLBRecord
	if err := collection.FindOne(ctx, filter).Decode(&record); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "GSLB record not found",
			})
			return
		}
		h.logger.Errorf("Failed to get GSLB record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to get GSLB record",
		})
		return
	}

	c.JSON(http.StatusOK, record)
}

// CreateGSLBRecord creates a new manual GSLB record
// POST /api/v3/gslb
// Access: Admin/Owner only
func (h *GSLBHandler) CreateGSLBRecord(c *gin.Context) {
	// Get user details from context
	user, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "User not authenticated",
		})
		return
	}

	// Check if user is Admin or Owner
	if !user.IsOwner && user.Role != models.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Only Admin and Owner can create GSLB records",
		})
		return
	}

	var input struct {
		FQDN         string            `json:"fqdn" binding:"required"`
		Project      string            `json:"project" binding:"required"`
		Version      string            `json:"version" binding:"required"`
		Enabled      bool              `json:"enabled"`
		TTL          uint32            `json:"ttl" binding:"required"`  // REQUIRED for manual records
		FailoverZone string            `json:"failover_zone,omitempty"` // Optional - defaults to first zone in settings.FailoverZones
		Probe        *models.GSLBProbe `json:"probe,omitempty"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("Invalid input: %v", err),
		})
		return
	}

	// Validate TTL (must be provided for manual records)
	if input.TTL == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "TTL is required for manual GSLB records",
		})
		return
	}

	// Validate TTL range
	if input.TTL < models.MinTTL || input.TTL > models.MaxTTL {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("TTL must be between %d and %d seconds", models.MinTTL, models.MaxTTL),
		})
		return
	}

	// Validate probe if provided
	if input.Probe != nil {
		// Set default enabled=true if not explicitly provided
		// Note: json.Unmarshal sets bool to false by default if not provided
		// We need to detect if it was explicitly set to false or just omitted
		// For simplicity, we'll use a pointer to detect this in validateProbe
		if err := validateProbe(input.Probe); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("Invalid probe configuration: %v", err),
			})
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get zone from settings
	var settings models.Settings
	settingsCollection := h.db.Collection("settings")
	if err := settingsCollection.FindOne(ctx, bson.M{"project": input.Project}).Decode(&settings); err != nil {
		h.logger.Errorf("Failed to get settings: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to get project settings",
		})
		return
	}

	if settings.GSLBConfig == nil || !settings.GSLBConfig.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "GSLB is not enabled for this project",
		})
		return
	}

	// Normalize FQDN with zone and calculate shard ID
	// Example: "dedeff" + zone "atest.elchi" -> "dedeff.atest.elchi."
	normalizedFQDN := helper.NormalizeFQDNWithZone(input.FQDN, settings.GSLBConfig.Zone)
	shardID := calculateShardID(normalizedFQDN)

	// Get default failover zone (first zone in array, if available)
	defaultFailoverZone := ""
	if len(settings.GSLBConfig.FailoverZones) > 0 {
		defaultFailoverZone = settings.GSLBConfig.FailoverZones[0]
	}

	// Use user-provided failover zone if specified, otherwise use default
	failoverZone := input.FailoverZone
	if failoverZone == "" {
		failoverZone = defaultFailoverZone
	}

	// Create GSLB record (IPs stored separately in gslb_ip_health collection)
	record := models.GSLBRecord{
		FQDN:         normalizedFQDN,
		ServiceID:    "", // Empty for manual records
		Project:      input.Project,
		Version:      input.Version,
		Zone:         settings.GSLBConfig.Zone,
		FailoverZone: failoverZone,
		ShardID:      shardID,
		Enabled:      input.Enabled,
		TTL:          input.TTL,
		Probe:        input.Probe,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		CreatedBy:    user.UserID,
	}

	collection := h.db.Collection("gslb_records")
	result, err := collection.InsertOne(ctx, record)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			c.JSON(http.StatusConflict, gin.H{
				"error": fmt.Sprintf("GSLB record with FQDN '%s' already exists in project '%s'", normalizedFQDN, input.Project),
			})
			return
		}
		h.logger.Errorf("Failed to create GSLB record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to create GSLB record",
		})
		return
	}

	record.ID = result.InsertedID.(primitive.ObjectID)

	h.logger.Infof("User %s created manual GSLB record: %s (shard: %d)", user.UserName, normalizedFQDN, shardID)

	// Trigger Time Wheel reload to pick up new record immediately
	if h.gslbSystem != nil {
		if err := h.gslbSystem.ReloadAllRecords(); err != nil {
			// Check if error is due to GSLB system not started (standby mode)
			if err.Error() == "GSLB system not started" {
				h.logger.Debugf("GSLB system in standby mode, record will be loaded when system starts")
			} else {
				h.logger.Warnf("Failed to reload GSLB Time Wheel after create: %v", err)
			}
			// Don't fail the request, just log the warning
		} else {
			h.logger.Debugf("GSLB Time Wheel reloaded after creating record: %s", normalizedFQDN)
		}
	}

	c.JSON(http.StatusCreated, record)
}

// UpdateGSLBRecord updates an existing GSLB record
// PUT /api/v3/gslb/:id
// Access: Admin/Owner only
func (h *GSLBHandler) UpdateGSLBRecord(c *gin.Context) {
	id := c.Param("id")

	// Get user details from context
	user, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "User not authenticated",
		})
		return
	}

	// Check if user is Admin or Owner
	if !user.IsOwner && user.Role != models.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Only Admin and Owner can update GSLB records",
		})
		return
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid record ID",
		})
		return
	}

	// Use json.RawMessage to detect explicit null for probe field
	var rawInput struct {
		Enabled      bool            `json:"enabled"`
		TTL          uint32          `json:"ttl"`
		FailoverZone *string         `json:"failover_zone,omitempty"` // Pointer to detect if field was provided
		Probe        json.RawMessage `json:"probe"`
	}

	if err := c.ShouldBindJSON(&rawInput); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("Invalid input: %v", err),
		})
		return
	}

	// Validate TTL range if provided
	if rawInput.TTL > 0 {
		if rawInput.TTL < models.MinTTL || rawInput.TTL > models.MaxTTL {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("TTL must be between %d and %d seconds", models.MinTTL, models.MaxTTL),
			})
			return
		}
	}

	// Determine probe operation: update, remove, or keep unchanged
	var probeToSet *models.GSLBProbe
	var shouldRemoveProbe bool
	var shouldUpdateProbe bool

	if len(rawInput.Probe) > 0 {
		// Probe field was provided in request
		if string(rawInput.Probe) == "null" {
			// Explicit null - remove probe
			shouldRemoveProbe = true
		} else {
			// Parse probe configuration
			if err := json.Unmarshal(rawInput.Probe, &probeToSet); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": fmt.Sprintf("Invalid probe JSON: %v", err),
				})
				return
			}
			// Validate probe configuration
			if err := validateProbe(probeToSet); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": fmt.Sprintf("Invalid probe configuration: %v", err),
				})
				return
			}
			shouldUpdateProbe = true
		}
	}
	// else: probe field not provided - keep existing probe unchanged

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := h.db.Collection("gslb_records")

	// Build update document (only allowed fields)
	update := bson.M{
		"$set": bson.M{
			"enabled":    rawInput.Enabled,
			"updated_at": time.Now(),
		},
	}

	if rawInput.TTL > 0 {
		update["$set"].(bson.M)["ttl"] = rawInput.TTL
	}

	// Handle failover zone update
	if rawInput.FailoverZone != nil {
		update["$set"].(bson.M)["failover_zone"] = *rawInput.FailoverZone
	}

	// Handle probe update or removal
	if shouldUpdateProbe {
		// Update probe with new configuration
		update["$set"].(bson.M)["probe"] = probeToSet
	} else if shouldRemoveProbe {
		// Remove probe field entirely
		if _, exists := update["$unset"]; !exists {
			update["$unset"] = bson.M{}
		}
		update["$unset"].(bson.M)["probe"] = ""
	}
	// else: probe field not provided - keep existing probe unchanged

	result, err := collection.UpdateOne(ctx, bson.M{"_id": objectID}, update)
	if err != nil {
		h.logger.Errorf("Failed to update GSLB record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to update GSLB record",
		})
		return
	}

	if result.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "GSLB record not found",
		})
		return
	}

	// If probe config changed (updated, removed, or re-enabled), reset backoff for all IPs in this record
	// This gives IPs a fresh start with new probe settings or when probe is re-enabled after being paused
	// IMPORTANT: Always reset backoff when probe changes, even if just enabled field changed
	if shouldUpdateProbe || shouldRemoveProbe {
		ipHealthCollection := h.db.Collection("gslb_ip_health")
		resetUpdate := bson.M{
			"$set": bson.M{
				"backoff_until":   time.Time{}, // Clear backoff timestamp
				"current_backoff": 0,           // Reset backoff duration
				"updated_at":      time.Now(),
			},
		}

		// Reset backoff for all IPs belonging to this record
		ipFilter := bson.M{"record_id": objectID}
		ipResult, err := ipHealthCollection.UpdateMany(ctx, ipFilter, resetUpdate)
		if err != nil {
			h.logger.Warnf("Failed to reset backoff after probe config update: %v", err)
			// Don't fail the request, just log the warning
		} else if ipResult.ModifiedCount > 0 {
			h.logger.Infof("Reset backoff for %d IPs after probe config update (record: %s, updated: probe=%v, removed: probe=%v)",
				ipResult.ModifiedCount, id, shouldUpdateProbe, shouldRemoveProbe)
		}
	}

	h.logger.Infof("User %s updated GSLB record: %s", user.UserName, id)

	// Trigger Time Wheel reload to pick up record changes immediately
	if h.gslbSystem != nil {
		if err := h.gslbSystem.ReloadAllRecords(); err != nil {
			// Check if error is due to GSLB system not started (standby mode)
			if err.Error() == "GSLB system not started" {
				h.logger.Debugf("GSLB system in standby mode, record will be loaded when system starts")
			} else {
				h.logger.Warnf("Failed to reload GSLB Time Wheel after update: %v", err)
			}
			// Don't fail the request, just log the warning
		} else {
			h.logger.Debugf("GSLB Time Wheel reloaded after updating record: %s", id)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "GSLB record updated successfully",
	})
}

// BulkUpdateGSLBRecords updates multiple GSLB records at once (enable/disable)
// PUT /api/v3/gslb/batch
// Request: { "record_ids": ["id1", "id2", ...], "enabled": true/false }
// Access: Admin/Owner only
func (h *GSLBHandler) BulkUpdateGSLBRecords(c *gin.Context) {
	// Get user details from context
	user, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "User not authenticated",
		})
		return
	}

	// Check if user is Admin or Owner
	if !user.IsOwner && user.Role != models.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Only Admin and Owner can bulk update GSLB records",
		})
		return
	}

	// Parse request body
	var input struct {
		RecordIDs []string `json:"record_ids" binding:"required"`
		Enabled   *bool    `json:"enabled" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("Invalid input: %v", err),
		})
		return
	}

	// Validate record IDs
	if len(input.RecordIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "record_ids cannot be empty",
		})
		return
	}

	if len(input.RecordIDs) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Cannot update more than 100 records at once",
		})
		return
	}

	// Convert record IDs to ObjectIDs
	objectIDs := make([]primitive.ObjectID, 0, len(input.RecordIDs))
	for _, id := range input.RecordIDs {
		objectID, err := primitive.ObjectIDFromHex(id)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("Invalid record ID: %s", id),
			})
			return
		}
		objectIDs = append(objectIDs, objectID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	collection := h.db.Collection("gslb_records")

	// Build update document
	update := bson.M{
		"$set": bson.M{
			"enabled":    *input.Enabled,
			"updated_at": time.Now(),
		},
	}

	// Update all matching records
	filter := bson.M{"_id": bson.M{"$in": objectIDs}}
	result, err := collection.UpdateMany(ctx, filter, update)
	if err != nil {
		h.logger.Errorf("Failed to bulk update GSLB records: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to bulk update GSLB records",
		})
		return
	}

	if result.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "No GSLB records found with provided IDs",
		})
		return
	}

	action := "disabled"
	if *input.Enabled {
		action = "enabled"
	}

	h.logger.Infof("User %s bulk %s %d GSLB records", user.UserName, action, result.ModifiedCount)

	// Trigger Time Wheel reload to pick up record changes immediately
	if h.gslbSystem != nil {
		if err := h.gslbSystem.ReloadAllRecords(); err != nil {
			if err.Error() == "GSLB system not started" {
				h.logger.Debugf("GSLB system in standby mode, records will be loaded when system starts")
			} else {
				h.logger.Warnf("Failed to reload GSLB Time Wheel after bulk update: %v", err)
			}
		} else {
			h.logger.Debugf("GSLB Time Wheel reloaded after bulk updating %d records", result.ModifiedCount)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message":        fmt.Sprintf("Successfully %s %d GSLB records", action, result.ModifiedCount),
		"matched_count":  result.MatchedCount,
		"modified_count": result.ModifiedCount,
	})
}

// DeleteGSLBRecord deletes a GSLB record
// DELETE /api/v3/gslb/:id?project=X
// Access: Admin/Owner only
func (h *GSLBHandler) DeleteGSLBRecord(c *gin.Context) {
	id := c.Param("id")
	project := c.Query("project")

	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "project parameter is required",
		})
		return
	}

	// Get user details from context
	user, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "User not authenticated",
		})
		return
	}

	// Check if user is Admin or Owner
	if !user.IsOwner && user.Role != models.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Only Admin and Owner can delete GSLB records",
		})
		return
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid record ID",
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := h.db.Collection("gslb_records")

	// Check if record is auto-created (has service_id)
	var record models.GSLBRecord
	if err := collection.FindOne(ctx, bson.M{"_id": objectID, "project": project}).Decode(&record); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "GSLB record not found",
			})
			return
		}
		h.logger.Errorf("Failed to get GSLB record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to get GSLB record",
		})
		return
	}

	if record.ServiceID != "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Cannot delete auto-created GSLB record. Delete the associated service instead.",
		})
		return
	}

	// Delete the record
	result, err := collection.DeleteOne(ctx, bson.M{"_id": objectID, "project": project})
	if err != nil {
		h.logger.Errorf("Failed to delete GSLB record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to delete GSLB record",
		})
		return
	}

	if result.DeletedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "GSLB record not found",
		})
		return
	}

	h.logger.Infof("User %s deleted manual GSLB record: %s (%s)", user.UserName, record.FQDN, id)

	// Trigger Time Wheel reload to remove deleted record immediately
	if h.gslbSystem != nil {
		if err := h.gslbSystem.ReloadAllRecords(); err != nil {
			// Check if error is due to GSLB system not started (standby mode)
			if err.Error() == "GSLB system not started" {
				h.logger.Debugf("GSLB system in standby mode, no reload needed")
			} else {
				h.logger.Warnf("Failed to reload GSLB Time Wheel after delete: %v", err)
			}
			// Don't fail the request, just log the warning
		} else {
			h.logger.Debugf("GSLB Time Wheel reloaded after deleting record: %s", record.FQDN)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "GSLB record deleted successfully",
	})
}

// validateProbe validates GSLB probe configuration
func validateProbe(probe *models.GSLBProbe) error {
	// Validate probe type
	if probe.Type != "http" && probe.Type != "https" && probe.Type != "tcp" {
		return fmt.Errorf("probe type must be one of: http, https, tcp")
	}

	// Validate interval (must be one of allowed values)
	validInterval := false
	for _, allowed := range models.AllowedProbeIntervals {
		if probe.Interval == allowed {
			validInterval = true
			break
		}
	}
	if !validInterval {
		return fmt.Errorf("interval must be one of: %v seconds", models.AllowedProbeIntervals)
	}

	// Validate timeout (supports millisecond precision: 0.1 = 100ms, 0.5 = 500ms, 3.0 = 3000ms)
	if probe.Timeout < models.MinProbeTimeout || probe.Timeout > models.MaxProbeTimeout {
		return fmt.Errorf("timeout must be between %.1f and %.1f seconds (%.0fms to %.0fms)",
			models.MinProbeTimeout, models.MaxProbeTimeout,
			models.MinProbeTimeout*1000, models.MaxProbeTimeout*1000)
	}

	// Timeout must be less than interval
	if probe.Timeout >= float64(probe.Interval) {
		return fmt.Errorf("timeout (%.1fs) must be less than interval (%ds)", probe.Timeout, probe.Interval)
	}

	// Validate thresholds (tri-state model)
	if probe.WarningThreshold < 1 || probe.CriticalThreshold < 1 {
		return fmt.Errorf("warning_threshold and critical_threshold must be at least 1")
	}
	if probe.CriticalThreshold <= probe.WarningThreshold {
		return fmt.Errorf("critical_threshold (%d) must be greater than warning_threshold (%d)", probe.CriticalThreshold, probe.WarningThreshold)
	}

	// Validate expected_status_codes if provided (HTTP/HTTPS only)
	if len(probe.ExpectedStatusCodes) > 0 {
		if probe.Type != "http" && probe.Type != "https" {
			return fmt.Errorf("expected_status_codes is only valid for http/https probes")
		}

		for _, code := range probe.ExpectedStatusCodes {
			// Check if it's a range (e.g., "200-299")
			if strings.Contains(code, "-") {
				parts := strings.Split(code, "-")
				if len(parts) != 2 {
					return fmt.Errorf("invalid status code range format: %s (expected format: '200-299')", code)
				}
				start, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
				end, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
				if err1 != nil || err2 != nil {
					return fmt.Errorf("invalid status code range: %s (must be numeric)", code)
				}
				if start < 100 || start > 599 || end < 100 || end > 599 {
					return fmt.Errorf("status codes must be between 100 and 599 (got: %s)", code)
				}
				if start > end {
					return fmt.Errorf("invalid status code range: %s (start must be <= end)", code)
				}
			} else {
				// Single status code
				statusCode, err := strconv.Atoi(strings.TrimSpace(code))
				if err != nil {
					return fmt.Errorf("invalid status code: %s (must be numeric)", code)
				}
				if statusCode < 100 || statusCode > 599 {
					return fmt.Errorf("status code must be between 100 and 599 (got: %d)", statusCode)
				}
			}
		}
	}

	return nil
}

// AddIPToRecord adds a new IP to an existing GSLB record
// POST /api/v3/gslb/:id/ips
// Access: Admin/Owner only
// Input: { "ip": "1.2.3.4", "client_id": "optional-client-id", "health_state": "passing|warning|critical" }
func (h *GSLBHandler) AddIPToRecord(c *gin.Context) {
	id := c.Param("id")

	// Get user details from context
	user, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "User not authenticated",
		})
		return
	}

	// Check if user is Admin or Owner
	if !user.IsOwner && user.Role != models.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Only Admin and Owner can add IPs to GSLB records",
		})
		return
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid record ID",
		})
		return
	}

	var input struct {
		IP          string `json:"ip" binding:"required"`
		ClientID    string `json:"client_id"`    // Optional
		HealthState string `json:"health_state"` // Optional - "passing", "warning", "critical" (default: "passing")
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("Invalid input: %v", err),
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := h.db.Collection("gslb_records")

	// Get existing record to check if it's auto-created
	var existingRecord models.GSLBRecord
	if err := collection.FindOne(ctx, bson.M{"_id": objectID}).Decode(&existingRecord); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "GSLB record not found",
			})
			return
		}
		h.logger.Errorf("Failed to get GSLB record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to get GSLB record",
		})
		return
	}

	// NOTE: Manual IPs can be added to both manual AND auto-created records
	// The is_manual field will distinguish them:
	// - Auto-generated IPs (from service deployment): is_manual = false
	// - Manually added IPs (via API): is_manual = true

	// Create IPHealthManager
	ipHealthManager := gslb.NewIPHealthManager(h.db, h.logger)

	// Calculate sub-shard for this IP
	subShardID := gslb.CalculateSubShardID(existingRecord.FQDN, input.IP)

	// Parse health state from input (defaults to "passing" if not provided or invalid)
	initialHealthState := models.HealthStatePassing // Default
	if input.HealthState != "" {
		switch strings.ToLower(input.HealthState) {
		case "passing":
			initialHealthState = models.HealthStatePassing
		case "warning":
			initialHealthState = models.HealthStateWarning
		case "critical":
			initialHealthState = models.HealthStateCritical
		default:
			// Invalid state provided - return error
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("Invalid health_state '%s'. Must be one of: passing, warning, critical", input.HealthState),
			})
			return
		}
	}

	// Add IP to gslb_ip_health collection with specified initial health state
	err = ipHealthManager.AddIPWithHealth(ctx, existingRecord.ID, existingRecord.FQDN, input.IP, input.ClientID, existingRecord.ShardID, subShardID, initialHealthState)
	if err != nil {
		// Check if duplicate (IP already exists)
		if mongo.IsDuplicateKeyError(err) {
			c.JSON(http.StatusConflict, gin.H{
				"error": fmt.Sprintf("IP %s already exists in this GSLB record", input.IP),
			})
			return
		}
		h.logger.Errorf("Failed to add IP to GSLB health collection: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to add IP to GSLB health collection",
		})
		return
	}

	h.logger.Infof("User %s added IP %s to GSLB record %s (FQDN: %s, shard: %d/%d, initial state: %s)",
		user.UserName, input.IP, id, existingRecord.FQDN, existingRecord.ShardID, subShardID, initialHealthState.String())

	// Trigger Time Wheel reload to start probing new IP immediately
	if h.gslbSystem != nil {
		if err := h.gslbSystem.ReloadAllRecords(); err != nil {
			// Check if error is due to GSLB system not started (standby mode)
			if err.Error() == "GSLB system not started" {
				h.logger.Debugf("GSLB system in standby mode, IP will be probed when system starts")
			} else {
				h.logger.Warnf("Failed to reload GSLB Time Wheel after adding IP: %v", err)
			}
			// Don't fail the request, just log the warning
		} else {
			h.logger.Debugf("GSLB Time Wheel reloaded after adding IP %s to record: %s", input.IP, existingRecord.FQDN)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      "IP added successfully to GSLB health collection",
		"ip":           input.IP,
		"client_id":    input.ClientID,
		"shard":        fmt.Sprintf("%d/%d", existingRecord.ShardID, subShardID),
		"health_state": initialHealthState.String(),
		"fqdn":         existingRecord.FQDN,
		"created_at":   time.Now(),
	})
}

// RemoveIPFromRecord removes an IP from a GSLB record
// DELETE /api/v3/gslb/:id/ips/:ip
// Access: Admin/Owner only
func (h *GSLBHandler) RemoveIPFromRecord(c *gin.Context) {
	id := c.Param("id")
	ipToRemove := c.Param("ip")

	// Get user details from context
	user, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "User not authenticated",
		})
		return
	}

	// Check if user is Admin or Owner
	if !user.IsOwner && user.Role != models.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Only Admin and Owner can remove IPs from GSLB records",
		})
		return
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid record ID",
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := h.db.Collection("gslb_records")

	// Get record to check if auto-created and verify IP exists
	var record models.GSLBRecord
	if err := collection.FindOne(ctx, bson.M{"_id": objectID}).Decode(&record); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "GSLB record not found",
			})
			return
		}
		h.logger.Errorf("Failed to get GSLB record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to get GSLB record",
		})
		return
	}

	// Create IPHealthManager
	ipHealthManager := gslb.NewIPHealthManager(h.db, h.logger)

	// Check if IP exists and is manually added (is_manual = true)
	ipHealthCollection := h.db.Collection("gslb_ip_health")
	var ipHealth models.GSLBIPHealth
	if err := ipHealthCollection.FindOne(ctx, bson.M{
		"record_id": record.ID,
		"ip":        ipToRemove,
	}).Decode(&ipHealth); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": fmt.Sprintf("IP %s not found in this GSLB record", ipToRemove),
			})
			return
		}
		h.logger.Errorf("Failed to get IP health record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to get IP health record",
		})
		return
	}

	// Only allow deletion of manually added IPs (is_manual = true)
	if !ipHealth.IsManual {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Cannot remove auto-generated IPs. Only manually added IPs (is_manual=true) can be removed via API.",
		})
		return
	}

	// Remove IP from gslb_ip_health collection
	err = ipHealthManager.RemoveIP(ctx, record.ID, ipToRemove)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": fmt.Sprintf("IP %s not found in this GSLB record", ipToRemove),
			})
			return
		}
		h.logger.Errorf("Failed to remove IP from GSLB health collection: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to remove IP from GSLB health collection",
		})
		return
	}

	h.logger.Infof("User %s removed IP %s from GSLB record %s (FQDN: %s)", user.UserName, ipToRemove, id, record.FQDN)

	// Trigger Time Wheel reload to stop probing removed IP immediately
	if h.gslbSystem != nil {
		if err := h.gslbSystem.ReloadAllRecords(); err != nil {
			// Check if error is due to GSLB system not started (standby mode)
			if err.Error() == "GSLB system not started" {
				h.logger.Debugf("GSLB system in standby mode, no reload needed")
			} else {
				h.logger.Warnf("Failed to reload GSLB Time Wheel after removing IP: %v", err)
			}
			// Don't fail the request, just log the warning
		} else {
			h.logger.Debugf("GSLB Time Wheel reloaded after removing IP %s from record: %s", ipToRemove, record.FQDN)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("IP %s removed successfully from GSLB health collection", ipToRemove),
	})
}

// UpdateIPHealthState manually updates the health state of an IP
// PUT /api/v3/gslb/:id/ips/:ip
// Access: Admin/Owner only
// Input: { "health_state": "passing"|"warning"|"critical" }
// Use cases: Manual drain, maintenance mode, force up/down, gradual degradation
func (h *GSLBHandler) UpdateIPHealthState(c *gin.Context) {
	id := c.Param("id")
	ipToUpdate := c.Param("ip")

	// Get user details from context
	user, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "User not authenticated",
		})
		return
	}

	// Check if user is Admin or Owner
	if !user.IsOwner && user.Role != models.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Only Admin and Owner can update IP health state",
		})
		return
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid record ID",
		})
		return
	}

	var input struct {
		HealthState string `json:"health_state" binding:"required"` // "passing", "warning", "critical"
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("Invalid input: %v", err),
		})
		return
	}

	// Validate and parse health state
	var newHealthState models.HealthState
	switch strings.ToLower(input.HealthState) {
	case "passing":
		newHealthState = models.HealthStatePassing
	case "warning":
		newHealthState = models.HealthStateWarning
	case "critical":
		newHealthState = models.HealthStateCritical
	default:
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("Invalid health_state '%s'. Must be one of: passing, warning, critical", input.HealthState),
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := h.db.Collection("gslb_records")

	// Get record to verify it exists and is not auto-created
	var record models.GSLBRecord
	if err := collection.FindOne(ctx, bson.M{"_id": objectID}).Decode(&record); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "GSLB record not found",
			})
			return
		}
		h.logger.Errorf("Failed to get GSLB record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to get GSLB record",
		})
		return
	}

	// NOTE: Manual health state updates are now allowed for both manual AND auto-created records
	// This allows administrators to override health checker decisions when necessary
	// The manual_reset_at timestamp will be set to track when the state was manually changed

	// Update IP health state directly in gslb_ip_health collection
	ipHealthCollection := h.db.Collection("gslb_ip_health")

	now := time.Now()

	// Create status history entry for manual change
	historyEntry := models.GSLBStatusHistory{
		State:        newHealthState.String(),
		DateTime:     now,
		ResponseCode: 0, // Manual change - no probe
		ResponseTime: 0,
	}

	// Update health state with manual override (no longer set 'healthy' field - removed)
	update := bson.M{
		"$set": bson.M{
			"health_state":       newHealthState,
			"last_status_change": now,
			"updated_at":         now,
			"backoff_until":      time.Time{}, // Clear any backoff
			"current_backoff":    0,
			"manual_reset_at":    now, // Mark as manually reset (prevents infinite detection loop)
		},
		"$push": bson.M{
			"status_history": bson.M{
				"$each":  []models.GSLBStatusHistory{historyEntry},
				"$slice": -models.GSLBMaxStatusHistorySize, // Keep last 50
			},
		},
	}

	filter := bson.M{
		"record_id": objectID,
		"ip":        ipToUpdate,
	}

	result, err := ipHealthCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		h.logger.Errorf("Failed to update IP health state: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to update IP health state",
		})
		return
	}

	if result.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{
			"error": fmt.Sprintf("IP %s not found in this GSLB record", ipToUpdate),
		})
		return
	}

	h.logger.Infof("User %s manually updated IP %s health state to %s for GSLB record %s (FQDN: %s)",
		user.UserName, ipToUpdate, newHealthState.String(), id, record.FQDN)

	// CRITICAL: Trigger immediate re-probe to verify actual health status
	// Without this, manually-changed IPs stay in incorrect state until next scheduled probe
	// Background execution to not block API response
	// IMPORTANT: Pass newHealthState directly to avoid MongoDB race condition
	go func() {
		// Use background context with timeout for async execution
		probeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if h.gslbSystem != nil {
			healthChecker := h.gslbSystem.GetHealthChecker()
			if healthChecker != nil {
				// Pass the manual state directly - don't read from MongoDB (race condition)
				// NOTE: TriggerImmediateReProbeForManualChange will:
				//   1. Execute immediate probe
				//   2. Process result via evaluateStatusChangeForRecord
				//   3. Automatically reschedule in Time Wheel via HandleProbeResult
				// No need for explicit RescheduleImmediate call here!
				if err := healthChecker.TriggerImmediateReProbeForManualChange(probeCtx, objectID, ipToUpdate, newHealthState); err != nil {
					h.logger.Warnf("Failed to trigger immediate re-probe for manually changed IP %s: %v", ipToUpdate, err)
				}
			}
		}
	}()

	c.JSON(http.StatusOK, gin.H{
		"message":      fmt.Sprintf("IP %s health state updated successfully", ipToUpdate),
		"ip":           ipToUpdate,
		"health_state": newHealthState.String(),
	})
}

// UpdateIPRegions assigns regions to a specific IP in a GSLB record
// PUT /api/v3/gslb/:id/ips/:ip/regions
// Access: Admin/Owner only
// Input: { "regions": ["eu-west-1", "us-east-1"] } - empty array clears all region assignments
func (h *GSLBHandler) UpdateIPRegions(c *gin.Context) {
	id := c.Param("id")
	ipToUpdate := c.Param("ip")

	// Get user details from context
	user, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "User not authenticated",
		})
		return
	}

	// Check if user is Admin or Owner
	if !user.IsOwner && user.Role != models.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Only Admin and Owner can update IP regions",
		})
		return
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid record ID",
		})
		return
	}

	var input struct {
		Regions []string `json:"regions" binding:"required"` // Region names to assign (empty array to clear)
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("Invalid input: %v", err),
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get GSLB record to find its project
	collection := h.db.Collection("gslb_records")
	var record models.GSLBRecord
	if err := collection.FindOne(ctx, bson.M{"_id": objectID}).Decode(&record); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "GSLB record not found",
			})
			return
		}
		h.logger.Errorf("Failed to get GSLB record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to get GSLB record",
		})
		return
	}

	// Validate regions against project's GSLB settings (only if assigning regions)
	if len(input.Regions) > 0 {
		var settings models.Settings
		settingsCollection := h.db.Collection("settings")
		if err := settingsCollection.FindOne(ctx, bson.M{"project": record.Project}).Decode(&settings); err != nil {
			h.logger.Errorf("Failed to get settings: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Failed to get project settings",
			})
			return
		}

		if settings.GSLBConfig == nil || len(settings.GSLBConfig.Regions) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "No regions defined in GSLB settings. Define regions first via GSLB configuration.",
			})
			return
		}

		// Build allowed regions set for fast lookup
		allowedRegions := make(map[string]bool, len(settings.GSLBConfig.Regions))
		for _, r := range settings.GSLBConfig.Regions {
			allowedRegions[r] = true
		}

		// Validate each requested region exists in settings
		for _, region := range input.Regions {
			if !allowedRegions[region] {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": fmt.Sprintf("Region '%s' is not defined in GSLB settings. Available regions: %v", region, settings.GSLBConfig.Regions),
				})
				return
			}
		}
	}

	// Update regions on the IP health document
	ipHealthCollection := h.db.Collection("gslb_ip_health")

	filter := bson.M{
		"record_id": objectID,
		"ip":        ipToUpdate,
	}

	update := bson.M{
		"$set": bson.M{
			"regions":    input.Regions,
			"updated_at": time.Now(),
		},
	}

	result, err := ipHealthCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		h.logger.Errorf("Failed to update IP regions: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to update IP regions",
		})
		return
	}

	if result.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{
			"error": fmt.Sprintf("IP %s not found in this GSLB record", ipToUpdate),
		})
		return
	}

	h.logger.Infof("User %s updated regions for IP %s in GSLB record %s (FQDN: %s, regions: %v)",
		user.UserName, ipToUpdate, id, record.FQDN, input.Regions)

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("IP %s regions updated successfully", ipToUpdate),
		"ip":      ipToUpdate,
		"regions": input.Regions,
	})
}

// ListIPsForRecord lists all IP health records for a GSLB record
// GET /api/v3/gslb/:id/ips
// Access: All authenticated users
func (h *GSLBHandler) ListIPsForRecord(c *gin.Context) {
	id := c.Param("id")

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid record ID",
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Verify GSLB record exists
	gslbCollection := h.db.Collection("gslb_records")
	var record models.GSLBRecord
	if err := gslbCollection.FindOne(ctx, bson.M{"_id": objectID}).Decode(&record); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "GSLB record not found",
			})
			return
		}
		h.logger.Errorf("Failed to get GSLB record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to get GSLB record",
		})
		return
	}

	// Get all IP health records for this GSLB record
	ipHealthManager := gslb.NewIPHealthManager(h.db, h.logger)
	ips, err := ipHealthManager.GetIPsByRecordID(ctx, objectID)
	if err != nil {
		h.logger.Errorf("Failed to get IPs for GSLB record: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to get IPs",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ips": ips,
	})
}

// ClearIPHistory clears the status_history array for a specific IP health document
// DELETE /api/v3/gslb/ip/:id/history
// Access: Admin/Owner only
// :id is the gslb_ip_health document ID (not the GSLB record ID)
func (h *GSLBHandler) ClearIPHistory(c *gin.Context) {
	id := c.Param("id")

	// Get user details from context
	user, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "User not authenticated",
		})
		return
	}

	// Check if user is Admin or Owner
	if !user.IsOwner && user.Role != models.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Only Admin and Owner can clear IP history",
		})
		return
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid IP health ID",
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Clear status history for the specific IP health document
	ipHealthCollection := h.db.Collection("gslb_ip_health")
	update := bson.M{
		"$set": bson.M{
			"status_history": []models.GSLBStatusHistory{}, // Clear history array
			"updated_at":     time.Now(),
		},
	}

	filter := bson.M{"_id": objectID}

	result, err := ipHealthCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		h.logger.Errorf("Failed to clear IP history: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to clear IP history",
		})
		return
	}

	if result.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "IP health record not found",
		})
		return
	}

	h.logger.Infof("User %s cleared status history for IP health document %s", user.UserName, id)

	c.JSON(http.StatusOK, gin.H{
		"message": "IP status history cleared successfully",
		"id":      id,
	})
}

// calculateShardID calculates shard ID using FNV-1a hash
func calculateShardID(fqdn string) int {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(fqdn)) // hash.Write never returns an error, but satisfying gosec
	return int(hash.Sum32() % uint32(models.GSLBNumShards))
}

// ListGSLBNodes returns all tracked GSLB node instances
// GET /api/v3/gslb/nodes?zone=X (optional zone filter)
func (h *GSLBHandler) ListGSLBNodes(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	// Only Admin/Owner can view GSLB nodes
	user, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	if !user.IsOwner && user.Role != models.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admin or Owner access required"})
		return
	}

	filter := bson.M{}
	if zone := c.Query("zone"); zone != "" {
		filter["zone"] = zone
	}

	collection := h.db.Collection("gslb_nodes")
	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		h.logger.Errorf("Failed to list GSLB nodes: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list GSLB nodes"})
		return
	}
	defer cursor.Close(ctx)

	var nodes []models.GSLBNode
	if err := cursor.All(ctx, &nodes); err != nil {
		h.logger.Errorf("Failed to decode GSLB nodes: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to decode GSLB nodes"})
		return
	}

	if nodes == nil {
		nodes = []models.GSLBNode{}
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "GSLB nodes retrieved successfully",
		"data":    nodes,
		"total":   len(nodes),
	})
}

// DeleteGSLBNode removes a tracked GSLB node entry
// DELETE /api/v3/gslb/nodes/:id
func (h *GSLBHandler) DeleteGSLBNode(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	// Only Admin/Owner can delete GSLB nodes
	user, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	if !user.IsOwner && user.Role != models.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admin or Owner access required"})
		return
	}

	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid node ID"})
		return
	}

	collection := h.db.Collection("gslb_nodes")
	result, err := collection.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		h.logger.Errorf("Failed to delete GSLB node: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete GSLB node"})
		return
	}

	if result.DeletedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "GSLB node not found"})
		return
	}

	h.logger.Infof("User %s deleted GSLB node: %s", user.UserName, id.Hex())

	c.JSON(http.StatusOK, gin.H{
		"message": "GSLB node deleted successfully",
	})
}
