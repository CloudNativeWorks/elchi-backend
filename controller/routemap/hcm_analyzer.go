package routemap

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/controller/routemap/extractors"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
)

// AnalyzeHTTPConnectionManager analyzes HTTP Connection Manager filter for route mapping
func (ra *RouteAnalyzer) AnalyzeHTTPConnectionManager(ctx context.Context, req RouteAnalysisRequest) (*RouteMapGraph, error) {
	// Mark as visited
	visitKey := fmt.Sprintf("hcm_%s_%s", req.ResourceName, req.Project)
	if ra.markVisited(visitKey) {
		return ra.graph, nil
	}

	// Fetch the filter resource
	filterResource, err := ra.getResource(ctx, "filters", req.ResourceName, req.Project, req.Version)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch HTTP Connection Manager: %w", err)
	}

	// Extract the HCM configuration
	hcmConfig := filterResource.Resource.Resource
	if hcmConfig == nil {
		return nil, fmt.Errorf("HTTP Connection Manager configuration is empty")
	}

	// Create HCM filter node
	hcmNode := ra.createFilterNode(req.ResourceName, "http_connection_manager", map[string]any{
		"resource_id": filterResource.ID.Hex(),
	})
	ra.addNode(hcmNode)

	// Check for RDS (Route Discovery Service)
	if rdsName := extractors.ExtractRDS(hcmConfig); rdsName != "" {
		ra.logger.Infof("Found RDS configuration: %s", rdsName)

		// Fetch route configuration from RDS
		routeConfig, err := ra.getResource(ctx, "routes", rdsName, req.Project, req.Version)
		if err != nil {
			ra.logger.Warnf("Failed to fetch RDS route config %s: %v", rdsName, err)
		} else {
			// Create route config node - type will be "rds" based on source parameter
			routeConfigNode := ra.createRouteConfigNode(rdsName, "rds", routeConfig.ID.Hex())
			ra.addNode(routeConfigNode)
			ra.addEdge(hcmNode.Data.ID, routeConfigNode.Data.ID, "uses_rds", "rds_reference", nil)

			// Process route configuration with correct route config request
			routeReq := RouteAnalysisRequest{
				ResourceName: rdsName,
				Collection:   "routes",
				Project:      req.Project,
				Version:      req.Version,
			}
			ra.processRouteConfiguration(ctx, routeConfig.Resource.Resource, routeConfigNode.Data.ID, routeReq)
		}
	}

	// Check for inline route configuration
	if inlineRouteConfig := extractors.ExtractInlineRouteConfig(hcmConfig); inlineRouteConfig != nil {
		ra.logger.Info("Found inline route configuration")

		// Extract route config name from the inline configuration
		routeConfigName := extractors.ExtractString(inlineRouteConfig, "name")
		if routeConfigName == "" {
			routeConfigName = req.ResourceName + "_inline"
		}

		// Create inline route config node - type will be "route_config" based on source parameter
		routeConfigNode := ra.createRouteConfigNode(routeConfigName, "inline", "")
		ra.addNode(routeConfigNode)
		ra.addEdge(hcmNode.Data.ID, routeConfigNode.Data.ID, "has_inline_routes", "inline_routes", nil)

		// Process the inline route configuration - for inline routes, we use the HCM filter request since there's no separate route config resource
		ra.processRouteConfiguration(ctx, inlineRouteConfig, routeConfigNode.Data.ID, req)
	}

	return ra.graph, nil
}

// processRouteConfiguration processes a route configuration (from RDS or inline)
func (ra *RouteAnalyzer) processRouteConfiguration(ctx context.Context, routeConfig any, parentNodeID string, req RouteAnalysisRequest) {
	if routeConfig == nil {
		return
	}

	// Check for VHDS (Virtual Host Discovery Service)
	if extractors.ExtractVHDS(routeConfig) != "" {
		ra.logger.Info("VHDS configuration detected, checking config_discovery for VHDS names")

		// VHDS names come from the resource's config_discovery array
		vhdsNames, err := ra.getVHDSNamesFromResource(ctx, req)
		if err != nil {
			ra.logger.Warnf("Failed to get VHDS names from config_discovery: %v", err)
		} else if len(vhdsNames) > 0 {
			ra.logger.Infof("Found %d VHDS virtual hosts: %v", len(vhdsNames), vhdsNames)

			// Create VHDS node first (like in route_analyzer)
			vhdsNodeID := ra.generateNodeID("virtual_host", vhdsNames[0], req.Project)
			vhdsNode := RouteNode{
				Data: RouteNodeData{
					ID:       vhdsNodeID,
					Label:    vhdsNames[0], // general.name
					Type:     "virtual_host",
					Category: "virtual_host",
					Properties: map[string]any{
						"description": fmt.Sprintf("VHDS resource: %s", vhdsNames[0]),
					},
					Source: "vhds",
				},
			}
			ra.addNode(vhdsNode)
			ra.addEdge(parentNodeID, vhdsNodeID, "uses VHDS", "uses_vhds", nil)

			// Fetch virtual hosts from VHDS
			vhdsResources, err := ra.fetchVirtualHostResourcesFromVHDS(ctx, vhdsNames, req.Project, req.Version)
			if err != nil {
				ra.logger.Warnf("Failed to fetch VHDS virtual hosts: %v", err)
			} else {
				for _, vhResource := range vhdsResources {
					// Connect virtual hosts to VHDS node, not route config directly
					ra.processVirtualHostResource(vhResource, vhdsNodeID, "vhds")
				}
			}
		}
	}

	// Check for inline virtual hosts
	if virtualHosts := extractors.ExtractVirtualHosts(routeConfig); len(virtualHosts) > 0 {
		ra.logger.Infof("Found %d inline virtual hosts", len(virtualHosts))
		for vhIndex, vh := range virtualHosts {
			ra.processInlineVirtualHostFromRouteConfig(vh, parentNodeID, "inline", vhIndex)
		}
	}
}

// fetchVirtualHostResourcesFromVHDS fetches virtual host resources (including general.name) using VHDS names from config_discovery
func (ra *RouteAnalyzer) fetchVirtualHostResourcesFromVHDS(ctx context.Context, vhdsNames []string, project, version string) ([]*models.DBResource, error) {
	if len(vhdsNames) == 0 {
		return nil, nil
	}

	// Query virtual_hosts collection for specific names
	filter := bson.M{
		"general.name":    bson.M{"$in": vhdsNames},
		"general.project": project,
		"general.version": version,
	}

	cursor, err := ra.db.Client.Collection("virtual_hosts").Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var vhResources []*models.DBResource
	for cursor.Next(ctx) {
		var resource models.DBResource
		if err := cursor.Decode(&resource); err != nil {
			ra.logger.Warnf("Failed to decode virtual host: %v", err)
			continue
		}
		if resource.Resource.Resource != nil {
			vhResources = append(vhResources, &resource)
		}
	}

	return vhResources, nil
}

// getVHDSNamesFromResource gets VHDS virtual host names from resource's config_discovery
func (ra *RouteAnalyzer) getVHDSNamesFromResource(ctx context.Context, req RouteAnalysisRequest) ([]string, error) {
	// Fetch the resource to get config_discovery
	resource, err := ra.getResource(ctx, req.Collection, req.ResourceName, req.Project, req.Version)
	if err != nil {
		return nil, err
	}

	// Extract config_discovery from general field
	configDiscovery := extractors.ExtractArray(resource.General, "config_discovery")
	if len(configDiscovery) == 0 {
		return nil, nil
	}

	// Extract VHDS names from config_discovery
	vhdsNames := extractors.ExtractVHDSNamesFromConfigDiscovery(configDiscovery)
	return vhdsNames, nil
}

// processVirtualHostResource processes a single virtual host resource (for VHDS)
func (ra *RouteAnalyzer) processVirtualHostResource(vhResource *models.DBResource, parentNodeID string, source string) {
	// Use the shared function from route_analyzer for consistent processing
	ra.processVirtualHostResourceFromRouteConfig(vhResource, parentNodeID, source)
}
