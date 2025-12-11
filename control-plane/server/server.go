package server

import (
	"context"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/reflection"

	discoverygrpc "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/service/discovery/v3"
	routeservice "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/service/route/v3"
	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/server/v3"

	"github.com/CloudNativeWorks/elchi-backend/control-plane/envoys"
	serverBridge "github.com/CloudNativeWorks/elchi-backend/control-plane/server/bridge"
	"github.com/CloudNativeWorks/elchi-backend/control-plane/server/snapshot"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/registry"
)

const (
	grpcKeepaliveTime        = 30 * time.Second
	grpcKeepaliveTimeout     = 10 * time.Second
	grpcKeepaliveMinTime     = 1 * time.Second
	grpcMaxConcurrentStreams = 10000
	grpcMaxRecvMsgSize       = 1024 * 1024 * 50 // 50MB
	grpcMaxSendMsgSize       = 1024 * 1024 * 50 // 50MB
)

type Server struct {
	xdsServer        server.Server
	port             uint
	logger           *logger.Logger
	context          *snapshot.Context
	healthServer     *health.Server
	routingManager   *registry.ControlPlaneManager
	envoyConnTracker *envoys.EnvoyConnTracker
}

func NewServer(xdsServer server.Server, port uint, context *snapshot.Context, envoyConnTracker *envoys.EnvoyConnTracker) *Server {
	logger := logger.NewLogger("control-plane/server")

	return &Server{
		xdsServer:        xdsServer,
		port:             port,
		logger:           logger,
		context:          context,
		healthServer:     health.NewServer(),
		routingManager:   nil, // Will be set from cmd if provided
		envoyConnTracker: envoyConnTracker,
	}
}

// Run starts an xDS server at the given port.
func (s *Server) Run(db *db.AppContext) {
	// Note: Routing manager is now handled in cmd/control-plane.go

	var grpcOptions []grpc.ServerOption
	grpcOptions = append(grpcOptions,
		grpc.MaxConcurrentStreams(grpcMaxConcurrentStreams),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:                  grpcKeepaliveTime,
			Timeout:               grpcKeepaliveTimeout,
			MaxConnectionAge:      0, // Infinite - ADS connections should never expire
			MaxConnectionAgeGrace: 0, // Infinite - graceful period
			MaxConnectionIdle:     0, // Infinite - never close idle connections
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             grpcKeepaliveMinTime,
			PermitWithoutStream: true, // Allow pings without active streams
		}),
		grpc.MaxRecvMsgSize(grpcMaxRecvMsgSize),
		grpc.MaxSendMsgSize(grpcMaxSendMsgSize),

		grpc.WriteBufferSize(32*1024),         // 32KB write buffer
		grpc.ReadBufferSize(32*1024),          // 32KB read buffer
		grpc.InitialWindowSize(1024*1024),     // 1MB initial window
		grpc.InitialConnWindowSize(1024*1024), // 1MB initial connection window

		// Debugging interceptors for preface failure analysis
		grpc.UnaryInterceptor(s.logInterceptor),
		grpc.StreamInterceptor(s.logStreamInterceptor),
	)
	grpcServer := grpc.NewServer(grpcOptions...)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		s.logger.Fatal(err)
	}

	s.registerServer(grpcServer, db)

	reflection.Register(grpcServer)
	s.logger.Infof("Management server listening on :%d with enhanced debugging", s.port)
	s.logger.Info("ADS server ready - interceptors active for connection analysis")
	if err = grpcServer.Serve(lis); err != nil {
		s.logger.Fatal(err)
	}
}

func (s *Server) registerServer(grpcServer *grpc.Server, db *db.AppContext) {
	// envoy ads & vhds services
	discoverygrpc.RegisterAggregatedDiscoveryServiceServer(grpcServer, s.xdsServer)
	routeservice.RegisterVirtualHostDiscoveryServiceServer(grpcServer, s.xdsServer)

	// bridge grpc services
	bridge.RegisterSnapshotServiceServer(grpcServer, serverBridge.NewSnapshotServiceServer(s.context))
	bridge.RegisterResourceServiceServer(grpcServer, serverBridge.NewResourceServiceServer(s.context))
	bridge.RegisterPokeServiceServer(grpcServer, serverBridge.NewPokeServiceServer(s.context, db, s.envoyConnTracker))

	// health check
	grpc_health_v1.RegisterHealthServer(grpcServer, s.healthServer)
	s.healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	s.logger.Info("Health check server registered and serving status set to SERVING")
}

// logInterceptor logs unary RPC calls for debugging
func (s *Server) logInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	start := time.Now()

	// Get peer info
	peerInfo := "unknown"
	if p, ok := peer.FromContext(ctx); ok {
		peerInfo = p.Addr.String()
	}

	s.logger.Infof("GRPC Unary [%s] from %s started", info.FullMethod, peerInfo)

	resp, err := handler(ctx, req)

	duration := time.Since(start)
	if err != nil {
		s.logger.Errorf("GRPC Unary [%s] from %s failed after %v: %v", info.FullMethod, peerInfo, duration, err)
	} else {
		s.logger.Infof("GRPC Unary [%s] from %s completed in %v", info.FullMethod, peerInfo, duration)
	}

	return resp, err
}

// logStreamInterceptor logs streaming RPC calls for debugging
func (s *Server) logStreamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	start := time.Now()

	// Get peer info
	peerInfo := "unknown"
	if p, ok := peer.FromContext(ss.Context()); ok {
		peerInfo = p.Addr.String()
	}

	s.logger.Infof("GRPC Stream [%s] from %s started", info.FullMethod, peerInfo)

	err := handler(srv, ss)

	duration := time.Since(start)
	if err != nil {
		s.logger.Errorf("GRPC Stream [%s] from %s failed after %v: %v", info.FullMethod, peerInfo, duration, err)
	} else {
		s.logger.Infof("GRPC Stream [%s] from %s completed in %v", info.FullMethod, peerInfo, duration)
	}

	return err
}
