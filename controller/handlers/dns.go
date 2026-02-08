package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
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
	Failover string   `json:"failover,omitempty"` // Per-record failover FQDN
}

// DNSSnapshot represents the complete DNS snapshot response
type DNSSnapshot struct {
	Zone        string      `json:"zone"`         // DNS zone
	VersionHash string      `json:"version_hash"` // SHA256 hash of sorted records
	Records     []DNSRecord `json:"records"`      // DNS records
}

// aggregatedRecord is the internal representation from MongoDB aggregation
type aggregatedRecord struct {
	FQDN         string `bson:"fqdn"`
	TTL          uint32 `bson:"ttl"`
	Enabled      bool   `bson:"enabled"`
	FailoverZone string `bson:"failover_zone"`
	HealthyIPs   []struct {
		IP          string `bson:"ip"`
		HealthState string `bson:"health_state"`
	} `bson:"healthy_ips"`
}

// GetDNSSnapshot returns complete DNS snapshot for a zone
// Authentication: X-Elchi-Secret header must match GSLBConfig.DNSSecret
// GET /api/v3/dns/snapshot?zone=X&node_ip=Y&regions=asia,europe
func (h *DNSHandler) GetDNSSnapshot(c *gin.Context) {
	zone := c.Query("zone")
	nodeIP := c.Query("node_ip")

	if zone == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "zone parameter is required",
		})
		return
	}

	if nodeIP == "" || net.ParseIP(nodeIP) == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "valid node_ip parameter is required",
		})
		return
	}

	// Parse regions query parameter (comma-separated)
	regions := parseRegionsParam(c.Query("regions"))

	// Build DNS records from database
	records, err := h.buildDNSRecords(zone, regions)
	if err != nil {
		h.logger.Errorf("Failed to build DNS records: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to query DNS records",
		})
		return
	}

	// Calculate version hash (SHA256 of sorted records + regions for uniqueness)
	versionHash := h.calculateVersionWithRegions(records, regions)

	snapshot := DNSSnapshot{
		Zone:        zone,
		VersionHash: versionHash,
		Records:     records,
	}

	// Track GSLB node
	go h.trackGSLBNode(nodeIP, zone, versionHash)

	c.JSON(http.StatusOK, snapshot)
}

// GetDNSChanges returns incremental DNS changes since a specific version
// If "since" hash matches current version, returns HTTP 304 Not Modified
// Authentication: DNSAuthMiddleware validates X-Elchi-Secret header
// GET /dns/changes?zone=X&since=VERSION&node_ip=Y
func (h *DNSHandler) GetDNSChanges(c *gin.Context) {
	zone := c.Query("zone")
	since := c.Query("since")
	nodeIP := c.Query("node_ip")

	if zone == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "zone parameter is required",
		})
		return
	}

	if nodeIP == "" || net.ParseIP(nodeIP) == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "valid node_ip parameter is required",
		})
		return
	}

	// Parse regions query parameter (comma-separated)
	regions := parseRegionsParam(c.Query("regions"))

	// If "since" parameter is provided, check if data has changed
	if since != "" {
		// Build records and calculate current hash
		records, err := h.buildDNSRecords(zone, regions)
		if err != nil {
			h.logger.Errorf("Failed to build DNS records for 304 check: %v", err)
			// Fall through to full snapshot on error
		} else {
			currentHash := h.calculateVersionWithRegions(records, regions)

			// Track GSLB node regardless of 304 or 200
			go h.trackGSLBNode(nodeIP, zone, currentHash)

			if currentHash == since {
				// No changes - return 304 Not Modified
				h.logger.Debugf("Zone %s: No changes since %s, returning 304", zone, since[:min(16, len(since))])
				c.Status(http.StatusNotModified)
				return
			}

			// Hash mismatch - return full snapshot (we already have records, avoid re-querying)
			h.logger.Debugf("Zone %s: Changes detected (old: %s, new: %s), returning full snapshot",
				zone, since[:min(16, len(since))], currentHash[:min(16, len(currentHash))])

			snapshot := DNSSnapshot{
				Zone:        zone,
				VersionHash: currentHash,
				Records:     records,
			}
			c.JSON(http.StatusOK, snapshot)
			return
		}
	}

	// No "since" provided or error - return full snapshot via GetDNSSnapshot
	h.logger.Debugf("Zone %s: No since param, returning full snapshot", zone)
	h.GetDNSSnapshot(c)
}

// buildDNSRecords queries MongoDB and builds sorted DNS records for a zone
// regions parameter filters IPs by geographic region (empty = all IPs, ["all"] = all IPs)
func (h *DNSHandler) buildDNSRecords(zone string, regions []string) ([]DNSRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := h.db.Collection("gslb_records")
	filter := bson.M{
		"zone": zone,
	}

	// Build IP health match conditions for the $lookup sub-pipeline
	// Base conditions: match record_id and exclude critical health state
	ipMatchConditions := bson.A{
		bson.D{{Key: "$eq", Value: bson.A{"$record_id", "$$record_id"}}},
		bson.D{{Key: "$ne", Value: bson.A{"$health_state", "critical"}}},
	}

	// Add region filter if specific regions are requested (strict mode)
	// IPs without regions are EXCLUDED from regional queries
	if len(regions) > 0 {
		ipMatchConditions = append(ipMatchConditions,
			bson.D{{Key: "$gt", Value: bson.A{
				bson.D{{Key: "$size", Value: bson.D{
					{Key: "$setIntersection", Value: bson.A{
						bson.D{{Key: "$ifNull", Value: bson.A{"$regions", bson.A{}}}},
						regions,
					}},
				}}},
				0,
			}}},
		)
	}

	// Use aggregation pipeline to join GSLB records with IP health in a single query
	pipeline := mongo.Pipeline{
		// Match all GSLB records in this zone (disabled records get empty IPs for failover)
		bson.D{{Key: "$match", Value: filter}},

		// Lookup healthy IPs from gslb_ip_health collection
		bson.D{{Key: "$lookup", Value: bson.D{
			{Key: "from", Value: "gslb_ip_health"},
			{Key: "let", Value: bson.D{{Key: "record_id", Value: "$_id"}}},
			{Key: "pipeline", Value: bson.A{
				bson.D{{Key: "$match", Value: bson.D{
					{Key: "$expr", Value: bson.D{
						{Key: "$and", Value: ipMatchConditions},
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
			{Key: "enabled", Value: 1},
			{Key: "failover_zone", Value: 1},
			{Key: "healthy_ips", Value: 1},
		}}},
	}

	aggCursor, err := collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer aggCursor.Close(ctx)

	var aggRecords []aggregatedRecord
	if err := aggCursor.All(ctx, &aggRecords); err != nil {
		return nil, err
	}

	// Build DNS records from aggregated results
	records := make([]DNSRecord, 0, len(aggRecords))
	for _, aggRecord := range aggRecords {
		// Extract IPs from aggregated healthy_ips
		// If record is disabled, return empty IPs to trigger failover (controlled failover)
		var healthyIPs []string
		if aggRecord.Enabled {
			healthyIPs = make([]string, 0, len(aggRecord.HealthyIPs))
			for _, healthyIP := range aggRecord.HealthyIPs {
				healthyIPs = append(healthyIPs, healthyIP.IP)
			}
		} else {
			// Disabled record: empty IPs -> failover
			healthyIPs = []string{}
		}

		// Use per-record failover zone (stored in GSLB record)
		// Build per-record failover FQDN by replacing zone with failover zone
		// Example: "dedeff.atest.elchi." + zone "atest.elchi" + failover "btest.elchi" -> "dedeff.btest.elchi."
		recordFailover := ""
		if aggRecord.FailoverZone != "" {
			// Extract record name by removing zone suffix (optimized with TrimSuffix)
			// aggRecord.FQDN = "dedeff.atest.elchi.", zone = "atest.elchi"
			// FQDN ends with "." so suffix must also include trailing dot: ".zone."
			zoneSuffix := "." + zone + "."
			recordName := strings.TrimSuffix(aggRecord.FQDN, zoneSuffix)

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
			Failover: recordFailover,
		}

		if len(healthyIPs) > 0 {
			// Healthy IPs available -> return A record
			dnsRecord.IPs = healthyIPs
		} else {
			// No healthy IPs -> return empty (plugin handles failover/NXDOMAIN)
			dnsRecord.IPs = []string{}
		}

		records = append(records, dnsRecord)
	}

	// Sort records by Name for consistent hashing
	sort.Slice(records, func(i, j int) bool {
		return records[i].Name < records[j].Name
	})

	return records, nil
}

// trackGSLBNode upserts the gslb_nodes collection to track which elchi-gslb instances are syncing
func (h *DNSHandler) trackGSLBNode(nodeIP, zone, versionHash string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	collection := h.db.Collection("gslb_nodes")
	now := time.Now()

	filter := bson.M{
		"node_ip": nodeIP,
		"zone":    zone,
	}

	update := bson.M{
		"$set": bson.M{
			"last_seen":         now,
			"last_version_hash": versionHash,
		},
		"$setOnInsert": bson.M{
			"first_seen": now,
		},
		"$inc": bson.M{
			"request_count": int64(1),
		},
	}

	opts := options.Update().SetUpsert(true)
	if _, err := collection.UpdateOne(ctx, filter, update, opts); err != nil {
		h.logger.Errorf("Failed to track GSLB node %s for zone %s: %v", nodeIP, zone, err)
	}
}

// parseRegionsParam parses the comma-separated regions query parameter
// Returns nil if empty or "all" (meaning: return all IPs without region filtering)
func parseRegionsParam(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "all") {
		return nil
	}

	parts := strings.Split(raw, ",")
	regions := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			regions = append(regions, trimmed)
		}
	}

	if len(regions) == 0 {
		return nil
	}
	return regions
}

// calculateVersionWithRegions calculates SHA256 hash of sorted DNS records
// Includes regions in hash input so different region queries produce different version hashes
func (h *DNSHandler) calculateVersionWithRegions(records []DNSRecord, regions []string) string {
	// Serialize records to JSON
	data, err := json.Marshal(records)
	if err != nil {
		h.logger.Errorf("Failed to marshal records for version calculation: %v", err)
		return ""
	}

	// Include regions in hash to differentiate per-region snapshots
	hashInput := data
	if len(regions) > 0 {
		sort.Strings(regions)
		regionKey := "regions:" + strings.Join(regions, ",")
		hashInput = append(hashInput, []byte(regionKey)...)
	}

	// Calculate SHA256
	hash := sha256.Sum256(hashInput)
	return hex.EncodeToString(hash[:])
}
