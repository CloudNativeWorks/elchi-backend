package server

import (
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	discoverygrpc "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/service/discovery/v3"
	routeservice "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/service/route/v3"
	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/server/v3"

	serverBridge "github.com/CloudNativeWorks/elchi-backend/control-plane/server/bridge"
	"github.com/CloudNativeWorks/elchi-backend/control-plane/server/snapshot"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

const (
	grpcKeepaliveTime        = 30 * time.Second
	grpcKeepaliveTimeout     = 5 * time.Second
	grpcKeepaliveMinTime     = 15 * time.Second
	grpcMaxConcurrentStreams = 10000
	grpcMaxRecvMsgSize       = 1024 * 1024 * 50 // 50MB
	grpcMaxSendMsgSize       = 1024 * 1024 * 50 // 50MB
)

type Server struct {
	xdsServer     server.Server
	port          uint
	logger        *logger.Logger
	context       *snapshot.Context
	healthServer  *health.Server
}

func NewServer(xdsServer server.Server, port uint, context *snapshot.Context) *Server {
	return &Server{
		xdsServer:     xdsServer,
		port:          port,
		logger:        logger.NewLogger("control-plane/server"),
		context:       context,
		healthServer:  health.NewServer(),
	}
}

// Run starts an xDS server at the given port.
func (s *Server) Run(db *db.AppContext) {
	var grpcOptions []grpc.ServerOption
	grpcOptions = append(grpcOptions,
		grpc.MaxConcurrentStreams(grpcMaxConcurrentStreams),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    grpcKeepaliveTime,
			Timeout: grpcKeepaliveTimeout,
		}),
		grpc.MaxRecvMsgSize(grpcMaxRecvMsgSize),
		grpc.MaxSendMsgSize(grpcMaxSendMsgSize),
	)
	grpcServer := grpc.NewServer(grpcOptions...)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		s.logger.Fatal(err)
	}

	s.registerServer(grpcServer, db)

	reflection.Register(grpcServer)
	s.logger.Infof("Management server listening on :%d\n", s.port)
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
	bridge.RegisterPokeServiceServer(grpcServer, serverBridge.NewPokeServiceServer(s.context, db))

	// health check
	grpc_health_v1.RegisterHealthServer(grpcServer, s.healthServer)
	s.healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	s.logger.Info("Health check server registered and serving status set to SERVING")
}
