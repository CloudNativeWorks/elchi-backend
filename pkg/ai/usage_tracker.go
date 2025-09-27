package ai

import (
	"context"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// calculateTokenCost calculates the cost in USD for token usage based on OpenRouter model pricing
func calculateTokenCost(inputTokens, outputTokens int, modelID string) float64 {
	// For now, assume all models are free since we removed the recommendation system
	// In a real implementation, you could query OpenRouter API for pricing info
	// or maintain a simple pricing cache

	// Simple heuristic: if model ID contains ":free", it's free
	if len(modelID) > 5 && modelID[len(modelID)-5:] == ":free" {
		return 0.0
	}

	// For non-free models, return 0.0 for now since we don't have pricing info
	// In production, you'd want to query OpenRouter API or maintain a pricing cache
	return 0.0
}

// AIUsageRecord represents a single AI usage record
type AIUsageRecord struct {
	ID           string    `json:"id,omitempty" bson:"_id,omitempty"`
	Project      string    `json:"project" bson:"project"`
	UserID       string    `json:"user_id" bson:"user_id"`
	RequestType  string    `json:"request_type" bson:"request_type"` // "analyze", "analyze-logs"
	ModelID      string    `json:"model_id" bson:"model_id"`         // OpenRouter model ID
	Provider     string    `json:"provider" bson:"provider"`         // "anthropic", "openai", "meta", etc.
	InputTokens  int       `json:"input_tokens" bson:"input_tokens"`
	OutputTokens int       `json:"output_tokens" bson:"output_tokens"`
	TotalTokens  int       `json:"total_tokens" bson:"total_tokens"`
	CostUSD      float64   `json:"cost_usd" bson:"cost_usd"`
	ResourceName string    `json:"resource_name,omitempty" bson:"resource_name,omitempty"`
	Collection   string    `json:"collection,omitempty" bson:"collection,omitempty"`
	Success      bool      `json:"success" bson:"success"`
	ErrorMessage string    `json:"error_message,omitempty" bson:"error_message,omitempty"`
	Duration     int64     `json:"duration_ms" bson:"duration_ms"` // milliseconds
	Timestamp    time.Time `json:"timestamp" bson:"timestamp"`
	CreatedAt    time.Time `json:"created_at" bson:"created_at"`
}

// AIUsageStats represents aggregated usage statistics
type AIUsageStats struct {
	Project             string    `json:"project"`
	TotalRequests       int       `json:"total_requests"`
	SuccessfulRequests  int       `json:"successful_requests"`
	FailedRequests      int       `json:"failed_requests"`
	TotalTokensUsed     int       `json:"total_tokens_used"`
	TotalInputTokens    int       `json:"total_input_tokens"`
	TotalOutputTokens   int       `json:"total_output_tokens"`
	TotalCostUSD        float64   `json:"total_cost_usd"`
	AnalyzeRequests     int       `json:"analyze_requests"`
	LogAnalyzeRequests  int       `json:"log_analyze_requests"`
	AverageResponseTime float64   `json:"average_response_time_ms"`
	LastUsed            time.Time `json:"last_used"`
	FirstUsed           time.Time `json:"first_used"`
	TokensToday         int       `json:"tokens_today"`
	TokensThisWeek      int       `json:"tokens_this_week"`
	TokensThisMonth     int       `json:"tokens_this_month"`
	CostToday           float64   `json:"cost_today_usd"`
	CostThisWeek        float64   `json:"cost_this_week_usd"`
	CostThisMonth       float64   `json:"cost_this_month_usd"`
	RequestsToday       int       `json:"requests_today"`
	RequestsThisWeek    int       `json:"requests_this_week"`
	RequestsThisMonth   int       `json:"requests_this_month"`
}

// UsageTracker handles AI usage tracking and statistics
type UsageTracker struct {
	dbContext *db.AppContext
	mu        sync.RWMutex
}

// NewUsageTracker creates a new usage tracker
func NewUsageTracker(dbContext *db.AppContext) *UsageTracker {
	return &UsageTracker{
		dbContext: dbContext,
	}
}

// RecordUsage records an AI usage event
func (ut *UsageTracker) RecordUsage(ctx context.Context, record AIUsageRecord) error {
	ut.mu.Lock()
	defer ut.mu.Unlock()

	// Set timestamps and calculate cost
	now := time.Now()
	record.Timestamp = now
	record.CreatedAt = now
	record.TotalTokens = record.InputTokens + record.OutputTokens
	record.CostUSD = calculateTokenCost(record.InputTokens, record.OutputTokens, record.ModelID)

	// Insert into ai_usage collection
	collection := ut.dbContext.Client.Collection("ai_usage")
	_, err := collection.InsertOne(ctx, record)
	return err
}

// GetUsageStats retrieves usage statistics for a project
func (ut *UsageTracker) GetUsageStats(ctx context.Context, project string) (*AIUsageStats, error) {
	ut.mu.RLock()
	defer ut.mu.RUnlock()

	collection := ut.dbContext.Client.Collection("ai_usage")

	// Time boundaries
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	startOfWeek := startOfDay.AddDate(0, 0, -int(now.Weekday()))
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	// Aggregation pipeline
	pipeline := []bson.M{
		{
			"$match": bson.M{
				"project": project,
			},
		},
		{
			"$group": bson.M{
				"_id":            "$project",
				"total_requests": bson.M{"$sum": 1},
				"successful_requests": bson.M{
					"$sum": bson.M{
						"$cond": bson.M{
							"if":   "$success",
							"then": 1,
							"else": 0,
						},
					},
				},
				"failed_requests": bson.M{
					"$sum": bson.M{
						"$cond": bson.M{
							"if":   "$success",
							"then": 0,
							"else": 1,
						},
					},
				},
				"total_tokens_used":   bson.M{"$sum": "$total_tokens"},
				"total_input_tokens":  bson.M{"$sum": "$input_tokens"},
				"total_output_tokens": bson.M{"$sum": "$output_tokens"},
				"total_cost_usd":      bson.M{"$sum": "$cost_usd"},
				"analyze_requests": bson.M{
					"$sum": bson.M{
						"$cond": bson.M{
							"if":   bson.M{"$eq": []interface{}{"$request_type", "analyze"}},
							"then": 1,
							"else": 0,
						},
					},
				},
				"log_analyze_requests": bson.M{
					"$sum": bson.M{
						"$cond": bson.M{
							"if":   bson.M{"$eq": []interface{}{"$request_type", "analyze-logs"}},
							"then": 1,
							"else": 0,
						},
					},
				},
				"average_response_time": bson.M{"$avg": "$duration_ms"},
				"last_used":             bson.M{"$max": "$timestamp"},
				"first_used":            bson.M{"$min": "$timestamp"},
				"tokens_today": bson.M{
					"$sum": bson.M{
						"$cond": bson.M{
							"if": bson.M{
								"$gte": []interface{}{"$timestamp", startOfDay},
							},
							"then": "$total_tokens",
							"else": 0,
						},
					},
				},
				"tokens_this_week": bson.M{
					"$sum": bson.M{
						"$cond": bson.M{
							"if": bson.M{
								"$gte": []interface{}{"$timestamp", startOfWeek},
							},
							"then": "$total_tokens",
							"else": 0,
						},
					},
				},
				"tokens_this_month": bson.M{
					"$sum": bson.M{
						"$cond": bson.M{
							"if": bson.M{
								"$gte": []interface{}{"$timestamp", startOfMonth},
							},
							"then": "$total_tokens",
							"else": 0,
						},
					},
				},
				"requests_today": bson.M{
					"$sum": bson.M{
						"$cond": bson.M{
							"if": bson.M{
								"$gte": []interface{}{"$timestamp", startOfDay},
							},
							"then": 1,
							"else": 0,
						},
					},
				},
				"requests_this_week": bson.M{
					"$sum": bson.M{
						"$cond": bson.M{
							"if": bson.M{
								"$gte": []interface{}{"$timestamp", startOfWeek},
							},
							"then": 1,
							"else": 0,
						},
					},
				},
				"cost_today": bson.M{
					"$sum": bson.M{
						"$cond": bson.M{
							"if": bson.M{
								"$gte": []interface{}{"$timestamp", startOfDay},
							},
							"then": "$cost_usd",
							"else": 0,
						},
					},
				},
				"cost_this_week": bson.M{
					"$sum": bson.M{
						"$cond": bson.M{
							"if": bson.M{
								"$gte": []interface{}{"$timestamp", startOfWeek},
							},
							"then": "$cost_usd",
							"else": 0,
						},
					},
				},
				"cost_this_month": bson.M{
					"$sum": bson.M{
						"$cond": bson.M{
							"if": bson.M{
								"$gte": []interface{}{"$timestamp", startOfMonth},
							},
							"then": "$cost_usd",
							"else": 0,
						},
					},
				},
				"requests_this_month": bson.M{
					"$sum": bson.M{
						"$cond": bson.M{
							"if": bson.M{
								"$gte": []interface{}{"$timestamp", startOfMonth},
							},
							"then": 1,
							"else": 0,
						},
					},
				},
			},
		},
	}

	cursor, err := collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var results []bson.M
	if err = cursor.All(ctx, &results); err != nil {
		return nil, err
	}

	// If no data found, return empty stats
	if len(results) == 0 {
		return &AIUsageStats{
			Project: project,
		}, nil
	}

	result := results[0]

	// Convert to AIUsageStats
	stats := &AIUsageStats{
		Project:             project,
		TotalRequests:       getIntFromBSON(result, "total_requests"),
		SuccessfulRequests:  getIntFromBSON(result, "successful_requests"),
		FailedRequests:      getIntFromBSON(result, "failed_requests"),
		TotalTokensUsed:     getIntFromBSON(result, "total_tokens_used"),
		TotalInputTokens:    getIntFromBSON(result, "total_input_tokens"),
		TotalOutputTokens:   getIntFromBSON(result, "total_output_tokens"),
		TotalCostUSD:        getFloatFromBSON(result, "total_cost_usd"),
		AnalyzeRequests:     getIntFromBSON(result, "analyze_requests"),
		LogAnalyzeRequests:  getIntFromBSON(result, "log_analyze_requests"),
		AverageResponseTime: getFloatFromBSON(result, "average_response_time"),
		TokensToday:         getIntFromBSON(result, "tokens_today"),
		TokensThisWeek:      getIntFromBSON(result, "tokens_this_week"),
		TokensThisMonth:     getIntFromBSON(result, "tokens_this_month"),
		CostToday:           getFloatFromBSON(result, "cost_today"),
		CostThisWeek:        getFloatFromBSON(result, "cost_this_week"),
		CostThisMonth:       getFloatFromBSON(result, "cost_this_month"),
		RequestsToday:       getIntFromBSON(result, "requests_today"),
		RequestsThisWeek:    getIntFromBSON(result, "requests_this_week"),
		RequestsThisMonth:   getIntFromBSON(result, "requests_this_month"),
	}

	// Handle time fields - MongoDB returns primitive.DateTime
	if lastUsed, ok := result["last_used"]; ok {
		switch v := lastUsed.(type) {
		case time.Time:
			stats.LastUsed = v
		case primitive.DateTime:
			stats.LastUsed = v.Time()
		}
	}
	if firstUsed, ok := result["first_used"]; ok {
		switch v := firstUsed.(type) {
		case time.Time:
			stats.FirstUsed = v
		case primitive.DateTime:
			stats.FirstUsed = v.Time()
		}
	}

	return stats, nil
}

// GetRecentUsage gets recent usage records for a project
func (ut *UsageTracker) GetRecentUsage(ctx context.Context, project string, limit int) ([]AIUsageRecord, error) {
	ut.mu.RLock()
	defer ut.mu.RUnlock()

	collection := ut.dbContext.Client.Collection("ai_usage")

	filter := bson.M{"project": project}
	opts := options.Find().
		SetSort(bson.M{"timestamp": -1}).
		SetLimit(int64(limit))

	cursor, err := collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var records []AIUsageRecord
	if err = cursor.All(ctx, &records); err != nil {
		return nil, err
	}

	return records, nil
}

// CleanupOldRecords removes usage records older than specified days
func (ut *UsageTracker) CleanupOldRecords(ctx context.Context, days int) error {
	ut.mu.Lock()
	defer ut.mu.Unlock()

	collection := ut.dbContext.Client.Collection("ai_usage")

	cutoffTime := time.Now().AddDate(0, 0, -days)
	filter := bson.M{
		"created_at": bson.M{
			"$lt": cutoffTime,
		},
	}

	result, err := collection.DeleteMany(ctx, filter)
	if err != nil {
		return err
	}

	if result.DeletedCount > 0 {
		// Log cleanup
		// Using simple log for now, can be enhanced with proper logging
		_ = result.DeletedCount
	}

	return nil
}

// Helper functions to safely extract values from BSON
func getIntFromBSON(data bson.M, key string) int {
	if val, ok := data[key]; ok {
		switch v := val.(type) {
		case int32:
			return int(v)
		case int64:
			return int(v)
		case int:
			return v
		}
	}
	return 0
}

func getFloatFromBSON(data bson.M, key string) float64 {
	if val, ok := data[key]; ok {
		switch v := val.(type) {
		case float32:
			return float64(v)
		case float64:
			return v
		case int32:
			return float64(v)
		case int64:
			return float64(v)
		}
	}
	return 0.0
}
