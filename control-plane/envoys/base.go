package envoys

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"go.mongodb.org/mongo-driver/mongo"
)

type dbOperation struct {
	nodeID      string
	count       int
	op          string
	dbClient    *mongo.Database
	address     string
	version     string
	downAddress string
	clientName  string
	logger      *logger.Logger
	isUndeploy  bool
}

type EnvoyConnTracker struct {
	mu       sync.RWMutex
	Counter  map[string]int
	dbOpChan chan dbOperation
}

func NewEnvoyConnTracker() *EnvoyConnTracker {
	tracker := &EnvoyConnTracker{
		Counter:  make(map[string]int),
		dbOpChan: make(chan dbOperation, 1000),
	}

	go tracker.processDBOperations()
	return tracker
}

func (e *EnvoyConnTracker) processDBOperations() {
	for op := range e.dbOpChan {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		switch op.op {
		case "inc":
			e.AddOrUpdateEnvoy(ctx, op.dbClient, op.address, op.nodeID, op.version, op.downAddress, op.clientName, op.count, op.logger)
		case "dec":
			e.DisconnectNodeIDWithCount(ctx, op.dbClient, op.nodeID, op.count, op.isUndeploy, op.logger)
		}
		cancel()
	}
}

// IncAndGet atomically increments the counter and returns the new value
func (e *EnvoyConnTracker) IncAndGet(nodeID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Counter[nodeID]++
	return e.Counter[nodeID]
}

// DecAndGet atomically decrements the counter and returns the new value
func (e *EnvoyConnTracker) DecAndGet(nodeID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Counter[nodeID] > 0 {
		e.Counter[nodeID]--
	}
	return e.Counter[nodeID]
}

func (e *EnvoyConnTracker) Count(nodeID string) int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.Counter[nodeID]
}

func (e *EnvoyConnTracker) TrackUndeploy(dbClient *mongo.Database, nodeID string, logger *logger.Logger) {
	fmt.Printf("🔍 DEBUG: TrackUndeploy called for nodeID: %s\n", nodeID)
	
	// Counter'ı güncelle (undeploy durumunda 0 yap)
	e.mu.Lock()
	e.Counter[nodeID] = 0
	e.mu.Unlock()
	
	fmt.Printf("🔍 DEBUG: TrackUndeploy - counter set to 0 for nodeID: %s\n", nodeID)
	
	// Synchronous undeploy işlemi (async değil!)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	
	fmt.Printf("🔍 DEBUG: TrackUndeploy - calling DisconnectNodeIDWithCount with isUndeploy=true\n")
	
	e.DisconnectNodeIDWithCount(ctx, dbClient, nodeID, 0, true, logger)
	
	fmt.Printf("🔍 DEBUG: TrackUndeploy - DisconnectNodeIDWithCount completed for nodeID: %s\n", nodeID)
	
	logger.Infof("Undeploy completed synchronously for NodeID %s", nodeID)
}
