# GSLB (Global Server Load Balancing) System

Distributed, scalable health checking system for DNS-based load balancing with intelligent failover detection and multi-controller coordination.

---

## Table of Contents

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
│  │   Shard    │───│  Time Wheel  │───│  Health Checker    │   │
│  │  Manager   │   │  Scheduler   │   │  (Quad-State FSM)  │   │
│  └────────────┘   └──────────────┘   └────────────────────┘   │
│        │                  │                      │              │
│        │                  ▼                      ▼              │
│        │         ┌─────────────────┐   ┌─────────────────┐    │
│        │         │  Time Wheel     │   │ Write Buffer    │    │
│        │         │  (512 slots)    │   │ (Batched I/O)   │    │
│        │         │  1s granularity │   └─────────────────┘    │
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
2. Time Wheel Scheduler loads GSLB records for owned shards into 512-slot ring buffer
3. Time Wheel advances every 1 second, executing tasks in current slot
4. Probe Executor performs HTTP/TCP health checks via shared worker pool
5. Health Checker processes results → state transitions → automatic rescheduling
6. Write Buffer batches updates → MongoDB flush (every 5s or 100 updates)
7. State Transition Event: Results automatically reschedule IPs based on health state
```

---

## Key Features

### Horizontal Scaling
- **Distributed Sharding**: 128 shards × 8 sub-shards = 1024 partitions
- **Leader Election**: MongoDB-based lease system (30s TTL, renewed every 25s)
- **Auto-Rebalancing**: Detects controller failures and redistributes work
- **No Single Point of Failure**: Any controller can handle any shard

###Per-IP Scheduling (Time Wheel)
- **Individual Scheduling**: Each record+IP has independent next probe time (not batch-based)
- **1-Second Granularity**: 512-slot time wheel with 1-second resolution
- **Immediate Response**: Manual PASS operations trigger re-probe within 1 second
- **Graduated Backoff**: Progressive backoff (10s→20s→30s→50s→80s→120s) works naturally

### Quad-State Health Model (Anti-Flapping)
- **PASSING** → **WARNING** → **CRITICAL** → **RECOVERY** → **PASSING** progression
- **Configurable Thresholds**:
  - `warning_threshold` (default: 1) - failures to WARNING
  - `critical_threshold` (default: 2) - failures to CRITICAL
  - `passing_threshold` (default: 1) - consecutive successes required for RECOVERY → PASSING
- **Anti-Flapping**: RECOVERY state prevents rapid oscillation between CRITICAL and PASSING
- **Circuit Breaker**: Graduated backoff for CRITICAL IPs (10s → 20s → 30s → 50s → 80s → 120s)
- **Manual Override**: Admin can reset health state via API with counter reset

### Performance Optimizations
- **On-Demand Fetching**: Only fetch IPs ready to probe (no prefetch parking lot)
- **Write Batching**: Batched MongoDB writes (every 5s or 100 updates)
- **Complete Isolation**: Each record+IP maintains independent state (no deduplication, no fan-out)
- **CPU-Aware Workers**: Single shared auto-scaling worker pool (100-500 workers based on CPU cores)

### Reliability Features
- **Graceful Shutdown**: sync.Once patterns prevent double-close panics
- **Resource Cleanup**: HTTP client pool connections properly closed on shutdown
- **Defensive Programming**: Validates task data (nil checks, empty IP checks)
- **Backoff Retry**: Exponential backoff with jitter for failed probes (thundering herd prevention)
- **Counter Persistence**: Infers failure count from health state on restart
- **Stale Counter Cleanup**: Automatic cleanup of counters for deleted IPs (every 5 minutes)

### 📈 Probe Metrics
- **Success/Failure Counters**: Atomic counters for probe results
- **Latency Tracking**: Min/Max/Avg probe latency in microseconds
- **Error Categorization**: 21+ error types (timeout, connection_refused, dns_failure, etc.)
- **Overflow Protection**: Safe handling of latency counter overflow

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

### 3. **TimeWheel** (`time_wheel.go`)
Linux kernel-style time wheel scheduler with 512 slots and 1-second granularity.

**Time Wheel Architecture:**
- **512 Slots**: Circular ring buffer (512 seconds = ~8.5 minutes max delay)
- **1-Second Granularity**: Advances every second via ticker
- **Per-IP Scheduling**: Each record+IP scheduled independently based on health state
- **O(1) Operations**: Schedule, reschedule, remove all O(1) complexity

**Scheduling Strategy:**
```
PASSING state:    Schedule at currentSlot + probe.interval
WARNING state:    Schedule at currentSlot + probe.interval/2 (increased monitoring)
CRITICAL state:   Schedule at currentSlot + graduatedBackoff (10s→80s→120s)
Manual PASS:      Schedule at currentSlot + 0 (immediate, within 1 second)
```

**Complete Record Isolation:**
```
Each record+IP combination maintains independent:
- Health state (PASSING/WARNING/CRITICAL)
- Consecutive failure counter
- Backoff state (backoff_until, current_backoff)

Example:
  Record A (abc.com): IP=1.2.3.4 → CRITICAL + scheduled in slot 80 (80s delay)
  Record B (xyz.com): IP=1.2.3.4 → PASSING + scheduled in slot 10 (10s delay)

Result: Each schedules independently, NO fan-out, NO shared state
```

**Key Methods:**
- `Schedule(task, delaySeconds)`: Add task to specific slot (O(1))
- `Reschedule(recordID, ip, delaySeconds)`: Move task to new slot (O(1))
- `RescheduleImmediate(recordID, ip)`: Move to current slot (O(1))
- `executeCurrentSlot()`: Execute all tasks in current slot, then advance
- `HandleProbeResult(...)`: Automatic rescheduling based on probe result

---

### 4. **WorkerPool** (`worker_pool.go`)
Single shared worker pool with CPU-aware auto-scaling for all probes.

**Auto-Scaling Algorithm:**
- **Scale-Up Trigger**: Queue depth > 70% → Add 20% workers (min 10)
- **Scale-Down Trigger**: Queue depth < 20% → Remove 10% workers (min 5)
- **Emergency Scale**: Queue full for 30s → Add 50% workers immediately
- **Rate Limiting**: Minimum 10s between scale actions

**DynamicQueue (Auto-Expanding):**
```go
// Unlike Go channels, DynamicQueue can grow without breaking consumers
type DynamicQueue[T any] struct {
    buffer          []T           // Ring buffer
    initialCapacity int           // Starting size (1000)
    maxCapacity     int           // Hard limit (50x maxWorkers)
    growthFactor    float64       // 2.0 (double on expand)
}

// Auto-shrink: When usage < 25% for 5+ minutes, halve capacity
// Prevents memory bloat after traffic spikes
```

**Worker Lifecycle:**
```
1. Worker goroutine pulls task from DynamicQueue (blocks if empty)
2. Validates task (nil checks, empty IP checks)
3. Executes probe with timeout context
4. Attaches task context (contains RecordID list for fan-out)
5. Sends result to sharded result queue (by IP hash)
6. Repeats until shutdown signal
```

**Shared Worker Pool:**
- All Time Wheel tasks use this single worker pool
- Single shared pool for all intervals (eliminates resource fragmentation)
- CPU-aware limits: minWorkers=100, maxWorkers=500 (16-core system)
- **sync.Once on Stop()**: Prevents double-close panic on shutdown

**Key Methods:**
- `Submit(task)`: Always succeeds (queue auto-expands up to maxCapacity)
- `spawnWorkers(count)`: Creates N new worker goroutines
- `GetStats()`: Returns pool statistics (workers, queue depth, scale count)

---

### 5. **HealthChecker** (`health_checker.go`)
Processes probe results and manages quad-state health transitions (PASSING → WARNING → CRITICAL → RECOVERY).

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

### 6. **CounterManager** (`counter_manager.go`)
In-memory failure/success counters with persistence inference and cleanup.

**Counter Structure:**
```go
type IPHealthCounter struct {
    ConsecutiveFailures  int64     // Incremented on probe failure (atomic)
    ConsecutiveSuccesses int64     // Incremented on probe success (atomic)
    LastAccessed         time.Time // For stale counter cleanup
}
```

**Persistence Inference:**
- On controller restart, counter doesn't exist in memory
- Infers failure count from persisted health state:
  - PASSING → 0 failures, 0 successes
  - WARNING → `warning_threshold` failures
  - CRITICAL → `critical_threshold` failures
  - RECOVERY → 0 failures, inferred successes from state

**Stale Counter Cleanup:**
- Runs every 5 minutes
- Removes counters not accessed in 1 hour
- Prevents memory leak for deleted IPs
- **sync.Once on Stop()**: Prevents double-close panic

**Key Methods:**
- `GetOrInitialize(...)`: Gets counter or creates new one (with inference)
- `Update(recordID, ip, success)`: Updates counter based on probe result
- `Reset(recordID, ip)`: Resets counter to 0 (manual override)
- `Stop()`: Gracefully stops cleanup goroutine (safe to call multiple times)

---

### 7. **WriteBuffer** (`write_buffer.go`)
Batched write system for high-throughput MongoDB updates.

**Batching Strategy:**
- **Flush Triggers**: Every 5 seconds OR 100 buffered updates (whichever comes first)
- **Deduplication**: Only latest update per IP is kept (map key = RecordID + IP)
- **Atomic Operations**: Uses `$set` and `$unset` for field updates
- **Immediate Flush**: `FlushImmediate()` removes pending buffered writes to prevent race conditions

**Write Performance:**
```
Without Batching:  100 updates/sec = 100 MongoDB writes/sec
With Batching:     100 updates/sec = ~1 MongoDB write per 5s (100x reduction)
```

**Update Types:**
1. **Normal Health Update**: Sets health_state, backoff, timestamps, history
2. **Manual Reset Clear**: Unsets `manual_reset_at` field only
3. **Immediate Write**: Bypasses buffer for critical state transitions

**Key Methods:**
- `Add(update)`: Buffers health state update (non-blocking)
- `FlushImmediate(ctx, update)`: Synchronous write, clears pending buffer for same IP
- `RemovePending(recordID, ip)`: Clears buffered updates for specific IP
- `Stop()`: Final flush before shutdown (sync.Once protected, safe to call multiple times)

---

### 8. **IPHealthManager** (`ip_health_manager.go`)
MongoDB repository for IP health records with batch query optimization.

**Key Queries:**

**1. Batch IP Query (On-Demand):**
```go
// Time Wheel executes current slot
// Collects unique record IDs from tasks in slot
// Batch query for ONLY those records (not all records)
ipsByRecord, _ := GetIPsByRecordIDs(recordIDs) // On-demand fetch
```

**Performance:**
- Query timeout: 5 seconds (on-demand queries are small)
- Indexes: `{shard_id, sub_shard_id, record_id}` compound index
- Fetch only IPs ready to probe (no prefetch parking lot)

**Key Methods:**
- `GetIPsByRecordIDs(recordIDs)`: Batch query for current slot tasks
- `CreateIPHealth(ip)`: Creates new IP health record with shard assignment
- `UpdateHealthState(...)`: Direct MongoDB update (used for manual override)

---

### 9. **ProbeExecutor** (`probe_executor.go`)
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

### 10. **CPUConfig** (`cpu_config.go`)
CPU-aware worker pool configuration for single shared worker pool.

**Worker Limits:**
```go
type WorkerLimits struct {
    MinWorkers int // CPU cores × 6.25 (e.g., 16 cores = 100)
    MaxWorkers int // CPU cores × 31.25 (e.g., 16 cores = 500)
}
```

**Rationale:**
- Single shared pool eliminates resource fragmentation
- CPU cores determine parallelism capacity
- Auto-scaling handles burst traffic from all intervals

---

### 11. **DynamicQueue** (`dynamic_queue.go`)
Thread-safe, dynamically-growing queue that replaces fixed-size Go channels.

**Features:**
```go
type DynamicQueue[T any] struct {
    buffer          []T           // Ring buffer implementation
    initialCapacity int           // Starting size (default: 1000)
    maxCapacity     int           // Hard limit (default: 50x maxWorkers)
    growthFactor    float64       // Expansion multiplier (default: 2.0)
}
```

**Auto-Expand:**
- When buffer full and below maxCapacity → double capacity
- When at maxCapacity → block until space available

**Auto-Shrink:**
- When usage < 25% for 5+ minutes → halve capacity
- Never shrinks below initialCapacity
- Prevents memory bloat after traffic spikes

**Thread Safety:**
- `sync.Mutex` for buffer operations
- `sync.Cond` for blocking Enqueue/Dequeue

**Metrics Tracked:**
- `Enqueued`, `Dequeued`, `Dropped` (atomic counters)
- `Expansions`, `Shrinks` (resize operations)
- `PeakSize` (high water mark)

---

### 12. **Keys Helper** (`keys.go`)
Shared key generation utility for consistent IP key format across components.

```go
// MakeIPKey creates a unique key for an IP within a GSLB record
// Format: "recordID:ip" (e.g., "507f1f77bcf86cd799439011:192.168.1.1")
func MakeIPKey(recordID primitive.ObjectID, ip string) string {
    return fmt.Sprintf("%s:%s", recordID.Hex(), ip)
}
```

**Usage:**
- CounterManager: In-memory counter lookup
- TimeWheel: Task tracking and deduplication
- HealthChecker: Probe result processing

**Rationale:**
- Prevents duplicate key generation logic
- Ensures consistent format across all components

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

### State Diagram (Quad-State with Anti-Flapping)

```
                    ┌──────────────┐
         ┌─────────►│   PASSING    │
         │          └──────────────┘
         │                 │
         │                 │ failures ≥ warning_threshold
         │                 ▼
         │          ┌──────────────┐
         │          │   WARNING    │───────────┐
         │          └──────────────┘           │
         │                 │                   │ success
         │                 │ failures ≥        │
         │                 │ critical_threshold│
         │                 ▼                   ▼
         │          ┌──────────────┐    ┌──────────────┐
         │          │   CRITICAL   │───►│   RECOVERY   │
         │          └──────────────┘    └──────────────┘
         │                 │                   │
         │                 │ backoff expires   │ successes ≥
         │                 │ + probe succeeds  │ passing_threshold
         │                 │                   │
         │                 └───────────────────┘
         │                           │
         │                           │
         └───────────────────────────┘
```

**Anti-Flapping Mechanism:**
- CRITICAL → RECOVERY (on first success after backoff)
- RECOVERY → PASSING (only after `passing_threshold` consecutive successes)
- RECOVERY → WARNING (on failure, requires starting over)
- Prevents rapid oscillation for unstable endpoints

### Thresholds (Configurable)

```go
probe := &models.GSLBProbe{
    WarningThreshold:  1,  // 1 failure  → WARNING
    CriticalThreshold: 2,  // 2 failures → CRITICAL
    PassingThreshold:  1,  // 1 success  → RECOVERY → PASSING (anti-flapping)
}
```

**Example with passing_threshold=3:**
```
CRITICAL IP receives backoff, then:
  Success #1: CRITICAL → RECOVERY (not PASSING yet!)
  Success #2: Still RECOVERY
  Success #3: RECOVERY → PASSING ✓

If failure occurs during RECOVERY:
  RECOVERY → WARNING (back to degraded state)
```

### Circuit Breaker (Adaptive Backoff Strategy)

**Purpose**: Stop probing persistently failing endpoints to save resources while allowing fast recovery detection.

**Strategy**: Interval-aware graduated backoff
- **Warning State**: NO backoff → probe at normal interval for fast recovery detection
- **Critical State**: Adaptive graduated backoff based on probe interval with 5-minute cap

**Adaptive Backoff Multipliers**: `[1.0, 2.0, 3.0, 5.0, 8.0, 12.0]`

Backoff scales with probe interval to ensure aligned scheduling:

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
- **Time-Aligned**: Multipliers ensure probes occur at aligned intervals
- **Hard Cap**: 5 minutes maximum regardless of interval

---

## Probe Scheduling

### Time Wheel Scheduling

Each record+IP is scheduled independently in the 512-slot time wheel:

```
Time Wheel State (example at T=0):
Slot 0:   [IP A (manual PASS), IP C (immediate)]    ← Current slot (execute now)
Slot 1:   [IP F]                                      ← Execute in 1 second
Slot 10:  [IP B (PASSING, 10s interval)]             ← Execute in 10 seconds
Slot 25:  [IP D (CRITICAL, 25s backoff)]             ← Execute in 25 seconds
Slot 80:  [IP E (CRITICAL, 80s backoff)]             ← Execute in 80 seconds
Slot 511: [IP G]                                      ← Execute in 511 seconds (max)

Every second: executeCurrentSlot() → advance → repeat
```

### Scheduling Strategy by Health State

```
PASSING:
  - Interval: probe.interval (10s-300s)
  - Example: 30s interval → scheduled in slot 30

WARNING:
  - Interval: probe.interval / 2 (increased monitoring)
  - Example: 30s interval → scheduled in slot 15

CRITICAL:
  - Interval: Graduated backoff (10s→20s→30s→50s→80s→120s)
  - Example: 3rd failure → 30s backoff → scheduled in slot 30
  - Max backoff: 120 seconds (capped)

Manual PASS:
  - Interval: 0 (immediate re-probe)
  - Scheduled in slot 0 (next tick = within 1 second)
```

### Dynamic Rescheduling

```
Automatic Rescheduling After Probe:
1. Probe completes → HandleProbeResult() called
2. Determine new health state and delay based on result
3. Reschedule task to appropriate slot
4. Process repeats automatically

Example Flow:
  IP at slot 10 (PASSING, 10s interval)
  → Probe executes → Fails
  → State: PASSING → WARNING
  → Reschedule to slot 5 (10s / 2 = 5s, increased monitoring)
  → Probe executes → Fails again
  → State: WARNING → CRITICAL
  → Reschedule to slot 10 (first backoff = 10s)
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
| Records per Interval | Unlimited |
| Worker Threads | 100-500 shared pool (CPU-dependent) |

### Throughput Optimization

**Time Wheel Approach:**
```
On-Demand Fetching: Only fetch IPs ready to probe in current slot
Batch Query: 1 aggregation query per slot execution (not per record)
Write Batching: 100 updates → 1 MongoDB write every 5s (100x reduction)
```

**Example:**
```
Slot 10 executes with 50 tasks
→ 20 unique record IDs
→ 1 batch query fetches all 20 records' IPs
→ 50 probes execute
→ Results buffered in write buffer
→ Flush to MongoDB every 5s or 100 updates
```

### Failover Speed Comparison

| Scenario | Bucket-Based (Old) | Time Wheel (New) |
|----------|-------------------|------------------|
| Manual PASS | 0-10 seconds | <1 second |
| PASSING → WARNING | 10-300 seconds | probe.interval / 2 |
| WARNING → CRITICAL | 10-300 seconds | probe.interval / 2 |
| CRITICAL backoff | Fixed intervals | Graduated 10s→120s |

**Time Wheel delivers per-IP scheduling with graduated backoff!**

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
// 10s interval:  min=80,  max=800
// 60s interval:  min=40,  max=400
// 300s interval: min=20,  max=200
```

---

## Monitoring & Metrics

### Metrics Pushed to Registry

All metrics are sent to Registry via gRPC MetricsPusher (fire-and-forget).

#### 1. Time Wheel Metrics
```
elchi_gslb_timewheel_current_slot{controller="ctrl-1"} = 42
elchi_gslb_timewheel_scheduled_total{controller="ctrl-1"} = 12345
elchi_gslb_timewheel_executed_total{controller="ctrl-1"} = 11000
elchi_gslb_timewheel_rescheduled_total{controller="ctrl-1"} = 11000
elchi_gslb_timewheel_current_load{controller="ctrl-1"} = 1234
elchi_gslb_timewheel_slot_depth{slot="0", controller="ctrl-1"} = 5
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
elchi_gslb_workers_current{controller="ctrl-1"} = 150
elchi_gslb_workers_queue_depth{controller="ctrl-1"} = 234
elchi_gslb_scale_up_count{controller="ctrl-1"} = 12
elchi_gslb_scale_down_count{controller="ctrl-1"} = 5
elchi_gslb_total_probes{controller="ctrl-1"} = 50000
```

#### 4. Probe Performance Metrics
```
elchi_gslb_probes_total{controller="ctrl-1", result="success"} = 45000
elchi_gslb_probes_total{controller="ctrl-1", result="failure"} = 5000
elchi_gslb_probe_success_rate_percent{controller="ctrl-1"} = 90.0
elchi_gslb_probe_latency_avg_seconds{controller="ctrl-1"} = 0.045
elchi_gslb_probe_latency_min_seconds{controller="ctrl-1"} = 0.001
elchi_gslb_probe_latency_max_seconds{controller="ctrl-1"} = 2.500
elchi_gslb_probe_errors_total{controller="ctrl-1", error_type="timeout"} = 2500
elchi_gslb_probe_errors_total{controller="ctrl-1", error_type="connection_refused"} = 1500
elchi_gslb_probe_errors_total{controller="ctrl-1", error_type="dns_failure"} = 500
```

#### 5. Write Buffer Metrics
```
elchi_gslb_write_buffer_size{controller="ctrl-1"} = 45
elchi_gslb_write_buffer_capacity_pct{controller="ctrl-1"} = 45.0
elchi_gslb_write_buffer_flush_total{controller="ctrl-1"} = 1234
elchi_gslb_write_buffer_updates_total{controller="ctrl-1"} = 50000
elchi_gslb_write_buffer_flush_errors_total{controller="ctrl-1"} = 2
elchi_gslb_write_buffer_avg_flush_duration_seconds{controller="ctrl-1"} = 0.015
```

#### 6. Result Queue Metrics
```
elchi_gslb_result_queue_depth{controller="ctrl-1"} = 50
elchi_gslb_result_queue_capacity_pct{controller="ctrl-1"} = 5.0
```

### Health Check Logs

```
# Time Wheel scheduling (DEBUG level)
DEBUG [gslb/system] time_wheel.go:195 📅 Scheduled 1.2.3.4 (record: abc123) in slot 10 (delay: 10s)

# Slot execution (DEBUG level)
DEBUG [gslb/system] time_wheel.go:310Slot 10: Executing 5 tasks

# State transitions (INFO level)
INFO  [gslb/system] health_checker.go:427 State transition: 1.2.3.4 passing → warning (failures: 1, successes: 0)

# Automatic rescheduling (DEBUG level)
DEBUG [gslb/system] time_wheel.go:420 🟡 WARNING: 1.2.3.4 → 5s re-probe

# Worker scaling (DEBUG level)
DEBUG [gslb/system] worker_pool.go:160 Spawned 20 workers (total: 150)

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
// Will be scheduled and probed by Time Wheel after system reload
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
- `elchi_gslb_timewheel_executed_total` metric = 0
- No slot execution logs

**Diagnosis:**
```bash
# Check if shards are acquired
mongo> db.gslb_shards.find({controller_id: "controller-1"})

# Check if records exist
mongo> db.gslb_records.find({probe.enabled: true})

# Check logs for shard acquisition and Time Wheel activity
grep "Acquired shard" controller.log
grep "Slot" controller.log
grep "📅 Scheduled" controller.log
```

**Solutions:**
- Verify MongoDB connection
- Check that `probe.enabled = true` in records
- Ensure controller has acquired shards (check lease_expires_at)
- Verify Time Wheel loaded records: check for "Loaded X tasks into time wheel" log

---

#### 2. **High Queue Depth / Submission Failures**

**Symptoms:**
```
ERROR [gslb/system] time_wheel.go:385 Failed to submit probe for 1.2.3.4 - worker pool queue full or closed
```

**Diagnosis:**
```bash
# Check worker pool stats
curl http://localhost:8080/api/v3/gslb/stats

# Look for queue_depth approaching queue_capacity
# Check for submission retry logs
grep "Failed to submit probe" controller.log
```

**Solutions:**
- Auto-scaler should handle this automatically (scales up when queue > 70%)
- If persistent: Increase CPU cores (workers scale with CPU)
- Check for slow probes (network latency, DNS issues)
- Verify worker pool not at maxWorkers limit (500 for 16-core system)

---

#### 3. **Slow Failover Detection**

**Symptoms:**
- IP fails but takes too long to detect state change

**Diagnosis:**
```bash
# Check probe interval and thresholds
mongo> db.gslb_records.find({fqdn: "api.example.com."}, {probe: 1})

# Verify Time Wheel automatic rescheduling
grep "Scheduled" controller.log | grep "1.2.3.4"
grep "State transition" controller.log
```

**Solutions:**
- Ensure `probe.interval` is reasonable (≤60s for critical services)
- Verify automatic rescheduling after state transitions (should see slot assignments in logs)
- Check that WARNING threshold = 1, CRITICAL threshold = 2 for fast detection
- WARNING state uses interval/2 for increased monitoring (e.g., 30s interval → 15s monitoring)

---

#### 4. **MongoDB Timeout Errors**

**Symptoms:**
```
ERROR [gslb/system] time_wheel.go:340 Failed to fetch IPs for slot 10: context deadline exceeded
```

**Diagnosis:**
- Network latency to MongoDB Atlas
- Large result sets in single slot (many tasks with same delay)
- Missing indexes

**Solutions:**
- Increase query timeout (currently: 5s)
- Add compound index: `{shard_id: 1, sub_shard_id: 1, record_id: 1}`
- Verify slot depth distribution (check `elchi_gslb_timewheel_slot_depth` metrics)
- If single slot has too many tasks: randomize initial scheduling to distribute load

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
WARNING [gslb/system] time_wheel.go:360 IP 1.2.3.4 not found in record abc123 (batch fetch)
```

**Diagnosis:**
- IP health record exists in Time Wheel but was deleted from MongoDB
- Record was deleted but tasks remain in Time Wheel slots
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

// Trigger shard rebalance to reload Time Wheel
# Controller will reload records and clear stale tasks
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

# Check WARNING IPs (should be re-probed at interval/2)
mongo> db.gslb_ip_health.find({
  health_state: "warning"
})

# View Time Wheel and worker pool statistics
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

Copyright © 2025 CloudNativeWorks. All rights reserved.
