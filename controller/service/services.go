package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/security"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type ServiceWithEnvoyStatus struct {
	*Service
	Status []string `json:"status"`
}

type ServiceWithStatus struct {
	*Service
	Status string `json:"status"`
}

func (s *AppHandler) ListServices(ctx context.Context, _ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	// Build base match filter
	matchFilter := bson.D{
		{Key: "project", Value: requestDetails.Project},
	}

	// Add permission checks for non-Owner/non-Admin users
	// Services have permissions but they were never checked - security vulnerability!
	if !requestDetails.User.IsOwner && requestDetails.User.Role != models.RoleAdmin {
		// Build complete group list including base_group
		allGroups := append([]string{}, requestDetails.User.Groups...)
		if requestDetails.User.BaseGroup != "" && !slices.Contains(allGroups, requestDetails.User.BaseGroup) {
			allGroups = append(allGroups, requestDetails.User.BaseGroup)
		}

		// Add permission filter: user must be in permissions.groups OR permissions.users
		matchFilter = append(matchFilter, bson.E{
			Key: "$or",
			Value: bson.A{
				bson.D{{Key: "permissions.groups", Value: bson.D{{Key: "$in", Value: allGroups}}}},
				bson.D{{Key: "permissions.users", Value: requestDetails.User.UserID}},
			},
		})
	}

	// Add filter conditions based on query parameters
	if requestDetails.Metadata != nil {
		if nameVal, ok := requestDetails.Metadata["name"]; ok {
			if nameVal != "" {
				if sanitizedName, valid := security.SanitizeSearchInput(nameVal); valid && sanitizedName != "" {
					matchFilter = append(matchFilter, bson.E{
						Key:   "name",
						Value: bson.D{{Key: "$regex", Value: sanitizedName}, {Key: "$options", Value: "i"}},
					})
				}
			}
		}

		if versionVal, ok := requestDetails.Metadata["version"]; ok {
			if versionVal != "" {
				matchFilter = append(matchFilter, bson.E{Key: "version", Value: versionVal})
			}
		}

		if downstreamVal, ok := requestDetails.Metadata["downstream_address"]; ok {
			if downstreamVal != "" {
				if sanitizedDownstream, valid := security.SanitizeSearchInput(downstreamVal); valid && sanitizedDownstream != "" {
					matchFilter = append(matchFilter, bson.E{
						Key:   "clients.downstream_address",
						Value: bson.D{{Key: "$regex", Value: sanitizedDownstream}, {Key: "$options", Value: "i"}},
					})
				}
			}
		}
	}

	// Build aggregation pipeline with filters
	pipeline := bson.A{
		bson.D{{Key: "$match", Value: matchFilter}},
		bson.D{
			{Key: "$lookup", Value: bson.D{
				{Key: "from", Value: "envoys"},
				{Key: "let", Value: bson.D{
					{Key: "name", Value: "$name"},
					{Key: "project", Value: "$project"},
				}},
				{Key: "pipeline", Value: bson.A{
					bson.D{
						{Key: "$match", Value: bson.D{
							{Key: "$expr", Value: bson.D{
								{Key: "$and", Value: bson.A{
									bson.D{{Key: "$eq", Value: bson.A{"$name", "$$name"}}},
									bson.D{{Key: "$eq", Value: bson.A{"$project", "$$project"}}},
								}},
							}},
						}},
					},
					bson.D{
						{Key: "$project", Value: bson.D{
							{Key: "status", Value: 1},
							{Key: "_id", Value: 0},
						}},
					},
				}},
				{Key: "as", Value: "envoy_statuses"},
			}},
		},
		bson.D{
			{Key: "$addFields", Value: bson.D{
				{Key: "status", Value: bson.D{
					{Key: "$ifNull", Value: bson.A{
						bson.D{{Key: "$arrayElemAt", Value: bson.A{"$envoy_statuses.status", 0}}},
						"Not_Deployed",
					}},
				}},
			}},
		},
	}

	// Add status filter if provided
	if requestDetails.Metadata != nil {
		if statusVal, ok := requestDetails.Metadata["status"]; ok && statusVal != "" {
			pipeline = append(pipeline, bson.D{
				{Key: "$match", Value: bson.D{
					{Key: "status", Value: statusVal},
				}},
			})
		}
	}

	// Add sorting
	pipeline = append(pipeline, bson.D{{Key: "$sort", Value: bson.D{{Key: "name", Value: 1}}}})

	// Get pagination parameters
	page := 1
	limit := 10
	if requestDetails.Metadata != nil {
		if pageVal, ok := requestDetails.Metadata["page"]; ok {
			if p, err := strconv.Atoi(pageVal); err == nil && p > 0 {
				page = p
			}
		}
		if limitVal, ok := requestDetails.Metadata["limit"]; ok {
			if l, err := strconv.Atoi(limitVal); err == nil && l > 0 && l <= 100 {
				limit = l
			}
		}
	}

	// Add pagination to pipeline
	skip := (page - 1) * limit

	// Add facet for both data and total count
	pipeline = append(pipeline, bson.D{
		{Key: "$facet", Value: bson.D{
			{Key: "data", Value: bson.A{
				bson.D{{Key: "$skip", Value: skip}},
				bson.D{{Key: "$limit", Value: limit}},
			}},
			{Key: "total", Value: bson.A{
				bson.D{{Key: "$count", Value: "count"}},
			}},
		}},
	})

	cursor, err := s.Context.Client.Collection("services").Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate services: %w", err)
	}
	defer cursor.Close(ctx)

	var aggregateResult []bson.M
	if err := cursor.All(ctx, &aggregateResult); err != nil {
		return nil, fmt.Errorf("failed to decode aggregate result: %w", err)
	}

	if len(aggregateResult) == 0 {
		return map[string]any{
			"data":       []ServiceWithStatus{},
			"total":      0,
			"page":       page,
			"limit":      limit,
			"totalPages": 0,
		}, nil
	}

	result := aggregateResult[0]
	data, ok := result["data"].(bson.A)
	if !ok {
		return nil, fmt.Errorf("unexpected data format")
	}

	totalArray, ok := result["total"].(bson.A)
	total := 0
	if ok && len(totalArray) > 0 {
		if totalDoc, ok := totalArray[0].(bson.M); ok {
			if count, ok := totalDoc["count"].(int32); ok {
				total = int(count)
			}
		}
	}

	var services []ServiceWithStatus
	for _, item := range data {
		svc, ok := item.(bson.M)
		if !ok {
			continue
		}

		var service Service
		bsonBytes, _ := bson.Marshal(svc)
		_ = bson.Unmarshal(bsonBytes, &service)

		status, _ := svc["status"].(string)
		services = append(services, ServiceWithStatus{
			Service: &service,
			Status:  status,
		})
	}

	totalPages := (total + limit - 1) / limit

	return map[string]any{
		"data":       services,
		"total":      total,
		"page":       page,
		"limit":      limit,
		"totalPages": totalPages,
	}, nil
}

func (s *AppHandler) GetService(ctx context.Context, _ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	if requestDetails.FromClient == "true" {
		return s.GetServicesByClientID(ctx, nil, requestDetails)
	}
	return s.GetSingleService(ctx, nil, requestDetails)
}

func (s *AppHandler) GetSingleService(ctx context.Context, _ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	objectID, err := primitive.ObjectIDFromHex(requestDetails.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service id: %w", err)
	}
	filter := bson.M{"_id": objectID, "project": requestDetails.Project}

	if !requestDetails.User.IsOwner && requestDetails.User.Role != models.RoleAdmin {
		allGroups := append([]string{}, requestDetails.User.Groups...)
		if requestDetails.User.BaseGroup != "" {
			found := false
			for _, g := range allGroups {
				if g == requestDetails.User.BaseGroup {
					found = true
					break
				}
			}
			if !found {
				allGroups = append(allGroups, requestDetails.User.BaseGroup)
			}
		}

		filter["$or"] = []bson.M{
			{"permissions.groups": bson.M{"$in": allGroups}},
			{"permissions.users": requestDetails.User.UserID},
		}
	}

	cursor := s.Context.Client.Collection("services").FindOne(ctx, filter)
	var service Service
	if err := cursor.Decode(&service); err != nil {
		return nil, fmt.Errorf("failed to decode service: %w", err)
	}

	cursor = s.Context.Client.Collection("envoys").FindOne(ctx, bson.M{"name": service.Name, "project": service.Project})
	var envoy models.Envoys
	err = cursor.Decode(&envoy)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			envoy = models.Envoys{}
		} else {
			return nil, fmt.Errorf("failed to decode envoy: %w", err)
		}
	}

	result := map[string]any{
		"service": service,
		"envoys":  envoy,
	}

	return result, nil
}

func (s *AppHandler) GetServicesByClientID(ctx context.Context, _ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	filter := bson.M{"clients.client_id": requestDetails.ClientID}

	// Add project filter if provided
	if requestDetails.Project != "" {
		filter["project"] = requestDetails.Project
	}

	cursor, err := s.Context.Client.Collection("services").Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to get services: %w", err)
	}
	defer cursor.Close(ctx)

	var result []*Service
	for cursor.Next(ctx) {
		var svc Service
		if err := cursor.Decode(&svc); err != nil {
			return nil, fmt.Errorf("failed to decode service: %w", err)
		}
		result = append(result, &svc)
	}

	return result, nil
}

func (s *AppHandler) GetEnvoyDetails(ctx context.Context, _ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	objectID, err := primitive.ObjectIDFromHex(requestDetails.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service id: %w", err)
	}
	cursor := s.Context.Client.Collection("services").FindOne(ctx, bson.M{"_id": objectID})
	var service Service
	if err := cursor.Decode(&service); err != nil {
		return nil, fmt.Errorf("failed to decode service: %w", err)
	}

	return &service, nil
}

// RecreateGSLBResponse represents the response for recreate-gslb endpoint
type RecreateGSLBResponse struct {
	Success       bool   `json:"success"`
	Message       string `json:"message"`
	GSLBRecordID  string `json:"gslb_record_id,omitempty"`
	FQDN          string `json:"fqdn,omitempty"`
	IPsAdded      int    `json:"ips_added"`
	IPsSkipped    int    `json:"ips_skipped"`
	AlreadyExists bool   `json:"already_exists"`
}

// RecreateGSLB creates GSLB record and IP health records for a service
// This is used for disaster recovery when GSLB records are lost after backup restore
// Note: Probe configuration will NOT be restored - user must configure probes manually after
// IMPORTANT: This function expects Admin/Owner authorization to be checked by the handler
func (s *AppHandler) RecreateGSLB(ctx context.Context, serviceID string, requestDetails models.RequestDetails) (*RecreateGSLBResponse, error) {
	// Step 1: Get service by ID
	objectID, err := primitive.ObjectIDFromHex(serviceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service id: %w", err)
	}

	// No permission filter needed - handler already enforces Admin/Owner only
	filter := bson.M{"_id": objectID, "project": requestDetails.Project}

	var service Service
	if err := s.Context.Client.Collection("services").FindOne(ctx, filter).Decode(&service); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("service not found")
		}
		return nil, fmt.Errorf("failed to get service: %w", err)
	}

	// Step 2: Get GSLB config from settings to check if GSLB is enabled
	settingsCollection := s.Context.Client.Collection("settings")
	var settings struct {
		GSLBConfig *models.GSLBConfig `bson:"gslb_config"`
	}

	settingsFilter := bson.M{"project": requestDetails.Project}
	if err := settingsCollection.FindOne(ctx, settingsFilter).Decode(&settings); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("GSLB not configured for this project")
		}
		return nil, fmt.Errorf("failed to get settings: %w", err)
	}

	if settings.GSLBConfig == nil || !settings.GSLBConfig.Enabled {
		return nil, fmt.Errorf("GSLB is not enabled for this project")
	}

	// Step 3: Check if GSLB record already exists
	gslbCollection := s.Context.Client.Collection("gslb_records")
	gslbFilter := bson.M{
		"service_id": serviceID,
		"project":    requestDetails.Project,
		"version":    service.Version,
	}

	var existingRecord models.GSLBRecord
	err = gslbCollection.FindOne(ctx, gslbFilter).Decode(&existingRecord)
	if err == nil {
		// Record already exists - just add missing IPs
		s.Logger.Infof("GSLB record already exists for service %s, adding missing IPs", serviceID)
		ipsAdded, ipsSkipped := s.addMissingIPsToGSLB(ctx, &existingRecord, &service)

		return &RecreateGSLBResponse{
			Success:       true,
			Message:       fmt.Sprintf("GSLB record already exists. Added %d IPs, skipped %d existing IPs.", ipsAdded, ipsSkipped),
			GSLBRecordID:  existingRecord.ID.Hex(),
			FQDN:          existingRecord.FQDN,
			IPsAdded:      ipsAdded,
			IPsSkipped:    ipsSkipped,
			AlreadyExists: true,
		}, nil
	}

	if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, fmt.Errorf("failed to check existing GSLB record: %w", err)
	}

	// Step 4: Create new GSLB record
	fqdn := s.normalizeFQDNWithZone(service.Name, settings.GSLBConfig.Zone)
	shardID := s.calculateShardID(fqdn)

	// Get default failover zone
	defaultFailoverZone := ""
	if len(settings.GSLBConfig.FailoverZones) > 0 {
		defaultFailoverZone = settings.GSLBConfig.FailoverZones[0]
	}

	now := time.Now()
	gslbRecord := models.GSLBRecord{
		FQDN:         fqdn,
		ServiceID:    serviceID,
		Project:      requestDetails.Project,
		Version:      service.Version,
		Zone:         settings.GSLBConfig.Zone,
		FailoverZone: defaultFailoverZone,
		ShardID:      shardID,
		Enabled:      true,
		TTL:          settings.GSLBConfig.DefaultTTL,
		Probe:        nil, // User must configure probes manually after recreation
		CreatedAt:    now,
		UpdatedAt:    now,
		CreatedBy:    requestDetails.User.UserID,
	}

	result, err := gslbCollection.InsertOne(ctx, gslbRecord)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, fmt.Errorf("GSLB record already exists (concurrent creation)")
		}
		return nil, fmt.Errorf("failed to create GSLB record: %w", err)
	}

	gslbRecord.ID = result.InsertedID.(primitive.ObjectID)
	s.Logger.Infof("Created GSLB record for service %s: %s (shard: %d)", serviceID, fqdn, shardID)

	// Step 5: Add IP health records for all clients
	ipsAdded, ipsSkipped := s.addMissingIPsToGSLB(ctx, &gslbRecord, &service)

	return &RecreateGSLBResponse{
		Success:      true,
		Message:      fmt.Sprintf("GSLB record created successfully. Added %d IPs to health tracking.", ipsAdded),
		GSLBRecordID: gslbRecord.ID.Hex(),
		FQDN:         fqdn,
		IPsAdded:     ipsAdded,
		IPsSkipped:   ipsSkipped,
	}, nil
}

// addMissingIPsToGSLB adds IP health records for all clients in the service that don't already exist
// Returns (ipsAdded, ipsSkipped) counts
func (s *AppHandler) addMissingIPsToGSLB(ctx context.Context, gslbRecord *models.GSLBRecord, service *Service) (int, int) {
	ipHealthCollection := s.Context.Client.Collection("gslb_ip_health")
	ipsAdded := 0
	ipsSkipped := 0

	for _, client := range service.Clients {
		if client.DownstreamAddress == "" {
			continue
		}

		// Check if IP already exists
		ipFilter := bson.M{
			"record_id": gslbRecord.ID,
			"ip":        client.DownstreamAddress,
		}

		count, err := ipHealthCollection.CountDocuments(ctx, ipFilter)
		if err != nil {
			s.Logger.Errorf("Failed to check existing IP %s: %v", client.DownstreamAddress, err)
			continue
		}

		if count > 0 {
			ipsSkipped++
			continue
		}

		// Calculate sub-shard for this IP
		subShardID := s.calculateSubShardID(gslbRecord.FQDN, client.DownstreamAddress)

		// Create IP health record with PASSING state (optimistic)
		ipHealth := models.NewGSLBIPHealth(
			gslbRecord.ID,
			gslbRecord.FQDN,
			client.DownstreamAddress,
			client.ClientID,
			gslbRecord.ShardID,
			subShardID,
		)

		_, err = ipHealthCollection.InsertOne(ctx, ipHealth)
		if err != nil {
			if mongo.IsDuplicateKeyError(err) {
				ipsSkipped++
				continue
			}
			s.Logger.Errorf("Failed to add IP %s to GSLB health: %v", client.DownstreamAddress, err)
			continue
		}

		s.Logger.Infof("Added IP %s to GSLB health for client %s (shard: %d/%d)",
			client.DownstreamAddress, client.ClientID, gslbRecord.ShardID, subShardID)
		ipsAdded++
	}

	return ipsAdded, ipsSkipped
}

// normalizeFQDNWithZone builds FQDN from service name and zone
func (s *AppHandler) normalizeFQDNWithZone(serviceName, zone string) string {
	// Normalize zone (ensure no leading dot, has trailing dot)
	normalizedZone := zone
	if len(normalizedZone) > 0 && normalizedZone[0] == '.' {
		normalizedZone = normalizedZone[1:]
	}
	if len(normalizedZone) > 0 && normalizedZone[len(normalizedZone)-1] != '.' {
		normalizedZone += "."
	}

	// Build FQDN: {service_name}.{zone}
	fqdn := serviceName + "." + normalizedZone

	// Ensure lowercase
	return toLower(fqdn)
}

// calculateShardID calculates shard ID from FQDN using FNV-1a hash
func (s *AppHandler) calculateShardID(fqdn string) int {
	// Normalize FQDN before hashing
	normalized := toLower(fqdn)
	if len(normalized) > 0 && normalized[len(normalized)-1] != '.' {
		normalized += "."
	}

	h := fnv32a(normalized)
	return int(h % uint32(models.GSLBNumShards))
}

// calculateSubShardID calculates sub-shard ID from FQDN and IP
func (s *AppHandler) calculateSubShardID(fqdn, ip string) int {
	normalized := toLower(fqdn)
	if len(normalized) > 0 && normalized[len(normalized)-1] != '.' {
		normalized += "."
	}
	combined := normalized + ip
	h := fnv32a(combined)
	return int(h % 8)
}

// fnv32a implements FNV-1a hash
func fnv32a(s string) uint32 {
	const prime32 = 16777619
	const offset32 = 2166136261

	h := uint32(offset32)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime32
	}
	return h
}

// toLower converts string to lowercase without importing strings package
func toLower(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		result[i] = c
	}
	return string(result)
}
