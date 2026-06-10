package worker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"go.mongodb.org/mongo-driver/mongo"
)

// Pool implements a distributed worker pool
type Pool struct {
	config         *PoolConfig
	workers        []*Worker
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	logger         *logger.Logger
	jobManager     *job.Manager
	pokeService    *bridge.PokeServiceClient
	dbContext      *db.AppContext
	commandHandler ClientCommandHandler
	isRunning      bool
	runningMutex   sync.RWMutex
}

// PoolConfig holds configuration for the worker pool
type PoolConfig struct {
	MongoDB            *mongo.Database
	DBContext          *db.AppContext
	WorkerCount        int
	BatchSize          int
	PollInterval       time.Duration
	Logger             *logger.Logger
	MaxConcurrentPokes int
	CommandHandler     ClientCommandHandler
}

// WorkerConfig holds configuration passed to workers
type WorkerConfig struct {
	PokeService        *bridge.PokeServiceClient
	JobManager         *job.Manager
	DBContext          *db.AppContext
	WAFProcessor       WAFProcessor
	ShieldDeployer     ShieldDeployer
	MaxConcurrentPokes int
}

// WorkerStatus represents the status of the worker pool
type WorkerStatus struct {
	ActiveWorkers  int       `json:"active_workers"`
	ProcessingJobs int       `json:"processing_jobs"`
	QueueSize      int       `json:"queue_size"`
	LastActivity   time.Time `json:"last_activity"`
}

// NewPool creates a new worker pool
func NewPool(config *PoolConfig) *Pool {
	if config.Logger == nil {
		config.Logger = logger.NewLogger("worker-pool")
	}

	if config.MaxConcurrentPokes == 0 {
		config.MaxConcurrentPokes = 5
	}

	if config.PollInterval == 0 {
		config.PollInterval = 2 * time.Second
	}

	return &Pool{
		config:         config,
		logger:         config.Logger,
		jobManager:     job.NewManager(config.MongoDB, config.Logger),
		dbContext:      config.DBContext,
		commandHandler: config.CommandHandler,
		workers:        make([]*Worker, 0, config.WorkerCount),
	}
}

// Start starts the worker pool
func (p *Pool) Start(ctx context.Context, config *WorkerConfig) error {
	p.runningMutex.Lock()
	defer p.runningMutex.Unlock()

	if p.isRunning {
		return fmt.Errorf("worker pool is already running")
	}

	// Store config
	p.pokeService = config.PokeService
	if config.JobManager != nil {
		p.jobManager = config.JobManager
	}
	if config.DBContext != nil {
		p.dbContext = config.DBContext
	}

	// Create context for pool
	p.ctx, p.cancel = context.WithCancel(ctx)

	// Create and start workers
	for i := 0; i < p.config.WorkerCount; i++ {
		workerID := fmt.Sprintf("worker-%d-%d", time.Now().Unix(), i)
		worker := NewWorker(workerID, p.config, p.jobManager, p.pokeService, p.dbContext, config.WAFProcessor, config.ShieldDeployer, p.commandHandler)
		p.workers = append(p.workers, worker)

		p.wg.Add(1)
		go func(w *Worker) {
			defer p.wg.Done()
			w.Run(p.ctx)
		}(worker)
	}

	p.isRunning = true
	p.logger.Infof("Started worker pool with %d workers", p.config.WorkerCount)

	return nil
}

// Stop stops the worker pool
func (p *Pool) Stop(ctx context.Context) error {
	p.runningMutex.Lock()
	defer p.runningMutex.Unlock()

	if !p.isRunning {
		return fmt.Errorf("worker pool is not running")
	}

	// Cancel context to signal workers to stop
	p.cancel()

	// Wait for all workers to finish with timeout
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		p.logger.Info("All workers stopped gracefully")
	case <-time.After(30 * time.Second):
		p.logger.Warn("Worker pool stop timeout, some workers may not have stopped cleanly")
	}

	p.isRunning = false
	p.workers = nil

	return nil
}

// GetStatus returns the current status of the worker pool
func (p *Pool) GetStatus(ctx context.Context) (*WorkerStatus, error) {
	p.runningMutex.RLock()
	defer p.runningMutex.RUnlock()

	if !p.isRunning {
		return &WorkerStatus{
			ActiveWorkers:  0,
			ProcessingJobs: 0,
			QueueSize:      0,
			LastActivity:   time.Time{},
		}, nil
	}

	processingCount := 0
	lastActivity := time.Time{}

	for _, worker := range p.workers {
		if worker.IsProcessing() {
			processingCount++
		}
		if workerLastActivity := worker.GetLastActivity(); workerLastActivity.After(lastActivity) {
			lastActivity = workerLastActivity
		}
	}

	// Get queue size from MongoDB
	filter := job.JobFilter{
		Status: []job.JobStatus{job.JobStatusPending},
	}
	pendingJobs, err := p.jobManager.ListJobs(ctx, &filter)
	if err != nil {
		return nil, err
	}

	return &WorkerStatus{
		ActiveWorkers:  len(p.workers),
		ProcessingJobs: processingCount,
		QueueSize:      len(pendingJobs),
		LastActivity:   lastActivity,
	}, nil
}
