package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// DNSHandler handles DNS snapshot and changes API
type DNSHandler struct {
	db     *mongo.Database
	logger *logger.Logger
}

// NewDNSHandler creates a new DNS handler
func NewDNSHandler(appContext *db.AppContext) *DNSHandler {
	return &DNSHandler{
		db:     appContext.Client,
		logger: logger.NewLogger("gslb/dns-handler"),
	}
}

// DNSRecord represents a DNS record in the snapshot
type DNSRecord struct {
	Name     string   `json:"name"`               // FQDN of the record
	Type     string   `json:"type"`               // "A" or "CNAME"
	TTL      uint32   `json:"ttl"`                // Time to live
	IPs      []string `json:"ips"`                // Healthy IPs (empty triggers failover)
	Enabled  bool     `json:"enabled"`            // Record enabled status
	Failover string   `json:"failover,omitempty"` // Per-record failover FQDN
}

// DNSSnapshot represents the complete DNS snapshot response
type DNSSnapshot struct {
	Zone        string      `json:"zone"`         // DNS zone
	VersionHash string      `json:"version_hash"` // SHA256 hash of sorted records
	Records     []DNSRecord `json:"records"`      // DNS records
}

// GetDNSSnapshot returns complete DNS snapshot for a zone
// Authentication: X-Elchi-DNS-Secret header must match GSLBConfig.DNSSecret
// GET /api/v3/dns/snapshot?zone=X
func (h *DNSHandler) GetDNSSnapshot(c *gin.Context) {
	zone := c.Query("zone")

	if zone == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "zone parameter is required",
		})
		return
	}

	// Get GSLB records for this zone (across ALL projects)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := h.db.Collection("gslb_records")
	filter := bson.M{
		"zone":    zone,
		"enabled": true,
	}

	// NOTE: Failover zone is now per-record (stored in GSLB record)
	// No need to query settings for zone-level failover anymore

	// Use aggregation pipeline to join GSLB records with IP health in a single query
	pipeline := mongo.Pipeline{
		// Match enabled GSLB records in this zone/project (already filtered above)
		bson.D{{Key: "$match", Value: filter}},

		// Lookup healthy IPs from gslb_ip_health collection
		bson.D{{Key: "$lookup", Value: bson.D{
			{Key: "from", Value: "gslb_ip_health"},
			{Key: "let", Value: bson.D{{Key: "record_id", Value: "$_id"}}},
			{Key: "pipeline", Value: bson.A{
				bson.D{{Key: "$match", Value: bson.D{
					{Key: "$expr", Value: bson.D{
						{Key: "$and", Value: bson.A{
							bson.D{{Key: "$eq", Value: bson.A{"$record_id", "$$record_id"}}},
							bson.D{{Key: "$ne", Value: bson.A{"$health_state", "critical"}}},
						}},
					}},
				}}},
				bson.D{{Key: "$project", Value: bson.D{
					{Key: "ip", Value: 1},
					{Key: "health_state", Value: 1},
				}}},
			}},
			{Key: "as", Value: "healthy_ips"},
		}}},

		// Project only needed fields
		bson.D{{Key: "$project", Value: bson.D{
			{Key: "fqdn", Value: 1},
			{Key: "ttl", Value: 1},
			{Key: "failover_zone", Value: 1},
			{Key: "healthy_ips", Value: 1},
		}}},
	}

	aggCursor, err := collection.Aggregate(ctx, pipeline)
	if err != nil {
		h.logger.Errorf("Failed to execute aggregation pipeline: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to query DNS records",
		})
		return
	}
	defer aggCursor.Close(ctx)

	// Process aggregated results
	type AggregatedRecord struct {
		FQDN         string `bson:"fqdn"`
		TTL          uint32 `bson:"ttl"`
		FailoverZone string `bson:"failover_zone"`
		HealthyIPs   []struct {
			IP          string `bson:"ip"`
			HealthState string `bson:"health_state"`
		} `bson:"healthy_ips"`
	}

	var aggRecords []AggregatedRecord
	if err := aggCursor.All(ctx, &aggRecords); err != nil {
		h.logger.Errorf("Failed to decode aggregated records: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to decode DNS records",
		})
		return
	}

	// Build DNS records from aggregated results
	records := make([]DNSRecord, 0, len(aggRecords))
	for _, aggRecord := range aggRecords {
		// Extract IPs from aggregated healthy_ips
		healthyIPs := make([]string, 0, len(aggRecord.HealthyIPs))
		for _, healthyIP := range aggRecord.HealthyIPs {
			healthyIPs = append(healthyIPs, healthyIP.IP)
		}

		// Use per-record failover zone (stored in GSLB record)
		// Build per-record failover FQDN by replacing zone with failover zone
		// Example: "dedeff.atest.elchi." + zone "atest.elchi" + failover "btest.elchi" → "dedeff.btest.elchi."
		recordFailover := ""
		if aggRecord.FailoverZone != "" {
			// Extract record name by removing zone suffix (optimized with TrimSuffix)
			// aggRecord.FQDN = "dedeff.atest.elchi.", zone = "atest.elchi"
			// Remove ".zone." → "dedeff"
			zoneSuffix := "." + zone
			recordName := strings.TrimSuffix(aggRecord.FQDN, zoneSuffix)
			recordName = strings.TrimSuffix(recordName, ".") // Remove any trailing dot

			// Reconstruct with failover zone: "dedeff" + "." + "btest.elchi" + "."
			recordFailover = recordName + "." + aggRecord.FailoverZone
			if !strings.HasSuffix(recordFailover, ".") {
				recordFailover += "."
			}
		}

		// Build DNS record
		dnsRecord := DNSRecord{
			Name:     aggRecord.FQDN,
			Type:     "A",
			TTL:      aggRecord.TTL,
			Enabled:  true, // All records in query are enabled (filtered in pipeline)
			Failover: recordFailover,
		}

		if len(healthyIPs) > 0 {
			// Healthy IPs available → return A record
			dnsRecord.IPs = healthyIPs
		} else if aggRecord.FailoverZone != "" {
			// No healthy IPs but failover configured → return empty (plugin will create CNAME)
			dnsRecord.IPs = []string{}
			// Note: DNS plugin will see empty IPs and create CNAME to failover zone
		} else {
			// No healthy IPs and no failover → return empty (plugin will return NXDOMAIN)
			dnsRecord.IPs = []string{}
		}

		records = append(records, dnsRecord)
	}

	// Sort records by Name for consistent hashing
	sort.Slice(records, func(i, j int) bool {
		return records[i].Name < records[j].Name
	})

	// Calculate version hash (SHA256 of sorted records)
	versionHash := h.calculateVersion(records)

	snapshot := DNSSnapshot{
		Zone:        zone,
		VersionHash: versionHash,
		Records:     records,
	}

	c.JSON(http.StatusOK, snapshot)
}

// GetDNSChanges returns incremental DNS changes since a specific version
// Authentication: DNSAuthMiddleware validates X-Elchi-DNS-Secret header
// GET /dns/changes?zone=X&since=VERSION
func (h *DNSHandler) GetDNSChanges(c *gin.Context) {
	zone := c.Query("zone")
	since := c.Query("since")

	if zone == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "zone parameter is required",
		})
		return
	}

	// For now, return full snapshot (incremental changes can be implemented later)
	// This is acceptable because DNS plugin will detect version mismatch and fetch full snapshot
	h.logger.Debugf("Incremental changes requested (since: %s), returning full snapshot", since)
	h.GetDNSSnapshot(c)
}

// calculateVersion calculates SHA256 hash of sorted DNS records
func (h *DNSHandler) calculateVersion(records []DNSRecord) string {
	// Serialize records to JSON
	data, err := json.Marshal(records)
	if err != nil {
		h.logger.Errorf("Failed to marshal records for version calculation: %v", err)
		return ""
	}

	// Calculate SHA256
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
