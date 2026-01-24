// Package envoys provides Envoy instance management for the control-plane
// including tracking, error handling, heartbeat, and synchronization.
package envoys

import (
	"context"
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
	clientID    string
	logger      *logger.Logger
	isUndeploy  bool
}

type EnvoyConnTracker struct {
	mu       sync.RWMutex
	Counter  map[string]int
	dbOpChan chan dbOperation
	stopChan chan struct{}
	wg       sync.WaitGroup
}

func NewEnvoyConnTracker() *EnvoyConnTracker {
	tracker := &EnvoyConnTracker{
		Counter:  make(map[string]int),
		dbOpChan: make(chan dbOperation, 1000),
		stopChan: make(chan struct{}),
	}

	tracker.wg.Add(1)
	go tracker.processDBOperations()
	return tracker
}

// Shutdown gracefully stops the tracker and waits for pending operations
func (e *EnvoyConnTracker) Shutdown() {
	close(e.stopChan)
	close(e.dbOpChan)
	e.wg.Wait()
}

func (e *EnvoyConnTracker) processDBOperations() {
	defer e.wg.Done()
	for op := range e.dbOpChan {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		switch op.op {
		case "inc":
			e.AddOrUpdateEnvoy(ctx, op.dbClient, op.address, op.nodeID, op.version, op.downAddress, op.clientName, op.clientID, op.count, op.logger)
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
	e.mu.Lock()
	e.Counter[nodeID] = 0
	e.mu.Unlock()

	// Synchronous undeploy (not async!)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	e.DisconnectNodeIDWithCount(ctx, dbClient, nodeID, 0, true, logger)

	logger.Infof("Undeploy completed synchronously for NodeID %s", nodeID)
}
