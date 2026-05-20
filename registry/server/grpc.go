// Package server provides gRPC and HTTP server implementations for the registry service
// handling controller and control-plane registration and routing.
package server

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/registry/metrics"
	"github.com/CloudNativeWorks/elchi-backend/registry/service"
	core "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/core/v3"
	ext "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Constants for header keys
const (
	HeaderNodeID         = "nodeid"
	HeaderClientID       = "clientid"
	HeaderVersion        = "envoy-version"
	HeaderTargetCluster  = "x-target-cluster"
	HeaderRoutingService = "x-routing-service"
	HeaderPath           = ":path"
	HeaderMethod         = ":method"
	HeaderContentType    = "content-type"
	HeaderGRPCAccept     = "accept"
)

// ExcludedPaths contains paths that should bypass routing
var ExcludedPaths = []string{
	"/healthz",
	"/ready",
	"/metrics",
	"/favicon.ico",
	"/api/v1/metrics",
	"/api/v1/health",
	"/opentelemetry.proto.collector.metrics.v1.MetricsService/Export",
	"/api/v1/query_range",
}

// ExternalProcessorServer implements Envoy's ext_proc protocol
type ExternalProcessorServer struct {
	ext.UnimplementedExternalProcessorServer
	controllerRoutingService   *service.ControllerRoutingService // For client routing
	controlPlaneRoutingService *service.RoutingService           // For control-plane routing
	logger                     *logger.Logger
	excludedPaths              map[string]bool // For O(1) lookup
	leader                     LeaderChecker   // nil = always serve (single-node legacy)

	// Mutex for atomic controller assignment
	assignmentMutex sync.RWMutex
	// Pending assignments to prevent race conditions
	pendingClientAssignments map[string]string // clientID -> controllerID
	pendingNodeAssignments   map[string]string // nodeID -> controlPlaneID
}

// NewExternalProcessorServer creates a new external processor server. leader
// may be nil; when present, RPCs are rejected on standby instances.
func NewExternalProcessorServer(controllerRoutingService *service.ControllerRoutingService, controlPlaneRoutingService *service.RoutingService, logger *logger.Logger, leader LeaderChecker) *ExternalProcessorServer {
	// Convert excluded paths slice to map for O(1) lookup
	excludedPaths := make(map[string]bool)
	for _, path := range ExcludedPaths {
		excludedPaths[path] = true
	}

	return &ExternalProcessorServer{
		controllerRoutingService:   controllerRoutingService,
		controlPlaneRoutingService: controlPlaneRoutingService,
		logger:                     logger,
		excludedPaths:              excludedPaths,
		leader:                     leader,
		pendingClientAssignments:   make(map[string]string),
		pendingNodeAssignments:     make(map[string]string),
	}
}

// GetAllRegistryData handles get all control plane registry data requests
func (s *ControlPlaneGRPCServer) GetAllRegistryData(ctx context.Context, req *bridge.GetAllRegistryDataRequest) (*bridge.GetAllRegistryDataResponse, error) {
	s.logger.Infof("Getting all control plane registry data")

	data, err := s.controlPlaneRoutingService.ListAllData(ctx)
	if err != nil {
		s.logger.Errorf("Failed to get all control plane registry data: %v", err)
		return nil, status.Errorf(codes.Internal, "failed to get registry data: %v", err)
	}

	// Convert to proto
	var protoControlPlanes []*bridge.ControlPlaneInfo
	for _, cp := range data.ControlPlanes {
		protoControlPlanes = append(protoControlPlanes, &bridge.ControlPlaneInfo{
			ControlPlaneId: cp.ID,
			Version:        cp.Version,
			LastSeen:       timestamppb.New(cp.LastSeen),
		})
	}

	nodesByControlPlane := make(map[string]*bridge.NodesData)
	for controlPlaneID, nodes := range data.NodesByControlPlane {
		var protoNodes []*bridge.NodeInfo
		for _, node := range nodes {
			protoNodes = append(protoNodes, &bridge.NodeInfo{
				NodeId:   node.NodeID,
				Version:  node.Version,
				LastSeen: timestamppb.New(node.LastSeen),
			})
		}
		nodesByControlPlane[controlPlaneID] = &bridge.NodesData{
			Nodes: protoNodes,
		}
	}

	s.logger.Infof("Returning registry data: %d control planes, %d control-plane-node mappings", len(protoControlPlanes), len(nodesByControlPlane))
	return &bridge.GetAllRegistryDataResponse{
		Data: &bridge.RegistryData{
			ControlPlanes:       protoControlPlanes,
			NodesByControlPlane: nodesByControlPlane,
		},
	}, nil
}

// Process handles Envoy's ext_proc bidirectional stream
func (p *ExternalProcessorServer) Process(stream ext.ExternalProcessor_ProcessServer) error {
	// Removed debug logging for ext_proc streams to reduce noise

	// Reject ext_proc streams on standby instances. Envoy upstream cluster
	// health-check (gRPC SERVING/NOT_SERVING) keeps these connections off in
	// the first place, but enforce here defensively.
	if p.leader != nil && !p.leader.IsLeader() {
		return status.Error(codes.Unavailable, "registry is not the active leader")
	}

	for {
		req, err := stream.Recv()
		if err != nil {
			if err.Error() == "EOF" {
				// Stream closed normally - no logging needed
				return nil
			}

			// Check if it's a context canceled error (normal when Envoy closes stream)
			if strings.Contains(err.Error(), "context canceled") || strings.Contains(err.Error(), "Canceled") {
				// Normal Envoy stream cancellation - no logging needed (reduces noise)
				return nil
			}

			p.logger.Warnf("Error receiving request: %v", err)
			return err
		}

		// Handle different types of processing requests
		switch req.Request.(type) {
		case *ext.ProcessingRequest_RequestHeaders:
			if err := p.handleRequestHeaders(stream, req.GetRequestHeaders()); err != nil {
				return err
			}
		case *ext.ProcessingRequest_RequestBody:
			if err := p.handleRequestBody(stream, req.GetRequestBody()); err != nil {
				return err
			}
		case *ext.ProcessingRequest_RequestTrailers:
			if err := p.handleRequestTrailers(stream, req.GetRequestTrailers()); err != nil {
				return err
			}
		case *ext.ProcessingRequest_ResponseHeaders:
			if err := p.handleResponseHeaders(stream, req.GetResponseHeaders()); err != nil {
				return err
			}
		case *ext.ProcessingRequest_ResponseBody:
			if err := p.handleResponseBody(stream, req.GetResponseBody()); err != nil {
				return err
			}
		case *ext.ProcessingRequest_ResponseTrailers:
			if err := p.handleResponseTrailers(stream, req.GetResponseTrailers()); err != nil {
				return err
			}
		default:
			p.logger.Errorf("Unknown request type: %T", req.Request)
			// Send continue response for unknown types
			if err := stream.Send(&ext.ProcessingResponse{
				Response: &ext.ProcessingResponse_RequestHeaders{
					RequestHeaders: &ext.HeadersResponse{
						Response: &ext.CommonResponse{
							Status: ext.CommonResponse_CONTINUE,
						},
					},
				},
			}); err != nil {
				return err
			}
		}
	}
}

// handleRequestHeaders processes request headers and adds routing information
func (p *ExternalProcessorServer) handleRequestHeaders(stream ext.ExternalProcessor_ProcessServer, headers *ext.HttpHeaders) error {
	if headers == nil || headers.Headers == nil {
		return stream.Send(p.createContinueResponse())
	}

	// First check path for early exclude
	var path string
	for _, header := range headers.Headers.Headers {
		if header != nil && header.Key == ":path" {
			path = string(header.RawValue)
			break
		}
	}

	// Early return for excluded paths
	if path != "" {
		// First check exact path match
		if p.excludedPaths[path] {
			return stream.Send(p.createContinueResponse())
		}

		// Then check if it's a gRPC service path that should be excluded
		// gRPC path format: /package.service/method
		parts := strings.Split(path, "/")
		if len(parts) > 2 {
			servicePath := strings.Join(parts[1:len(parts)-1], "/") // Get everything except the method
			if p.excludedPaths[servicePath] {
				return stream.Send(p.createContinueResponse())
			}
		}
	}

	// Check if this is a gRPC request
	contentType := p.getHeaderFromMap(headers.Headers, HeaderContentType)
	method := p.getHeaderFromMap(headers.Headers, HeaderMethod)

	isGRPC := strings.HasPrefix(contentType, "application/grpc")

	// If not gRPC, continue without processing
	if !isGRPC {
		p.logger.Infof("Non-gRPC request received: %s %s - continuing without processing", method, path)
		return stream.Send(p.createContinueResponse())
	}

	// For gRPC requests, proceed with routing
	p.logger.Infof("=== Processing gRPC Request ===")
	p.logger.Infof("Path: %s", path)
	p.logger.Infof("Method: %s", method)
	p.logger.Infof("Content-Type: %s", contentType)

	// Extract routing information from headers
	nodeID := p.getHeaderFromMap(headers.Headers, HeaderNodeID)
	clientID := p.getHeaderFromMap(headers.Headers, HeaderClientID)
	version := p.getHeaderFromMap(headers.Headers, HeaderVersion)

	// Also check for alternative header names
	if nodeID == "" {
		nodeID = p.getHeaderFromMap(headers.Headers, "nodeid")
	}
	if clientID == "" {
		clientID = p.getHeaderFromMap(headers.Headers, "client-id")
	}
	if version == "" {
		version = p.getHeaderFromMap(headers.Headers, "envoy-version")
	}

	p.logger.Infof("NodeID: %s", nodeID)
	p.logger.Infof("ClientID: %s", clientID)
	p.logger.Infof("Version: %s", version)

	// Determine routing type and get target cluster
	var targetCluster string
	if clientID != "" {
		// Controller routing (client requests)
		targetCluster = p.resolveControllerCluster(version, clientID)
		p.logger.Infof("Controller routing: %s:%s -> %s", clientID, version, targetCluster)
	} else if nodeID != "" && version != "" {
		// Control-plane routing (envoy node requests)
		if p.isEnvoyADSStream(path) {
			// For real Envoy ADS streams, create new mappings if needed
			targetCluster = p.resolveControlPlaneCluster(version, nodeID)
			p.logger.Infof("Control-plane routing: %s:%s -> %s", nodeID, version, targetCluster)
		} else {
			// For bridge calls, first check if node is already mapped to a control-plane
			ctx := context.Background()
			targetCluster = p.resolveBridgeControlPlaneCluster(ctx, version, nodeID, path)
			if targetCluster != "" {
				p.logger.Infof("Bridge routing: %s:%s -> %s", nodeID, version, targetCluster)
			} else {
				p.logger.Infof("Bridge routing: No control-plane found for %s:%s", nodeID, version)
			}
		}
	}

	// Only proceed with mutation if we found a target cluster
	if targetCluster != "" {
		p.logger.Infof("=== Adding Routing Header ===")
		p.logger.Infof("Original headers:")
		for _, h := range headers.Headers.Headers {
			if h != nil {
				p.logger.Infof("  %s: %s", h.Key, string(h.RawValue))
			}
		}

		// Create the response with only the new header
		response := &ext.ProcessingResponse{
			Response: &ext.ProcessingResponse_RequestHeaders{
				RequestHeaders: &ext.HeadersResponse{
					Response: &ext.CommonResponse{
						Status:          ext.CommonResponse_CONTINUE,
						ClearRouteCache: true,
						HeaderMutation: &ext.HeaderMutation{
							SetHeaders: []*core.HeaderValueOption{
								{
									Header: &core.HeaderValue{
										Key:      HeaderTargetCluster,
										RawValue: []byte(targetCluster), // Use RawValue instead of Value
									},
									// Don't append, just set
									Append: &wrapperspb.BoolValue{Value: false},
								},
							},
						},
					},
				},
			},
		}

		p.logger.Infof("=== Adding Routing Header ===")
		p.logger.Infof("Status: CONTINUE")
		p.logger.Infof("Adding Header: %s = %s (as RawValue)", HeaderTargetCluster, targetCluster)
		p.logger.Infof("Header Mutation: %+v", response.GetResponse().(*ext.ProcessingResponse_RequestHeaders).RequestHeaders.Response.HeaderMutation)
		p.logger.Infof("============================")

		// Send the response
		err := stream.Send(response)
		if err != nil {
			p.logger.Errorf("Failed to send response: %v", err)
			return err
		}

		p.logger.Infof("Response sent successfully")
		return nil
	}

	p.logger.Warnf("No routing decision found for gRPC request, continuing without routing")
	return stream.Send(p.createContinueResponse())
}

// createContinueResponse creates a simple continue response without modifications
func (p *ExternalProcessorServer) createContinueResponse() *ext.ProcessingResponse {
	return &ext.ProcessingResponse{
		Response: &ext.ProcessingResponse_RequestHeaders{
			RequestHeaders: &ext.HeadersResponse{
				Response: &ext.CommonResponse{
					Status: ext.CommonResponse_CONTINUE,
				},
			},
		},
	}
}

// handleRequestBody processes request body (if needed)
func (p *ExternalProcessorServer) handleRequestBody(stream ext.ExternalProcessor_ProcessServer, body *ext.HttpBody) error {
	p.logger.Debugf("Processing request body (length: %d)", len(body.Body))

	// For now, just continue without modification
	response := &ext.ProcessingResponse{
		Response: &ext.ProcessingResponse_RequestBody{
			RequestBody: &ext.BodyResponse{
				Response: &ext.CommonResponse{
					Status: ext.CommonResponse_CONTINUE,
				},
			},
		},
	}

	return stream.Send(response)
}

// handleRequestTrailers processes request trailers
func (p *ExternalProcessorServer) handleRequestTrailers(stream ext.ExternalProcessor_ProcessServer, _ *ext.HttpTrailers) error {
	p.logger.Debugf("Processing request trailers")

	response := &ext.ProcessingResponse{
		Response: &ext.ProcessingResponse_RequestTrailers{
			RequestTrailers: &ext.TrailersResponse{
				HeaderMutation: &ext.HeaderMutation{},
			},
		},
	}

	return stream.Send(response)
}

// handleResponseHeaders processes response headers
func (p *ExternalProcessorServer) handleResponseHeaders(stream ext.ExternalProcessor_ProcessServer, _ *ext.HttpHeaders) error {
	// Just continue without any processing since response_header_mode is SKIP
	return stream.Send(&ext.ProcessingResponse{
		Response: &ext.ProcessingResponse_ResponseHeaders{
			ResponseHeaders: &ext.HeadersResponse{
				Response: &ext.CommonResponse{
					Status: ext.CommonResponse_CONTINUE,
				},
			},
		},
	})
}

// handleResponseBody processes response body
func (p *ExternalProcessorServer) handleResponseBody(stream ext.ExternalProcessor_ProcessServer, body *ext.HttpBody) error {
	p.logger.Debugf("Processing response body (length: %d)", len(body.Body))

	response := &ext.ProcessingResponse{
		Response: &ext.ProcessingResponse_ResponseBody{
			ResponseBody: &ext.BodyResponse{
				Response: &ext.CommonResponse{
					Status: ext.CommonResponse_CONTINUE,
				},
			},
		},
	}

	return stream.Send(response)
}

// handleResponseTrailers processes response trailers
func (p *ExternalProcessorServer) handleResponseTrailers(stream ext.ExternalProcessor_ProcessServer, _ *ext.HttpTrailers) error {
	p.logger.Debugf("Processing response trailers")

	response := &ext.ProcessingResponse{
		Response: &ext.ProcessingResponse_ResponseTrailers{
			ResponseTrailers: &ext.TrailersResponse{
				HeaderMutation: &ext.HeaderMutation{},
			},
		},
	}

	return stream.Send(response)
}

// getHeaderFromMap extracts header value from Envoy HeaderMap
func (p *ExternalProcessorServer) getHeaderFromMap(headers *core.HeaderMap, key string) string {
	if headers == nil || headers.Headers == nil {
		return ""
	}

	// In Envoy ext_proc, headers come in raw_value field
	for _, header := range headers.Headers {
		if header != nil && strings.EqualFold(header.Key, key) {
			return string(header.RawValue)
		}
	}

	return ""
}

// ClearPendingClientAssignment clears a pending client assignment
func (p *ExternalProcessorServer) ClearPendingClientAssignment(clientID, controllerID string) {
	p.assignmentMutex.Lock()
	defer p.assignmentMutex.Unlock()

	if pendingController, exists := p.pendingClientAssignments[clientID]; exists && pendingController == controllerID {
		delete(p.pendingClientAssignments, clientID)
		p.logger.Infof("Cleared pending client assignment: %s -> %s", clientID, controllerID)
	}
}

// ClearPendingNodeAssignment clears a pending node assignment
func (p *ExternalProcessorServer) ClearPendingNodeAssignment(nodeID, controlPlaneID string) {
	p.assignmentMutex.Lock()
	defer p.assignmentMutex.Unlock()

	if pendingControlPlane, exists := p.pendingNodeAssignments[nodeID]; exists && pendingControlPlane == controlPlaneID {
		delete(p.pendingNodeAssignments, nodeID)
		p.logger.Infof("Cleared pending node assignment: %s -> %s", nodeID, controlPlaneID)
	}
}

// isEnvoyADSStream checks if the incoming request is from a real Envoy ADS stream
func (p *ExternalProcessorServer) isEnvoyADSStream(path string) bool {
	// Real Envoy ADS streams start with /envoy.
	if strings.HasPrefix(path, "/envoy.") {
		return true
	}

	// Bridge service paths should NOT create control-plane mappings
	bridgePaths := []string{
		"/bridge.ResourceService/ValidateResource",
		"/bridge.SnapshotService/GetSnapshotKeys",
		"/bridge.SnapshotService/GetSnapshotResources",
		"/bridge.PokeService/Poke",
	}

	for _, bridgePath := range bridgePaths {
		if path == bridgePath {
			return false
		}
	}

	// Default: treat unknown paths as non-ADS to be safe
	return false
}

// StartGRPCServer starts the gRPC server. leader and observer are optional —
// when both are nil the registry runs single-node (always SERVING, no guards).
// When supplied, the registry registers a grpc.health.v1 service whose status
// flips with leadership and rejects RPCs on standby instances.
//
// ctx drives graceful shutdown: when ctx is cancelled (typically by a SIGTERM
// handler in cmd/registry.go) the server calls grpc.Server.GracefulStop, which
// stops accepting new RPCs and waits for in-flight ones to finish. This is
// the only path that lets the election loop's `releaseIfHeld` actually run
// before the process exits — without it, SIGTERM terminates the Go runtime
// immediately, deferred releases never fire, and the Mongo lock doc stays
// behind for the full TTL (default 30s). A restarted single-node registry
// then sits as standby for ~30s rejecting RPCs with "not the active leader",
// which is what we observed in local restarts.
func StartGRPCServer(
	ctx context.Context,
	address string,
	controllerRoutingService *service.ControllerRoutingService,
	controlPlaneRoutingService *service.RoutingService,
	metricsAggregator *metrics.Aggregator,
	logger *logger.Logger,
	appConfig *config.AppConfig,
	leader LeaderChecker,
	observer LeadershipObserver,
) error {
	lis, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to listen on address %s: %w", address, err)
	}

	grpcServer := grpc.NewServer(
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    60 * time.Second,
			Timeout: 30 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second, // Client can ping at most every 20 seconds
			PermitWithoutStream: false,            // Don't allow pings without streams
		}),
	)

	// Create external processor server first
	extProcessorServer := NewExternalProcessorServer(controllerRoutingService, controlPlaneRoutingService, logger, leader)

	// Register controller routing service with access to external processor
	controllerGRPCServer := NewControllerGRPCServer(controllerRoutingService, extProcessorServer, logger, appConfig, leader)
	bridge.RegisterControllerRoutingServiceServer(grpcServer, controllerGRPCServer)

	// Register control-plane routing service with access to external processor
	controlPlaneGRPCServer := NewControlPlaneGRPCServer(controlPlaneRoutingService, extProcessorServer, logger, leader)
	bridge.RegisterEnvoyRoutingServiceServer(grpcServer, controlPlaneGRPCServer)

	// Register external processor service (for Envoy ext_proc)
	ext.RegisterExternalProcessorServer(grpcServer, extProcessorServer)

	// Register metrics service
	bridge.RegisterMetricsServiceServer(grpcServer, metricsAggregator)

	// gRPC health service (drives Envoy upstream cluster health-check).
	if observer != nil {
		hs := NewHealthServer(observer, logger)
		grpc_health_v1.RegisterHealthServer(grpcServer, hs)
	}

	// Wire ctx cancellation to a graceful shutdown of the gRPC server.
	// GracefulStop sends GOAWAY to existing connections, drains in-flight
	// RPCs and only then returns — letting deferred Mongo work (snapshot
	// save, lock release) finish before the binary exits.
	go func() {
		<-ctx.Done()
		logger.Infof("shutdown signal received, gracefully stopping gRPC server")
		grpcServer.GracefulStop()
	}()

	logger.Infof("gRPC server starting on address %s (Controller + Control-Plane + ExternalProcessor + Metrics + Health services)", address)

	if err := grpcServer.Serve(lis); err != nil {
		return fmt.Errorf("failed to serve gRPC: %w", err)
	}

	return nil
}
