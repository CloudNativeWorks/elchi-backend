package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

const gslbNodePort = 8053

// --- Request / Response types ---

// notifyRequest is the simplified input from the frontend containing only FQDNs.
type notifyRequest struct {
	Records []string `json:"records"` // FQDNs to update (controller fetches full data from DB)
	Deletes []string `json:"deletes"` // FQDNs to delete
}

// notifyPayload is the complete payload forwarded to the GSLB node.
type notifyPayload struct {
	Records []DNSRecord        `json:"records,omitempty"`
	Deletes []notifyDeleteItem `json:"deletes,omitempty"`
}

type notifyDeleteItem struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// nodeNotifyResult captures the outcome of notifying a single GSLB node.
type nodeNotifyResult struct {
	NodeIP string `json:"node_ip"`
	Status int    `json:"status"`
	Error  string `json:"error,omitempty"`
}

// --- Helpers ---

func (h *GSLBHandler) resolveNodeByID(ctx context.Context, id string) (*models.GSLBNode, error) {
	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, fmt.Errorf("invalid node ID")
	}
	var node models.GSLBNode
	if err := h.db.Collection("gslb_nodes").FindOne(ctx, bson.M{"_id": objectID}).Decode(&node); err != nil {
		return nil, err
	}
	return &node, nil
}

func (h *GSLBHandler) resolveGSLBConfig(ctx context.Context, project string) (*models.GSLBConfig, error) {
	var settings models.Settings
	if err := h.db.Collection("settings").FindOne(ctx, bson.M{"project": project}).Decode(&settings); err != nil {
		return nil, err
	}
	if settings.GSLBConfig == nil || !settings.GSLBConfig.Enabled {
		return nil, fmt.Errorf("GSLB is not enabled for this project")
	}
	return settings.GSLBConfig, nil
}

func normalizeFQDN(fqdn string) string {
	fqdn = strings.TrimSpace(fqdn)
	if fqdn != "" && !strings.HasSuffix(fqdn, ".") {
		fqdn += "."
	}
	return fqdn
}

// forwardHTTP sends a single request to a GSLB node and returns status + body.
func (h *GSLBHandler) forwardHTTP(ctx context.Context, method, targetURL string, headers map[string]string, body io.Reader) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, targetURL, body)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, respBody, nil
}

// --- Payload builders ---

// buildRecordsForFQDNs is the same aggregation as DNSHandler.buildDNSRecords
// but filtered by specific FQDNs (no region filter, excludes critical IPs).
func (h *GSLBHandler) buildRecordsForFQDNs(ctx context.Context, zone string, fqdns []string) ([]DNSRecord, error) {
	normalized := make([]string, 0, len(fqdns))
	for _, f := range fqdns {
		normalized = append(normalized, normalizeFQDN(f))
	}

	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.M{
			"zone": zone,
			"fqdn": bson.M{"$in": normalized},
		}}},
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
				}}},
			}},
			{Key: "as", Value: "healthy_ips"},
		}}},
		bson.D{{Key: "$project", Value: bson.D{
			{Key: "fqdn", Value: 1},
			{Key: "ttl", Value: 1},
			{Key: "enabled", Value: 1},
			{Key: "healthy_ips", Value: 1},
		}}},
	}

	cursor, err := h.db.Collection("gslb_records").Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var agg []aggregatedRecord
	if err := cursor.All(ctx, &agg); err != nil {
		return nil, err
	}

	records := make([]DNSRecord, 0, len(agg))
	for _, r := range agg {
		ips := []string{}
		if r.Enabled {
			for _, hip := range r.HealthyIPs {
				ips = append(ips, hip.IP)
			}
		}
		records = append(records, DNSRecord{
			Name: r.FQDN,
			Type: "A",
			TTL:  r.TTL,
			IPs:  ips,
		})
	}

	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	return records, nil
}

func (h *GSLBHandler) assembleNotifyPayload(ctx context.Context, zone string, req notifyRequest) (*notifyPayload, error) {
	p := &notifyPayload{}

	if len(req.Records) > 0 {
		recs, err := h.buildRecordsForFQDNs(ctx, zone, req.Records)
		if err != nil {
			return nil, fmt.Errorf("build update records: %w", err)
		}
		p.Records = recs
	}

	if len(req.Deletes) > 0 {
		p.Deletes = make([]notifyDeleteItem, 0, len(req.Deletes))
		for _, fqdn := range req.Deletes {
			p.Deletes = append(p.Deletes, notifyDeleteItem{
				Name: normalizeFQDN(fqdn),
				Type: "A",
			})
		}
	}
	return p, nil
}

// --- Guard helpers (reduce boilerplate) ---

func (h *GSLBHandler) requireAdminOwner(c *gin.Context) (models.UserDetails, bool) {
	user, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return user, false
	}
	if !user.IsOwner && user.Role != models.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admin or Owner access required"})
		return user, false
	}
	return user, true
}

func (h *GSLBHandler) requireProject(c *gin.Context) (string, bool) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project parameter is required"})
		return "", false
	}
	return project, true
}

func (h *GSLBHandler) handleNodeLookupError(c *gin.Context, err error) {
	if errors.Is(err, mongo.ErrNoDocuments) {
		c.JSON(http.StatusNotFound, gin.H{"error": "GSLB node not found"})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
}

// --- Handlers ---

// ProxyNodeHealth proxies a health check to a specific GSLB node.
// GET /api/v3/gslb/nodes/:id/health
func (h *GSLBHandler) ProxyNodeHealth(c *gin.Context) {
	if _, ok := h.requireAdminOwner(c); !ok {
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	node, err := h.resolveNodeByID(ctx, c.Param("id"))
	if err != nil {
		h.handleNodeLookupError(c, err)
		return
	}

	targetURL := fmt.Sprintf("http://%s:%d/health", node.NodeIP, gslbNodePort)
	status, body, err := h.forwardHTTP(ctx, http.MethodGet, targetURL, nil, nil)
	if err != nil {
		h.logger.Errorf("Health check failed for node %s: %v", node.NodeIP, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("GSLB node unreachable: %v", err)})
		return
	}

	c.Data(status, "application/json", body)
}

// ProxyNodeRecords proxies a records query to a specific GSLB node.
// GET /api/v3/gslb/nodes/:id/records?project=X&name=Y&type=A
func (h *GSLBHandler) ProxyNodeRecords(c *gin.Context) {
	if _, ok := h.requireAdminOwner(c); !ok {
		return
	}
	project, ok := h.requireProject(c)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	gslbConfig, err := h.resolveGSLBConfig(ctx, project)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	node, err := h.resolveNodeByID(ctx, c.Param("id"))
	if err != nil {
		h.handleNodeLookupError(c, err)
		return
	}

	targetURL := fmt.Sprintf("http://%s:%d/records", node.NodeIP, gslbNodePort)
	params := url.Values{}
	if name := c.Query("name"); name != "" {
		params.Set("name", name)
	}
	if typ := c.Query("type"); typ != "" {
		params.Set("type", typ)
	}
	if q := params.Encode(); q != "" {
		targetURL += "?" + q
	}

	headers := map[string]string{"X-Elchi-Secret": gslbConfig.DNSSecret}
	status, body, err := h.forwardHTTP(ctx, http.MethodGet, targetURL, headers, nil)
	if err != nil {
		h.logger.Errorf("Records query failed for node %s: %v", node.NodeIP, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("GSLB node unreachable: %v", err)})
		return
	}

	c.Data(status, "application/json", body)
}

// ProxyNodeNotify builds DNS record payloads from MongoDB and sends them to a specific GSLB node.
// POST /api/v3/gslb/nodes/:id/notify?project=X
func (h *GSLBHandler) ProxyNodeNotify(c *gin.Context) {
	if _, ok := h.requireAdminOwner(c); !ok {
		return
	}
	project, ok := h.requireProject(c)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	var req notifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid input: %v", err)})
		return
	}
	if len(req.Records) == 0 && len(req.Deletes) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one record or delete FQDN is required"})
		return
	}

	gslbConfig, err := h.resolveGSLBConfig(ctx, project)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	node, err := h.resolveNodeByID(ctx, c.Param("id"))
	if err != nil {
		h.handleNodeLookupError(c, err)
		return
	}

	payload, err := h.assembleNotifyPayload(ctx, gslbConfig.Zone, req)
	if err != nil {
		h.logger.Errorf("Failed to build notify payload: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to build notify payload"})
		return
	}

	payloadBytes, _ := json.Marshal(payload)
	targetURL := fmt.Sprintf("http://%s:%d/notify", node.NodeIP, gslbNodePort)
	headers := map[string]string{"X-Elchi-Secret": gslbConfig.DNSSecret}

	status, body, err := h.forwardHTTP(ctx, http.MethodPost, targetURL, headers, bytes.NewReader(payloadBytes))
	if err != nil {
		h.logger.Errorf("Notify failed for node %s: %v", node.NodeIP, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("GSLB node unreachable: %v", err)})
		return
	}

	c.Data(status, "application/json", body)
}

// ProxyNotifyAll builds DNS record payloads once and broadcasts to ALL tracked nodes in the zone.
// POST /api/v3/gslb/nodes/notify-all?project=X
func (h *GSLBHandler) ProxyNotifyAll(c *gin.Context) {
	user, ok := h.requireAdminOwner(c)
	if !ok {
		return
	}
	project, ok := h.requireProject(c)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	var req notifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid input: %v", err)})
		return
	}
	if len(req.Records) == 0 && len(req.Deletes) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one record or delete FQDN is required"})
		return
	}

	gslbConfig, err := h.resolveGSLBConfig(ctx, project)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Build payload once for all nodes
	payload, err := h.assembleNotifyPayload(ctx, gslbConfig.Zone, req)
	if err != nil {
		h.logger.Errorf("Failed to build notify payload: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to build notify payload"})
		return
	}
	payloadBytes, _ := json.Marshal(payload)

	// Fetch all nodes in this zone
	cursor, err := h.db.Collection("gslb_nodes").Find(ctx, bson.M{"zone": gslbConfig.Zone})
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

	if len(nodes) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"total": 0, "success": 0, "failed": 0,
			"results": []nodeNotifyResult{},
		})
		return
	}

	// Fan-out to all nodes concurrently
	results := make([]nodeNotifyResult, len(nodes))
	headers := map[string]string{"X-Elchi-Secret": gslbConfig.DNSSecret}

	var wg sync.WaitGroup
	for i, node := range nodes {
		wg.Add(1)
		go func(idx int, n models.GSLBNode) {
			defer wg.Done()
			targetURL := fmt.Sprintf("http://%s:%d/notify", n.NodeIP, gslbNodePort)
			status, _, fwdErr := h.forwardHTTP(ctx, http.MethodPost, targetURL, headers, bytes.NewReader(payloadBytes))
			results[idx] = nodeNotifyResult{NodeIP: n.NodeIP, Status: status}
			if fwdErr != nil {
				results[idx].Error = fwdErr.Error()
				h.logger.Errorf("Notify failed for node %s: %v", n.NodeIP, fwdErr)
			}
		}(i, node)
	}
	wg.Wait()

	successCount := 0
	for _, r := range results {
		if r.Error == "" && r.Status >= 200 && r.Status < 300 {
			successCount++
		}
	}

	h.logger.Infof("User %s broadcast notify to %d nodes (zone=%s): %d success, %d failed",
		user.UserName, len(nodes), gslbConfig.Zone, successCount, len(nodes)-successCount)

	c.JSON(http.StatusOK, gin.H{
		"total":   len(nodes),
		"success": successCount,
		"failed":  len(nodes) - successCount,
		"results": results,
	})
}
