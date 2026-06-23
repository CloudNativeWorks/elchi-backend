package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SnippetHandler handles snippet-related operations
type SnippetHandler struct {
	handler *Handler
}

// NewSnippetHandler creates a new snippet handler
func NewSnippetHandler(handler *Handler) *SnippetHandler {
	return &SnippetHandler{
		handler: handler,
	}
}

// requireSnippetWriter rejects read-only viewers from mutating snippets.
// Snippet handlers are direct gin handlers (no checkRole pipeline), so the
// role guard is explicit here. Editor/Admin/Owner may write; viewers are
// read-only. Returns false (and writes the response) when access is denied.
func (sh *SnippetHandler) requireSnippetWriter(c *gin.Context) bool {
	user, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return false
	}
	if user.Role == models.RoleViewer {
		c.JSON(http.StatusForbidden, gin.H{"error": "viewers cannot modify snippets"})
		return false
	}
	return true
}

// generateDataHash generates a hash for snippet data to detect duplicates
func (sh *SnippetHandler) generateDataHash(data any) string {
	// Convert to consistent JSON string
	jsonBytes, _ := json.Marshal(data)
	hash := sha256.Sum256(jsonBytes)
	return hex.EncodeToString(hash[:])
}

// validateSnippetData performs basic validation on snippet data
func (sh *SnippetHandler) validateSnippetData(data any) error {
	// Size limit check (100KB)
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("invalid JSON data: %w", err)
	}
	if len(jsonBytes) > 100*1024 {
		return fmt.Errorf("snippet data too large (max 100KB)")
	}
	return nil
}

// setSnippetAuditChanges sets audit changes for snippet updates
func (sh *SnippetHandler) setSnippetAuditChanges(c *gin.Context, existingSnippet *models.ResourceSnippet, updateReq *models.SnippetUpdateRequest) {
	changes := map[string]any{
		"before": map[string]any{
			"name":         existingSnippet.Name,
			"snippet_data": existingSnippet.SnippetData,
		},
		"after": map[string]any{},
	}

	// Track what's being changed
	if updateReq.Name != nil {
		changes["after"].(map[string]any)["name"] = *updateReq.Name
	} else {
		changes["after"].(map[string]any)["name"] = existingSnippet.Name
	}

	if updateReq.SnippetData != nil {
		changes["after"].(map[string]any)["snippet_data"] = *updateReq.SnippetData
	} else {
		changes["after"].(map[string]any)["snippet_data"] = existingSnippet.SnippetData
	}

	// Set changes in audit context
	audit.SetAuditChanges(c, changes)
}

// CreateSnippet creates a new snippet
func (sh *SnippetHandler) CreateSnippet(c *gin.Context) {
	if !sh.requireSnippetWriter(c) {
		return
	}
	var req models.SnippetCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format", "details": err.Error()})
		return
	}

	// Set audit context
	audit.SetAuditAction(c, "SNIPPET_CREATE")
	audit.SetAuditResource(c, "snippets", "", req.Name, req.Project)

	// Validate snippet data
	if err := sh.validateSnippetData(req.SnippetData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid snippet data", "details": err.Error()})
		return
	}

	// Get user details from context
	user, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated", "details": err.Error()})
		return
	}

	// Generate data hash
	dataHash := sh.generateDataHash(req.SnippetData)

	// Check for duplicates
	ctx := context.Background()
	collection := sh.handler.XDS.Context.Client.Collection("snippets")

	var existingSnippet models.ResourceSnippet
	err = collection.FindOne(ctx, bson.M{
		"data_hash": dataHash,
		"project":   req.Project,
		"gtype":     req.GType,
	}).Decode(&existingSnippet)

	if err == nil {
		c.JSON(http.StatusConflict, gin.H{
			"error": "Duplicate snippet detected",
			"existing_snippet": gin.H{
				"id":   existingSnippet.ID.Hex(),
				"name": existingSnippet.Name,
			},
		})
		return
	}

	// Create new snippet
	snippet := models.ResourceSnippet{
		ID:            primitive.NewObjectID(),
		Name:          req.Name,
		ComponentType: req.ComponentType,
		GType:         req.GType,
		FieldPath:     req.FieldPath,
		IsArray:       req.IsArray,
		Version:       req.Version,
		Project:       req.Project,
		SnippetData:   req.SnippetData,
		DataHash:      dataHash,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		CreatedBy:     user.UserName,
	}

	// Insert into database
	_, err = collection.InsertOne(ctx, snippet)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create snippet", "details": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, snippet)
}

// GetSnippet retrieves a snippet by ID
func (sh *SnippetHandler) GetSnippet(c *gin.Context) {
	snippetID := c.Param("id")
	objID, err := primitive.ObjectIDFromHex(snippetID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid snippet ID"})
		return
	}

	ctx := context.Background()
	collection := sh.handler.XDS.Context.Client.Collection("snippets")

	var snippet models.ResourceSnippet
	err = collection.FindOne(ctx, bson.M{"_id": objID}).Decode(&snippet)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Snippet not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve snippet", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, snippet)
}

// ListSnippets lists snippets with filtering and pagination
func (sh *SnippetHandler) ListSnippets(c *gin.Context) {
	var req models.SnippetListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid query parameters", "details": err.Error()})
		return
	}

	// Set defaults
	if req.Limit <= 0 || req.Limit > models.MaxSnippetLimit {
		req.Limit = models.DefaultSnippetLimit
	}
	if req.Offset < 0 {
		req.Offset = models.DefaultSnippetOffset
	}

	// Build filter
	filter := bson.M{}
	if req.Project != "" {
		filter["project"] = req.Project
	}
	if req.Version != "" {
		filter["version"] = req.Version
	}
	if req.ComponentType != "" {
		filter["component_type"] = req.ComponentType
	}
	if req.GType != "" {
		filter["gtype"] = req.GType
	}
	if req.Search != "" {
		filter["name"] = bson.M{"$regex": req.Search, "$options": "i"}
	}

	ctx := context.Background()
	collection := sh.handler.XDS.Context.Client.Collection("snippets")

	// Count total documents
	total, err := collection.CountDocuments(ctx, filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count snippets", "details": err.Error()})
		return
	}

	// Find documents with pagination
	findOptions := options.Find()
	findOptions.SetLimit(int64(req.Limit))
	findOptions.SetSkip(int64(req.Offset))
	findOptions.SetSort(bson.D{{Key: "created_at", Value: -1}}) // Sort by creation date, newest first

	cursor, err := collection.Find(ctx, filter, findOptions)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to query snippets", "details": err.Error()})
		return
	}
	defer cursor.Close(ctx)

	var snippets []models.ResourceSnippet
	if err := cursor.All(ctx, &snippets); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to decode snippets", "details": err.Error()})
		return
	}

	// Calculate pagination info
	totalPages := int(math.Ceil(float64(total) / float64(req.Limit)))
	currentPage := (req.Offset / req.Limit) + 1
	hasMore := req.Offset+req.Limit < int(total)

	response := models.SnippetListResponse{
		Snippets:   snippets,
		Total:      total,
		Limit:      req.Limit,
		Offset:     req.Offset,
		HasMore:    hasMore,
		Page:       currentPage,
		TotalPages: totalPages,
	}

	c.JSON(http.StatusOK, response)
}

// UpdateSnippet updates an existing snippet
func (sh *SnippetHandler) UpdateSnippet(c *gin.Context) {
	if !sh.requireSnippetWriter(c) {
		return
	}
	snippetID := c.Param("id")
	objID, err := primitive.ObjectIDFromHex(snippetID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid snippet ID"})
		return
	}

	var req models.SnippetUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format", "details": err.Error()})
		return
	}

	ctx := context.Background()
	collection := sh.handler.XDS.Context.Client.Collection("snippets")

	// Get existing snippet for audit comparison
	var existingSnippet models.ResourceSnippet
	err = collection.FindOne(ctx, bson.M{"_id": objID}).Decode(&existingSnippet)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Snippet not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve snippet", "details": err.Error()})
		return
	}

	// Set audit context with project info
	audit.SetAuditAction(c, "SNIPPET_UPDATE")
	audit.SetAuditResource(c, "snippets", snippetID, existingSnippet.Name, existingSnippet.Project)

	// Set audit changes for comparison
	sh.setSnippetAuditChanges(c, &existingSnippet, &req)

	// Build update document
	update := bson.M{"$set": bson.M{"updated_at": time.Now()}}

	if req.Name != nil {
		update["$set"].(bson.M)["name"] = *req.Name
	}

	if req.SnippetData != nil {
		// Validate new snippet data
		if err := sh.validateSnippetData(*req.SnippetData); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid snippet data", "details": err.Error()})
			return
		}

		// Generate new hash
		dataHash := sh.generateDataHash(*req.SnippetData)
		update["$set"].(bson.M)["snippet_data"] = *req.SnippetData
		update["$set"].(bson.M)["data_hash"] = dataHash
	}

	result, err := collection.UpdateOne(ctx, bson.M{"_id": objID}, update)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update snippet", "details": err.Error()})
		return
	}

	if result.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Snippet not found"})
		return
	}

	// Retrieve updated snippet
	var updatedSnippet models.ResourceSnippet
	err = collection.FindOne(ctx, bson.M{"_id": objID}).Decode(&updatedSnippet)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve updated snippet", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, updatedSnippet)
}

// DeleteSnippet deletes a snippet by ID
func (sh *SnippetHandler) DeleteSnippet(c *gin.Context) {
	if !sh.requireSnippetWriter(c) {
		return
	}
	snippetID := c.Param("id")
	objID, err := primitive.ObjectIDFromHex(snippetID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid snippet ID"})
		return
	}

	// Set audit context
	audit.SetAuditAction(c, "SNIPPET_DELETE")
	audit.SetAuditResource(c, "snippets", snippetID, snippetID, "")

	ctx := context.Background()
	collection := sh.handler.XDS.Context.Client.Collection("snippets")

	result, err := collection.DeleteOne(ctx, bson.M{"_id": objID})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete snippet", "details": err.Error()})
		return
	}

	if result.DeletedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Snippet not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Snippet deleted successfully"})
}

// BatchCreateSnippets creates multiple snippets in a single request
func (sh *SnippetHandler) BatchCreateSnippets(c *gin.Context) {
	if !sh.requireSnippetWriter(c) {
		return
	}
	var req models.SnippetBatchCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format", "details": err.Error()})
		return
	}

	// Get user details
	user, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated", "details": err.Error()})
		return
	}

	ctx := context.Background()
	collection := sh.handler.XDS.Context.Client.Collection("snippets")

	var created []models.ResourceSnippet
	var failed []models.SnippetError

	for i, snippetReq := range req.Snippets {
		// Validate snippet data
		if err := sh.validateSnippetData(snippetReq.SnippetData); err != nil {
			failed = append(failed, models.SnippetError{
				Index:   i,
				Error:   "validation_error",
				Message: err.Error(),
			})
			continue
		}

		// Generate data hash
		dataHash := sh.generateDataHash(snippetReq.SnippetData)

		// Create snippet
		snippet := models.ResourceSnippet{
			ID:            primitive.NewObjectID(),
			Name:          snippetReq.Name,
			ComponentType: snippetReq.ComponentType,
			GType:         snippetReq.GType,
			FieldPath:     snippetReq.FieldPath,
			IsArray:       snippetReq.IsArray,
			Version:       snippetReq.Version,
			Project:       snippetReq.Project,
			SnippetData:   snippetReq.SnippetData,
			DataHash:      dataHash,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
			CreatedBy:     user.UserName,
		}

		// Insert into database
		_, err := collection.InsertOne(ctx, snippet)
		if err != nil {
			failed = append(failed, models.SnippetError{
				Index:   i,
				Error:   "database_error",
				Message: err.Error(),
			})
			continue
		}

		created = append(created, snippet)
	}

	response := models.SnippetBatchCreateResponse{
		Created: created,
		Failed:  failed,
	}

	statusCode := http.StatusCreated
	if len(failed) > 0 {
		statusCode = http.StatusPartialContent
	}

	c.JSON(statusCode, response)
}

// BatchDeleteSnippets deletes multiple snippets in a single request
func (sh *SnippetHandler) BatchDeleteSnippets(c *gin.Context) {
	if !sh.requireSnippetWriter(c) {
		return
	}
	var req models.SnippetBatchDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format", "details": err.Error()})
		return
	}

	ctx := context.Background()
	collection := sh.handler.XDS.Context.Client.Collection("snippets")

	var deleted []string
	var failed []models.SnippetError

	for _, snippetID := range req.SnippetIDs {
		objID, err := primitive.ObjectIDFromHex(snippetID)
		if err != nil {
			failed = append(failed, models.SnippetError{
				ID:      snippetID,
				Error:   "invalid_id",
				Message: "Invalid snippet ID format",
			})
			continue
		}

		result, err := collection.DeleteOne(ctx, bson.M{"_id": objID})
		if err != nil {
			failed = append(failed, models.SnippetError{
				ID:      snippetID,
				Error:   "database_error",
				Message: err.Error(),
			})
			continue
		}

		if result.DeletedCount == 0 {
			failed = append(failed, models.SnippetError{
				ID:      snippetID,
				Error:   "not_found",
				Message: "Snippet not found",
			})
			continue
		}

		deleted = append(deleted, snippetID)
	}

	response := models.SnippetBatchDeleteResponse{
		Deleted: deleted,
		Failed:  failed,
	}

	statusCode := http.StatusOK
	if len(failed) > 0 {
		statusCode = http.StatusPartialContent
	}

	c.JSON(statusCode, response)
}

// GetSnippetStats returns statistics about snippets
func (sh *SnippetHandler) GetSnippetStats(c *gin.Context) {
	project := c.Query("project")

	ctx := context.Background()
	collection := sh.handler.XDS.Context.Client.Collection("snippets")

	filter := bson.M{}
	if project != "" {
		filter["project"] = project
	}

	// Total count
	total, err := collection.CountDocuments(ctx, filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get total count", "details": err.Error()})
		return
	}

	// Aggregation pipelines for statistics
	stats := models.SnippetStats{
		TotalSnippets:      total,
		SnippetsByProject:  make(map[string]int64),
		SnippetsByGType:    make(map[string]int64),
		SnippetsByVersion:  make(map[string]int64),
		MostUsedComponents: make(map[string]int64),
		RecentActivity:     []models.SnippetActivityItem{},
	}

	// Get stats by project (if no project filter)
	if project == "" {
		pipeline := mongo.Pipeline{
			{{Key: "$group", Value: bson.D{
				{Key: "_id", Value: "$project"},
				{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
			}}},
		}
		cursor, err := collection.Aggregate(ctx, pipeline)
		if err == nil {
			for cursor.Next(ctx) {
				var result struct {
					ID    string `bson:"_id"`
					Count int64  `bson:"count"`
				}
				if err := cursor.Decode(&result); err == nil {
					stats.SnippetsByProject[result.ID] = result.Count
				}
			}
			if err := cursor.Close(ctx); err != nil {
				// Log but don't fail - this is cleanup
				sh.handler.XDS.Context.Logger.Warnf("Failed to close cursor: %v", err)
			}
		}
	}

	// Get other statistics...
	// (Similar aggregation pipelines for GType, Version, Components)

	c.JSON(http.StatusOK, stats)
}

// SearchSnippets provides advanced search capabilities
func (sh *SnippetHandler) SearchSnippets(c *gin.Context) {
	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Search query is required"})
		return
	}

	project := c.Query("project")
	limitStr := c.DefaultQuery("limit", strconv.Itoa(models.DefaultSnippetLimit))
	limit, _ := strconv.Atoi(limitStr)

	ctx := context.Background()
	collection := sh.handler.XDS.Context.Client.Collection("snippets")

	// Build search filter
	searchFilter := bson.M{
		"$or": bson.A{
			bson.M{"name": bson.M{"$regex": query, "$options": "i"}},
			bson.M{"component_type": bson.M{"$regex": query, "$options": "i"}},
			bson.M{"field_path": bson.M{"$regex": query, "$options": "i"}},
		},
	}

	if project != "" {
		searchFilter["project"] = project
	}

	findOptions := options.Find()
	findOptions.SetLimit(int64(limit))
	findOptions.SetSort(bson.D{{Key: "created_at", Value: -1}})

	cursor, err := collection.Find(ctx, searchFilter, findOptions)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Search failed", "details": err.Error()})
		return
	}
	defer cursor.Close(ctx)

	var snippets []models.ResourceSnippet
	if err := cursor.All(ctx, &snippets); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to decode search results", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"query":   query,
		"results": snippets,
		"count":   len(snippets),
		"project": project,
	})
}
