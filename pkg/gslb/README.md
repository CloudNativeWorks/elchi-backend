# GSLB (Global Server Load Balancing) System

Distributed, scalable health checking system for DNS-based load balancing with intelligent failover detection and multi-controller coordination.

---

## 📋 Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Key Features](#key-features)
- [Components](#components)
- [Data Model](#data-model)
- [Health State Model](#health-state-model)
- [Probe Scheduling](#probe-scheduling)
- [Performance Characteristics](#performance-characteristics)
- [Configuration](#configuration)
- [Monitoring & Metrics](#monitoring--metrics)
- [API Usage](#api-usage)
- [Troubleshooting](#troubleshooting)

---

## Overview

The GSLB system provides automated health checking for DNS records with intelligent failover detection. It supports horizontal scaling across multiple controllers, each handling a subset of health checks through distributed sharding.

### Design Goals

- **High Scale**: 100K+ IPs across distributed controllers
- **Fast Failover**: Immediate re-probe on state transitions (event-driven)
- **Horizontal Scaling**: Multiple controllers share work via distributed sharding
- **Zero Data Loss**: Batched writes with graceful shutdown
- **Resource Efficiency**: CPU-aware worker pools, batch queries, connection reuse

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         GSLB System                              │
│  ┌────────────┐   ┌──────────────┐   ┌────────────────────┐   │
│  │   Shard    │───│    Bucket    │───│  Health Checker    │   │
│  │  Manager   │   │  Scheduler   │   │  (Tri-State FSM)   │   │
│  └────────────┘   └──────────────┘   └────────────────────┘   │
│        │                  │                      │              │
│        │                  ▼                      ▼              │
│        │         ┌─────────────────┐   ┌─────────────────┐    │
│        │         │  Timer Buckets  │   │ Write Buffer    │    │
│        │         │  (10s-300s)     │   │ (Batched I/O)   │    │
│        │         │                 │   └─────────────────┘    │
│        │         └─────────────────┘             │              │
│        │                  │                      │              │
│        ▼                  ▼                      ▼              │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │              MongoDB (gslb_* collections)                 │  │
│  │  - gslb_records: DNS records with probe configs           │  │
│  │  - gslb_ip_health: IP health states (128 shards × 8 sub)  │  │
│  │  - gslb_shards: Distributed ownership leases              │  │
│  └──────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘

         Controller 1         Controller 2         Controller 3
              │                     │                     │
              └─────────────────────┴─────────────────────┘
                    Distributed Shard Ownership
                    (128 shards × 8 sub-shards = 1024 partitions)
```

### Component Interaction Flow

```
1. Shard Manager acquires shards (128 top-level, each with 8 sub-shards)
2. Bucket Scheduler loads GSLB records for owned shards
3. Timer Buckets execute probe cycles at their interval (10s, 20s, ..., 300s)
4. Probe Executor performs HTTP/TCP health checks
5. Health Checker processes results → state transitions
6. Write Buffer batches updates → MongoDB flush (every 1s or 1000 updates)
7. State Transition Event: PASSING → WARNING triggers immediate re-probe (event-driven)
```

---

## Key Features

### 🚀 Horizontal Scaling
- **Distributed Sharding**: 128 shards × 8 sub-shards = 1024 partitions
- **Leader Election**: MongoDB-based lease system (30s TTL, renewed every 25s)
- **Auto-Rebalancing**: Detects controller failures and redistributes work
- **No Single Point of Failure**: Any controller can handle any shard

### ⚡ Immediate Re-Probe (Event-Driven Failover)
- **Instant Detection**: WARNING IPs re-probed immediately (~100ms after transition)
- **Failover Speed**: One additional probe, then CRITICAL or PASSING (no periodic queries)
- **Event-Driven**: Triggered by PASSING → WARNING state transition
- **Exact Threshold Control**: warning_threshold=1, critical_threshold=2 → exactly 1 re-probe

### 🎯 Tri-State Health Model
- **PASSING** → **WARNING** → **CRITICAL** progression
- **Configurable Thresholds**: `warning_threshold` (default: 1), `critical_threshold` (default: 2)
- **Circuit Breaker**: Exponential backoff for CRITICAL IPs (30s → 60s → 120s, max 5 min)
- **Manual Override**: Admin can reset health state via API with counter reset

### 📊 Performance Optimizations
- **Batch N+1 Fix**: 5000 queries → 1 aggregation query per bucket cycle
- **Write Batching**: 1000 updates/sec → 1 MongoDB write/sec (1000x reduction)
- **Complete Isolation**: Each record+IP maintains independent state (no deduplication, no fan-out)
- **CPU-Aware Workers**: Auto-scaling pools (20-500 workers/bucket based on CPU cores)

### 🔧 Reliability Features
- **Graceful Shutdown**: Flushes write buffer before stopping
- **Defensive Programming**: Validates task data (nil checks, empty IP checks)
- **Backoff Retry**: Exponential backoff with jitter for failed probes
- **Counter Persistence**: Infers failure count from health state on restart

---

## Components

### 1. **System** (`system.go`)
Main orchestrator and entry point for the entire GSLB subsystem.

**Responsibilities:**
- Initializes all components in correct order
- Coordinates startup/shutdown lifecycle
- Owns dependencies (DB, logger, metrics pusher)

**Key Methods:**
- `NewSystem()`: Creates system with all dependencies
- `Start(ctx)`: Starts shard manager → health checker → scheduler
- `Stop()`: Gracefully stops all components (flushes write buffer)

---

### 2. **ShardManager** (`shard_manager.go`)
Distributed shard ownership with leader election and rebalancing.

**Sharding Strategy:**
- **128 top-level shards** (based on FQDN hash)
- **8 sub-shards per shard** = 1024 total partitions
- **Lease-Based Ownership**: 30s TTL, renewed every 25s
- **Collision Detection**: Retry with exponential backoff if conflict

**Shard Calculation:**
```go
shardID := fnv.New32a(fqdn) % 128
subShardID := fnv.New32a(fqdn) % 8
```

**Repository Pattern:**
- `ShardRepository` (`shard_repository.go`): MongoDB CRUD operations
- `ShardManager`: Business logic, lease renewal, rebalancing

**Key Methods:**
- `AcquireShards(ctx)`: Acquires available shards with exponential backoff retry
- `RenewLeases(ctx)`: Renews owned shards every 25 seconds
- `DetectStaleShardsAndRebalance()`: Finds expired leases and triggers rebalancing
- `GetOwnedShards()`: Returns current shard ownership list

---

### 3. **BucketScheduler** (`bucket_scheduler.go`)
Interval-based probe scheduling with dedicated worker pools per bucket.

**Bucket Architecture:**
- **Pre-defined Intervals**: 10s, 20s, 30s, 60s, 90s, 120s, 180s, 300s
- **One Bucket Per Interval**: Each has its own timer and worker pool
- **No FastFail Bucket**: Replaced with immediate re-probe on state transition (event-driven)

**Worker Pool Sizing (CPU-Aware):**
```
CPU Cores    10s Bucket     60s Bucket     300s Bucket
--------     ----------     ----------     -----------
   4         20-200         10-100         5-50
   8         40-400         20-200         10-100
  16         80-800         40-400         20-200
```

**Key Methods:**
- `AddRecord(record)`: Assigns record to appropriate bucket based on probe interval
- `RemoveRecord(fqdn)`: Removes from bucket (e.g., record deleted)
- `GetStats()`: Returns bucket statistics (workers, queue depth, probes/sec)

---

### 4. **TimerBucket** (`timer_bucket.go`)
Executes periodic probe cycles at a specific interval (10s, 20s, ..., 300s).

**Bucket Cycle:**
1. Load records from in-memory cache (assigned by BucketScheduler)
2. Batch query all IPs for all records (N+1 fix)
3. Create separate probe task for EACH record+IP combination (complete isolation)
4. Submit probe tasks to worker pool
5. Push cycle metrics to Registry

**Immediate Re-Probe (Event-Driven):**
- Triggered in `health_checker.go::executeImmediateReProbe()`
- Fires on PASSING → WARNING state transition
- Executes ONE additional probe after 100ms delay
- Result: Either WARNING → CRITICAL or WARNING → PASSING

**Complete Record Isolation:**
```
Each record+IP combination maintains independent:
- Health state (PASSING/WARNING/CRITICAL)
- Consecutive failure counter
- Backoff state (backoff_until, current_backoff)

Example:
  Record A (abc.com): IP=1.2.3.4 → CRITICAL + backoff 300s
  Record B (xyz.com): IP=1.2.3.4 → PASSING + no backoff

Result: Record A skipped (in backoff), Record B probed normally
NO fan-out, NO shared state between records
```

**Key Methods:**
- `runBucketCycle()`: Bucket cycle (probes cached records)
- `SetOwnedShards(shards)`: Updates shard ownership (triggers reload)
- `TriggerRebalance()`: Marks bucket for record reload on next cycle

---

### 5. **BucketWorkerPool** (`bucket_worker_pool.go`)
Dedicated worker pool for a specific bucket with CPU-aware auto-scaling.

**Auto-Scaling Algorithm:**
- **Scale-Up Trigger**: Queue depth > 70% → Add 20% workers (min 10)
- **Scale-Down Trigger**: Queue depth < 20% → Remove 10% workers (min 5)
- **Emergency Scale**: Queue full for 30s → Add 50% workers immediately
- **Rate Limiting**: Minimum 10s between scale actions

**Worker Lifecycle:**
```
1. Worker goroutine pulls task from probe queue
2. Validates task (nil checks, empty IP checks)
3. Executes probe with timeout context
4. Attaches task context (contains single RecordID)
5. Sends result to shared result queue (non-blocking)
6. Repeats until shutdown signal
```

**Key Methods:**
- `Submit(task)`: Non-blocking task submission (returns false if queue full)
- `spawnWorkers(count)`: Creates N new worker goroutines
- `killWorkers(count)`: Signals N workers to stop
- `GetStats()`: Returns pool statistics (workers, queue depth, scale count)

---

### 6. **HealthChecker** (`health_checker.go`)
Processes probe results and manages tri-state health transitions.

**State Transition Logic:**
```
PASSING (healthy, no failures)
   │
   │ failures >= warning_threshold (default: 1)
   ▼
WARNING (degraded, immediate re-probe triggered)
   │                              │
   │ failures >= critical_threshold (default: 2)
   │                              │ success → PASSING
   ▼                              ▼
CRITICAL (failed, circuit breaker activates)
   │
   │ backoff period (30s → 60s → 120s, max 5 min)
   ▼
[Next probe attempt after backoff expires]
```

**Manual Reset Handling:**
- Admin sets IP state via API → `manual_reset_at` timestamp set
- Counter manager detects manual reset (within 60s) → Resets counter to 0
- Health checker clears `manual_reset_at` field after first detection
- Next probe establishes new baseline

**Key Methods:**
- `processProbeResult(result)`: Main result handler
- `updateHealthState(...)`: Executes state transition logic
- `Start()`: Spawns result processor goroutine
- `Stop()`: Drains result queue and stops processor

---

### 7. **CounterManager** (`counter_manager.go`)
In-memory failure/success counters with persistence inference and cleanup.

**Counter Structure:**
```go
type IPHealthCounter struct {
    ConsecutiveFailures  int       // Incremented on probe failure
    ConsecutiveSuccesses int       // Incremented on probe success
    LastAccessed         time.Time // For stale counter cleanup
}
```

**Persistence Inference:**
- On controller restart, counter doesn't exist in memory
- Infers failure count from persisted health state:
  - PASSING → 0 failures
  - WARNING → `warning_threshold` failures (default: 1)
  - CRITICAL → `critical_threshold` failures (default: 2)

**Stale Counter Cleanup:**
- Runs every 1 hour
- Removes counters not accessed in 2 hours
- Prevents memory leak for deleted IPs

**Key Methods:**
- `GetOrInitialize(...)`: Gets counter or creates new one (with inference)
- `Update(recordID, ip, success)`: Updates counter based on probe result
- `Reset(recordID, ip)`: Resets counter to 0 (manual override)

---

### 8. **WriteBuffer** (`write_buffer.go`)
Batched write system for high-throughput MongoDB updates.

**Batching Strategy:**
- **Flush Triggers**: Every 1 second OR 1000 buffered updates (whichever comes first)
- **Deduplication**: Only latest update per IP is kept (map key = RecordID + IP)
- **Atomic Operations**: Uses `$set` and `$unset` for field updates

**Write Performance:**
```
Without Batching:  1000 updates/sec = 1000 MongoDB writes/sec
With Batching:     1000 updates/sec = 1 MongoDB write/sec (1000x reduction)
```

**Update Types:**
1. **Normal Health Update**: Sets health_state, backoff, timestamps, history
2. **Manual Reset Clear**: Unsets `manual_reset_at` field only

**Key Methods:**
- `Add(update)`: Buffers health state update (non-blocking)
- `Flush()`: Writes all buffered updates to MongoDB
- `Stop()`: Final flush before shutdown (ensures zero data loss)

---

### 9. **IPHealthManager** (`ip_health_manager.go`)
MongoDB repository for IP health records with batch query optimization.

**Key Queries:**

**1. Batch IP Query (N+1 Fix):**
```go
// Before: 5000 records × 1 query each = 5000 queries/cycle
for _, record := range records {
    ips, _ := GetIPsByRecordID(record.ID) // N+1 problem
}

// After: 1 aggregation query for all records
ipsByRecord, _ := GetIPsByRecordIDs(recordIDs) // Single query
```

**Performance:**
- Query timeout: 30 seconds (MongoDB Atlas)
- Indexes: `{shard_id, sub_shard_id, record_id}` compound index
- Batch size: Unlimited (aggregation cursor)

**Key Methods:**
- `GetIPsByRecordIDs(recordIDs)`: Batch query for normal bucket cycles
- `CreateIPHealth(ip)`: Creates new IP health record with shard assignment
- `UpdateHealthState(...)`: Direct MongoDB update (used for manual override)

**Note:** `GetWarningIPs()` method deprecated - immediate re-probe replaced FastFail bucket

---

### 10. **ProbeExecutor** (`probe_executor.go`)
HTTP/TCP health probe execution with configurable timeouts and retry logic.

**HTTP Probe:**
```go
// Request configuration
req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
req.Header.Set("User-Agent", "Elchi-GSLB-Health-Checker/1.0")

// Execute with timeout
resp, err := client.Do(req)

// Validate status code
success := resp.StatusCode >= 200 && resp.StatusCode < 300
```

**TCP Probe:**
```go
// Connection test with timeout
conn, err := net.DialTimeout("tcp", address, timeout)
conn.Close()
success := err == nil
```

**Retry Logic:**
- Automatic retry on network errors (DNS failure, connection timeout)
- No retry on successful probe or explicit failure (4xx, 5xx)
- Uses exponential backoff with jitter (`retry_helper.go`)

**Key Methods:**
- `ExecuteProbe(ctx, ipHealth, probe)`: Main probe execution
- `executeHTTPProbe(...)`: HTTP-specific logic
- `executeTCPProbe(...)`: TCP-specific logic

---

### 11. **CPUConfig** (`cpu_config.go`)
CPU-aware worker pool configuration.

**Worker Limits by Bucket Interval:**
```go
type WorkerLimits struct {
    MinWorkers int // CPU cores × 5
    MaxWorkers int // CPU cores × 100 (for 10s bucket)
}

// Examples (16 CPU cores):
// 10s bucket:  min=80,  max=800  (high frequency)
// 60s bucket:  min=40,  max=400  (medium frequency)
// 300s bucket: min=20,  max=200  (low frequency)
```

**Rationale:**
- Shorter intervals = more concurrent probes needed
- CPU cores determine parallelism capacity
- Auto-scaling handles burst traffic

---

## Data Model

### Collections

#### 1. `gslb_records`
DNS records with probe configuration.

```json
{
  "_id": ObjectId("..."),
  "fqdn": "api.example.com.",
  "project": "proj-123",
  "probe": {
    "enabled": true,
    "interval": 30,
    "timeout": 5,
    "protocol": "http",
    "path": "/health",
    "port": 443,
    "expected_status": [200, 204],
    "warning_threshold": 1,
    "critical_threshold": 2
  },
  "created_at": ISODate("..."),
  "updated_at": ISODate("...")
}
```

#### 2. `gslb_ip_health`
IP health state records (sharded).

```json
{
  "_id": ObjectId("..."),
  "record_id": ObjectId("..."),
  "ip": "1.2.3.4",
  "fqdn": "api.example.com.",
  "health_state": "passing",  // "passing" | "warning" | "critical"
  "last_status_change": ISODate("..."),
  "backoff_until": ISODate("..."),
  "current_backoff": 60,
  "manual_reset_at": ISODate("..."),  // Set by admin, cleared after detection
  "shard_id": 42,
  "sub_shard_id": 3,
  "status_history": [
    {
      "state": "passing",
      "timestamp": ISODate("..."),
      "reason": "Probe succeeded"
    }
  ],
  "created_at": ISODate("..."),
  "updated_at": ISODate("...")
}
```

**Indexes:**
```javascript
// Shard-based query index
{ shard_id: 1, sub_shard_id: 1, health_state: 1 }

// Record lookup index
{ record_id: 1 }

// FQDN search index
{ fqdn: 1 }
```

#### 3. `gslb_shards`
Distributed shard ownership leases.

```json
{
  "_id": ObjectId("..."),
  "shard_id": 42,
  "sub_shard_id": 3,
  "controller_id": "controller-1",
  "lease_expires_at": ISODate("..."),  // TTL: 30 seconds
  "updated_at": ISODate("...")
}
```

**Indexes:**
```javascript
// Shard lookup index
{ shard_id: 1, sub_shard_id: 1 }

// Lease expiration index (for rebalancing)
{ lease_expires_at: 1 }

// Controller ownership index
{ controller_id: 1 }
```

---

## Health State Model

### State Diagram

```
                    ┌──────────────┐
         ┌─────────►│   PASSING    │◄─────────┐
         │          └──────────────┘          │
         │                 │                   │
         │                 │ failures ≥        │
         │                 │ warning_threshold │
         │                 ▼                   │
         │          ┌──────────────┐          │ successes ≥ 1
         │          │   WARNING    │          │ (recovery)
         │          └──────────────┘──────────┘
         │                 │
         │                 │ failures ≥
         │                 │ critical_threshold
         │                 ▼
         │          ┌──────────────┐
         │          │   CRITICAL   │
         │          └──────────────┘
         │                 │
         │                 │ backoff expires
         │                 ▼
         │          [Next Probe Attempt]
         │                 │
         │                 │ success
         └─────────────────┘
```

### Thresholds (Configurable)

```go
probe := &models.GSLBProbe{
    WarningThreshold:  1,  // 1 failure  → WARNING
    CriticalThreshold: 2,  // 2 failures → CRITICAL
}
```

### Circuit Breaker (Adaptive Backoff Strategy)

**Purpose**: Stop probing persistently failing endpoints to save resources while allowing fast recovery detection.

**Strategy**: Interval-aware graduated backoff
- **Warning State**: NO backoff → probe at normal interval for fast recovery detection
- **Critical State**: Adaptive graduated backoff based on probe interval with 5-minute cap

**Adaptive Backoff Multipliers**: `[1.0, 2.0, 3.0, 5.0, 8.0, 12.0]`

Backoff scales with probe interval to ensure bucket-aligned probing:

```
10s interval: 10s → 20s → 30s → 50s → 80s → 120s (max)
30s interval: 30s → 60s → 90s → 150s → 240s → 300s (capped)
60s interval: 60s → 120s → 180s → 300s (capped)
```

**Backoff Calculation:**
```go
backoffSeconds := min(probeInterval * multipliers[failuresSinceCritical], 300)
```

**Rationale**:
- **Interval-Aware**: Fast intervals (10s) get gentler max backoff, slow intervals (60s+) reach cap quickly
- **Fast Recovery**: Warning state keeps normal interval probing for quick recovery detection
- **Resource Savings**: Critical state uses graduated backoff for persistent failures
- **Bucket-Aligned**: Multipliers ensure probes occur at bucket boundaries
- **Hard Cap**: 5 minutes maximum regardless of interval

---

## Probe Scheduling

### Bucket Assignment

Records are assigned to buckets based on their `probe.interval`:

```
Probe Interval    Bucket Assignment
--------------    -----------------
   10s            10s bucket
   20s            20s bucket
   30s            30s bucket
   60s            60s bucket
   90s            90s bucket
  120s            120s bucket
  180s            180s bucket
  300s            300s bucket
```

### Probe Execution Timeline

```
Time    10s Bucket   20s Bucket   60s Bucket
----    ----------   ----------   ----------
  0s    ●            ●            ●
 10s    ●
 20s    ●            ●
 30s    ●
 40s    ●            ●
 50s    ●
 60s    ●            ●            ●
```

### Immediate Re-Probe on State Transition

```
Normal Flow (Before):
  IP fails probe → PASSING → WARNING (first failure)
  Next probe: 10-300 seconds later (bucket interval)

Event-Driven Re-Probe (New):
  IP fails probe → PASSING → WARNING (first failure)
  Immediate re-probe triggered (~100ms delay)

Result:
  - Success: WARNING → PASSING (recovery in <1 second)
  - Failure: WARNING → CRITICAL (failover in <1 second vs 10-300s)
```

---

## Performance Characteristics

### Scale Limits

| Metric | Value |
|--------|-------|
| IPs per Controller | 100,000+ |
| Probes per Second | 10,000+ |
| Controllers | Unlimited (horizontal scaling) |
| Shards | 128 × 8 = 1024 partitions |
| Records per Bucket | Unlimited |
| Worker Threads | 20-500 per bucket (CPU-dependent) |

### Throughput Optimization

**Without Optimizations:**
```
5,000 records × 1 query each = 5,000 queries/cycle
1,000 health updates/sec = 1,000 MongoDB writes/sec
```

**With Optimizations:**
```
5,000 records → 1 aggregation query/cycle (5000x reduction)
1,000 updates/sec → 1 MongoDB write/sec (1000x batching)
```

### Failover Speed Comparison

| Scenario | Without Immediate Re-Probe | With Immediate Re-Probe |
|----------|----------------------------|-------------------------|
| 10s interval bucket | 10-20 seconds | <1 second |
| 60s interval bucket | 60-120 seconds | <1 second |
| 300s interval bucket | 300-600 seconds | <1 second |

**Immediate re-probe delivers <1 second failover regardless of probe interval!**

---

## Configuration

### Environment Variables

```yaml
# GSLB system is auto-started by controller
# Configuration is read from MongoDB gslb_records collection
```

### Probe Configuration (Per Record)

```go
probe := &models.GSLBProbe{
    Enabled:            true,
    Interval:           30,              // Seconds (10, 20, 30, 60, 90, 120, 180, 300)
    Timeout:            5.0,             // Probe timeout in seconds
    Protocol:           "http",          // "http" | "tcp"
    Path:               "/health",       // HTTP path
    Port:               443,             // Target port
    ExpectedStatus:     []int{200, 204}, // HTTP status codes for success
    WarningThreshold:   1,               // Failures to WARNING
    CriticalThreshold:  2,               // Failures to CRITICAL
}
```

### CPU-Based Worker Scaling

```go
// Automatically calculated based on runtime.NumCPU()
// No manual configuration needed

// Example: 16 CPU cores
// 10s bucket:  min=80,  max=800
// 60s bucket:  min=40,  max=400
// 300s bucket: min=20,  max=200
```

---

## Monitoring & Metrics

### Metrics Pushed to Registry

All metrics are sent to Registry via gRPC MetricsPusher (fire-and-forget).

#### 1. Bucket Cycle Metrics
```
elchi_gslb_bucket_cycle_duration_seconds{interval="10s", controller="ctrl-1"} = 0.234
elchi_gslb_bucket_ips_total{interval="10s", controller="ctrl-1"} = 1234
elchi_gslb_bucket_ips_probed{interval="10s", controller="ctrl-1"} = 1200
elchi_gslb_bucket_ips_skipped{interval="10s", controller="ctrl-1"} = 34
elchi_gslb_bucket_completion_percent{interval="10s", controller="ctrl-1"} = 23.4
```

#### 2. State Transition Metrics
```
elchi_gslb_state_transitions_total{
  from="passing",
  to="warning",
  controller="ctrl-1"
} = 45
```

#### 3. Worker Pool Metrics
```
elchi_gslb_workers_current{interval="10s", controller="ctrl-1"} = 150
elchi_gslb_queue_depth{interval="10s", controller="ctrl-1"} = 234
elchi_gslb_scale_up_count{interval="10s", controller="ctrl-1"} = 12
elchi_gslb_scale_down_count{interval="10s", controller="ctrl-1"} = 5
```

### Health Check Logs

```
# State transitions (INFO level)
INFO  [gslb/system] health_checker.go:427 🔄 State transition: 1.2.3.4 passing → warning (failures: 1, successes: 0)

# Immediate re-probe trigger (INFO level)
INFO  [gslb/system] health_checker.go:466 ⚡ Triggering immediate re-probe for 1.2.3.4 (WARNING transition)

# Re-probe result (DEBUG level)
DEBUG [gslb/system] health_checker.go:506 ⚡ Re-probe result sent for 1.2.3.4 (success: true)

# Worker scaling (DEBUG level)
DEBUG [gslb/system] bucket_worker_pool.go:145 Bucket 10s spawned 20 workers (total: 100)

# Manual reset (DEBUG level)
DEBUG [gslb/system] health_checker.go:338 Manual reset detected for 1.2.3.4: Counter reset to 0
```

---

## API Usage

### Starting the GSLB System

```go
package main

import (
    "context"
    "log"

    "github.com/CloudNativeWorks/elchi-backend/pkg/gslb"
    "github.com/CloudNativeWorks/elchi-backend/pkg/db"
    "github.com/CloudNativeWorks/elchi-backend/pkg/logger"
    "github.com/CloudNativeWorks/elchi-backend/pkg/metrics"
)

func main() {
    // Initialize dependencies
    database := db.Connect(/* ... */)
    lgr := logger.New(/* ... */)
    pusher := metrics.NewMetricsPusher(/* ... */)

    // Create GSLB system
    system := gslb.NewSystem(
        database,
        "controller-1",  // Unique controller ID
        pusher,
        lgr,
    )

    // Start system
    ctx := context.Background()
    if err := system.Start(ctx); err != nil {
        log.Fatalf("Failed to start GSLB: %v", err)
    }

    // Graceful shutdown
    defer func() {
        if err := system.Stop(); err != nil {
            log.Printf("GSLB shutdown error: %v", err)
        }
    }()

    // Keep running
    select {}
}
```

### Creating a GSLB Record

```go
record := &models.GSLBRecord{
    FQDN:    "api.example.com.",
    Project: "proj-123",
    Probe: &models.GSLBProbe{
        Enabled:           true,
        Interval:          30,
        Timeout:           5.0,
        Protocol:          "http",
        Path:              "/health",
        Port:              443,
        ExpectedStatus:    []int{200, 204},
        WarningThreshold:  1,
        CriticalThreshold: 2,
    },
}

// Insert to MongoDB gslb_records collection
// GSLB system will automatically pick it up on next rebalance
```

### Adding IP to GSLB Record

```go
ipHealth := &models.GSLBIPHealth{
    RecordID:         recordID,
    IP:               "1.2.3.4",
    FQDN:             "api.example.com.",
    HealthState:      models.HealthStatePassing,
    LastStatusChange: time.Now(),
    ShardID:          calculateShardID(fqdn),
    SubShardID:       calculateSubShardID(fqdn),
}

// Insert to MongoDB gslb_ip_health collection
// Will be probed starting next bucket cycle
```

### Manual Health State Override (API Endpoint)

```bash
# Update IP health state manually
PUT /api/v3/gslb/{record_id}/ips/{ip}
{
  "health_state": "passing"
}

# System behavior:
# 1. Sets health_state to "passing"
# 2. Clears backoff_until and current_backoff
# 3. Sets manual_reset_at timestamp
# 4. Counter manager detects reset → resets counter to 0
# 5. Next probe establishes new baseline
```

---

## Troubleshooting

### Common Issues

#### 1. **No Probes Executing**

**Symptoms:**
- No state transitions in logs
- `elchi_gslb_bucket_ips_probed` metric = 0

**Diagnosis:**
```bash
# Check if shards are acquired
mongo> db.gslb_shards.find({controller_id: "controller-1"})

# Check if records exist
mongo> db.gslb_records.find({probe.enabled: true})

# Check logs for shard acquisition
grep "Acquired shard" controller.log
```

**Solutions:**
- Verify MongoDB connection
- Check that `probe.enabled = true` in records
- Ensure controller has acquired shards (check lease_expires_at)

---

#### 2. **High Queue Depth / Dropped Probes**

**Symptoms:**
```
WARNING [gslb/system] bucket_worker_pool.go:127 Bucket 10s queue full, dropping probe for IP 1.2.3.4
```

**Diagnosis:**
```bash
# Check worker pool stats
curl http://localhost:8080/api/v3/gslb/stats

# Look for queue_depth > 70% of queue_capacity
```

**Solutions:**
- Auto-scaler should handle this automatically
- If persistent: Increase CPU cores (workers scale with CPU)
- Check for slow probes (network latency, DNS issues)

---

#### 3. **Slow Failover Detection**

**Symptoms:**
- IP fails but takes 60+ seconds to detect

**Diagnosis:**
```bash
# Check probe interval and thresholds
mongo> db.gslb_records.find({fqdn: "api.example.com."}, {probe: 1})

# Verify immediate re-probe is working
grep "⚡ Triggering immediate re-probe" controller.log
```

**Solutions:**
- Ensure `probe.interval` is reasonable (≤60s for critical services)
- Verify immediate re-probe is triggered (should see "⚡ Triggering immediate re-probe" logs on WARNING transitions)
- Check that WARNING threshold = 1 (enables immediate re-probe after first failure)

---

#### 4. **MongoDB Timeout Errors**

**Symptoms:**
```
ERROR [gslb/system] timer_bucket.go:235 Bucket 20s: Failed to get IPs (batch query): context deadline exceeded
```

**Diagnosis:**
- Network latency to MongoDB Atlas
- Large result sets (10K+ IPs per query)
- Missing indexes

**Solutions:**
- Increase query timeout (default: 30s)
- Add compound index: `{shard_id: 1, sub_shard_id: 1, health_state: 1}`
- Reduce shard size (split shards if > 10K IPs per shard)

---

#### 5. **Manual Reset Not Working**

**Symptoms:**
- Set IP to PASSING via API
- IP immediately goes back to WARNING/CRITICAL

**Diagnosis:**
```bash
# Check probe result
curl -i http://<IP>:<PORT><PATH>

# Verify manual_reset_at field is set
mongo> db.gslb_ip_health.find({ip: "1.2.3.4"}, {manual_reset_at: 1})
```

**Solutions:**
- **If probe actually fails**: IP is legitimately unhealthy (403, 500, timeout)
  - Fix the upstream service
  - Verify probe config (path, port, expected_status)

- **If manual_reset_at missing**: API update failed
  - Check API logs for errors
  - Verify record_id and IP are correct

---

#### 6. **Orphan IP Warnings**

**Symptoms:**
```
WARNING [gslb/system] timer_bucket.go:405 FastFail: Skipping IP health record with empty IP address (RecordID: 000000000000000000000000, FQDN: )
```

**Diagnosis:**
- IP health record exists but parent GSLB record was deleted
- Data corruption (empty IP field)

**Solutions:**
```javascript
// Find orphaned IP health records (no parent record)
db.gslb_ip_health.aggregate([
  {
    $lookup: {
      from: "gslb_records",
      localField: "record_id",
      foreignField: "_id",
      as: "record"
    }
  },
  { $match: { record: { $size: 0 } } }
])

// Delete orphaned records
db.gslb_ip_health.deleteMany({
  record_id: { $in: [/* orphaned record IDs */] }
})
```

---

#### 7. **Write Buffer Not Flushing**

**Symptoms:**
- Health states not updating in MongoDB
- `updated_at` timestamp stale

**Diagnosis:**
```bash
# Check write buffer flush logs
grep "Flushed.*health state updates" controller.log

# Verify MongoDB connection
mongo> db.adminCommand({ping: 1})
```

**Solutions:**
- Check MongoDB connection health
- Verify write buffer is running (auto-started by System)
- Check for write errors in logs

---

### Debug Commands

```bash
# View all controller-owned shards
mongo> db.gslb_shards.find({controller_id: "controller-1"})

# Count IPs per health state
mongo> db.gslb_ip_health.aggregate([
  { $group: { _id: "$health_state", count: { $sum: 1 } } }
])

# Find IPs in backoff
mongo> db.gslb_ip_health.find({
  backoff_until: { $gt: new Date() }
})

# Check WARNING IPs (will trigger immediate re-probe)
mongo> db.gslb_ip_health.find({
  health_state: "warning"
})

# View bucket statistics
curl http://localhost:8080/api/v3/gslb/stats | jq
```

---

## Related Documentation

- **System Architecture**: `/docs/architecture/gslb-system.md`
- **API Reference**: `/docs/api/gslb-endpoints.md`
- **MongoDB Schema**: `/docs/database/gslb-collections.md`
- **Metrics Guide**: `/docs/monitoring/gslb-metrics.md`

---

## License

Copyright © 2024 CloudNativeWorks. All rights reserved.
