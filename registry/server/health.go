package server

import (
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	healthsvc "google.golang.org/grpc/health"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

// LeaderChecker is the small interface ExternalProcessorServer / GRPC handlers
// rely on to decide whether to serve traffic. Implemented by registry/leader.Election.
type LeaderChecker interface {
	IsLeader() bool
}

// LeadershipObserver is fired by registry/leader.Election to flip health status.
// Defined here so the server package depends only on the leader interface, not
// on its concrete implementation (avoids import cycle).
type LeadershipObserver interface {
	OnLeadershipChange(fn func(isLeader bool))
}

// NewHealthServer builds a standard grpc.health.v1 server that flips between
// SERVING (leader) and NOT_SERVING (standby) based on observer callbacks.
// The empty service name "" applies to the whole server, which is what
// Envoy's `grpc_health_check: {}` config queries.
func NewHealthServer(observer LeadershipObserver, log *logger.Logger) *healthsvc.Server {
	hs := healthsvc.NewServer()
	// Start unhealthy until the leader loop confirms our role. This avoids a
	// race where Envoy briefly considers us SERVING during boot.
	hs.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	observer.OnLeadershipChange(func(isLeader bool) {
		if isLeader {
			log.Infof("registry health: -> SERVING")
			hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
		} else {
			log.Infof("registry health: -> NOT_SERVING")
			hs.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		}
	})
	return hs
}
