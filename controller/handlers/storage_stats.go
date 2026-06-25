package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/CloudNativeWorks/elchi-backend/pkg/clickhouse"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// StorageStatsResponse is the live storage picture for Settings → General.
type StorageStatsResponse struct {
	GeneratedAt         time.Time             `json:"generated_at"`
	ClickHouseAvailable bool                  `json:"clickhouse_available"`
	ClickHouse          *clickhouse.CHStorage `json:"clickhouse,omitempty"`
	MongoDB             MongoStorageStats     `json:"mongodb"`
}

// MongoStorageStats is the MongoDB footprint. Unlike ClickHouse it is
// cardinality-bound (distinct endpoints + config), not request-volume-bound —
// so "new endpoints in the last hour" is the meaningful growth signal.
type MongoStorageStats struct {
	DataBytes    int64               `json:"data_bytes"`
	StorageBytes int64               `json:"storage_bytes"`
	IndexBytes   int64               `json:"index_bytes"`
	FsUsedBytes  int64               `json:"fs_used_bytes"`
	FsTotalBytes int64               `json:"fs_total_bytes"`
	Inventory    MongoInventoryStats `json:"inventory"`
}

type MongoInventoryStats struct {
	Endpoints   int64 `json:"endpoints"`
	NewLastHour int64 `json:"new_last_hour"`
	NewLast24h  int64 `json:"new_last_24h"`
}

// GetStorageStats handles GET /api/v3/setting/storage-stats — a global,
// owner/admin-only live view of ClickHouse + MongoDB disk usage and growth.
// ClickHouse being unconfigured degrades gracefully (mongodb still returned).
func (h *Handler) GetStorageStats(c *gin.Context) {
	_, ud := h.getRequestDetails(c)
	if !(ud.IsOwner || ud.Role == models.RoleAdmin) {
		c.JSON(http.StatusForbidden, gin.H{"message": "only Owner and Admin can view storage stats"})
		return
	}

	if h.Inventory == nil || h.Inventory.Context == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "storage stats unavailable"})
		return
	}
	ctx := c.Request.Context()
	appCtx := h.Inventory.Context
	resp := StorageStatsResponse{GeneratedAt: time.Now().UTC()}

	// ClickHouse (optional). Retention 7d matches the collector RETENTION_DAYS and
	// the shield audit TTL defaults (the projection target window).
	if appCtx != nil && appCtx.Clickhouse != nil {
		ch, err := appCtx.Clickhouse.QueryStorageStats(ctx, 7)
		if err != nil {
			h.Inventory.Logger.Errorf("storage-stats: clickhouse query failed: %v", err)
		} else {
			resp.ClickHouseAvailable = true
			resp.ClickHouse = &ch
		}
	}

	// MongoDB (always available — it is the system of record). dbStats sizes can
	// come back as BSON double OR int depending on scale/version, so decode into a
	// raw map and coerce numerically (a typed int64 struct would error on a double).
	if appCtx.Client != nil {
		var raw bson.M
		if err := appCtx.Client.RunCommand(ctx, bson.D{{Key: "dbStats", Value: 1}}).Decode(&raw); err != nil {
			h.Inventory.Logger.Errorf("storage-stats: mongo dbStats failed: %v", err)
		} else {
			resp.MongoDB.DataBytes = bsonInt64(raw["dataSize"])
			resp.MongoDB.StorageBytes = bsonInt64(raw["storageSize"])
			resp.MongoDB.IndexBytes = bsonInt64(raw["indexSize"])
			resp.MongoDB.FsUsedBytes = bsonInt64(raw["fsUsedSize"])
			resp.MongoDB.FsTotalBytes = bsonInt64(raw["fsTotalSize"])
		}

		inv := appCtx.Client.Collection("api_inventory")
		if n, err := inv.EstimatedDocumentCount(ctx); err == nil {
			resp.MongoDB.Inventory.Endpoints = n
		}
		if n, err := inv.CountDocuments(ctx, bson.M{"created_at": bson.M{"$gte": time.Now().Add(-1 * time.Hour)}}); err == nil {
			resp.MongoDB.Inventory.NewLastHour = n
		}
		if n, err := inv.CountDocuments(ctx, bson.M{"created_at": bson.M{"$gte": time.Now().Add(-24 * time.Hour)}}); err == nil {
			resp.MongoDB.Inventory.NewLast24h = n
		}
	}

	c.JSON(http.StatusOK, gin.H{"data": resp})
}

// bsonInt64 coerces a BSON numeric (int32/int64/double) to int64; 0 otherwise.
func bsonInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}
