# ⏱️ Time Configuration Reference

This document catalogs all time-based configurations, ticker intervals, and timeout values used throughout the Elchi Backend system.

## Table of Contents
- [Cleanup & Sync Intervals](#cleanup--sync-intervals)
- [Heartbeat & Health Checks](#heartbeat--health-checks)
- [Stale Detection Thresholds](#stale-detection-thresholds)
- [gRPC Keepalive Configuration](#grpc-keepalive-configuration)
- [Command Timeouts](#command-timeouts)
- [Retry & Backoff Configuration](#retry--backoff-configuration)
- [HTTP Server Configuration](#http-server-configuration)
- [Worker & Job Management](#worker--job-management)
- [Miscellaneous](#miscellaneous)

---

## Cleanup & Sync Intervals

Configuration for periodic cleanup tasks and synchronization operations.

| Component | Interval | Location | Description |
|-----------|----------|----------|-------------|
| **Registry Cleanup** | `5 minutes` | [cmd/registry.go:70](../cmd/registry.go#L70) | Removes stale controller and control-plane data from registry |
| **Client Sync** | `5 minutes` | [cmd/controller.go:124](../cmd/controller.go#L124) | Syncs client states between controller and registry |
| **Client Cleanup** | `5 minutes` | [controller/client/handlers/base.go:133](../controller/client/handlers/base.go#L133) | Removes stale client connections from database |
| **Envoy Cleanup** | `5 minutes` | Runs in `StartPeriodicSync()` | Marks stale envoy connections as disconnected |
| **Dependency Cache** | `1 minute` | [cmd/controller.go:155](../cmd/controller.go#L155) | Cleans up expired dependency cache entries |
| **Dependency TTL** | `5 minutes` | [controller/dependency/cache.go:35](../controller/dependency/cache.go#L35) | Time-to-live for dependency cache entries |

---

## Heartbeat & Health Checks

Regular health check and heartbeat mechanisms to detect failures.

| Component | Interval | Location | Description |
|-----------|----------|----------|-------------|
| **Envoy Heartbeat**  | `60 seconds` | [control-plane/envoys/heartbeat.go:21](../control-plane/envoys/heartbeat.go#L21) | Updates `lastSync` for active envoy connections |
| **Worker Status** | `30 seconds` | [pkg/async/worker/worker.go:287](../pkg/async/worker/worker.go#L287) | Updates async worker status ticker |
| **Control-Plane Health** | `15 seconds` | [pkg/registry/control_plane_manager.go:121](../pkg/registry/control_plane_manager.go#L121) | Registry checks control-plane health |
| **Control-Plane Node List** | `30 seconds` | [pkg/registry/control_plane_manager.go:343](../pkg/registry/control_plane_manager.go#L343) | Updates node list in registry |
| **Control-Plane Registration** | `30 seconds` | [pkg/registry/control_plane_manager.go:380](../pkg/registry/control_plane_manager.go#L380) | Re-registers control-plane with registry |
| **Controller Health** | `15 seconds` | [pkg/registry/controller.go:456](../pkg/registry/controller.go#L456) | Registry checks controller health |
| **Controller Registration** | `30 seconds` | [pkg/registry/controller.go:529](../pkg/registry/controller.go#L529) | Re-registers controller with registry |

---

## Stale Detection Thresholds

Thresholds for detecting stale or disconnected components.

| Component | Threshold | Location | Description |
|-----------|-----------|----------|-------------|
| **Client Stale** | `2 minutes` | [controller/client/services/sync_registry.go:311](../controller/client/services/sync_registry.go#L311) | Marks clients as disconnected if `last_seen > 2min` |
| **Envoy Stale** | `2 minutes` | [controller/client/services/sync_registry.go:334](../controller/client/services/sync_registry.go#L334) | Marks envoys as disconnected if `lastSync > 2min` |
| **Registry Client (Own)** | `10 minutes` | [controller/client/services/sync_registry.go:165](../controller/client/services/sync_registry.go#L165) | Marks own registry clients as stale |
| **Registry Client (Missing)** | `11 minutes` | [controller/client/services/sync_registry.go:268](../controller/client/services/sync_registry.go#L268) | Marks missing registry clients as stale |
| **Unhealthy Connection** | `10 minutes` | [controller/client/services/clients.go:810](../controller/client/services/clients.go#L810) | Removes unhealthy client connections |
| **Discovery Last Seen** | `2 minutes` | [controller/discovery/service.go:43](../controller/discovery/service.go#L43) | K8s discovery endpoint last seen threshold |
| **Stuck Job** | `10 minutes` | [pkg/async/job/manager.go:552](../pkg/async/job/manager.go#L552) | Marks jobs as stuck if running too long |

### Critical Relationship (Envoy Cleanup)

```
Heartbeat Interval:    30 seconds
Cleanup Interval:      5 minutes (300 seconds)
Stale Threshold:       2 minutes (120 seconds)
Safety Margin:         120s / 30s = 4 heartbeat cycles
```

**Flow:**
1. Control-plane heartbeat updates `lastSync` every 30s
2. If control-plane crashes, `lastSync` stops updating
3. After 2 minutes (4 missed heartbeats), envoy is marked stale
4. Cleanup runs every 5 minutes and detects stale envoys
5. Status recalculated: Live → Partial → Offline

---

## gRPC Keepalive Configuration

gRPC keepalive settings for connection health monitoring.

### Control-Plane Server

| Setting | Value | Location | Description |
|---------|-------|----------|-------------|
| **Time** | `30 seconds` | [control-plane/server/server.go:30](../control-plane/server/server.go#L30) | Ping interval when no activity |
| **Timeout** | `10 seconds` | [control-plane/server/server.go:31](../control-plane/server/server.go#L31) | Wait time for ping ack |
| **MinTime** | `1 second` | [control-plane/server/server.go:32](../control-plane/server/server.go#L32) | Minimum ping interval from client |

### Controller Client

| Setting | Value | Location | Description |
|---------|-------|----------|-------------|
| **Time** | `30 seconds` | [controller/client/client.go:64](../controller/client/client.go#L64) | Health check interval |
| **Timeout** | `5 seconds` | [controller/client/client.go:65](../controller/client/client.go#L65) | Health check timeout |
| **MinTime** | `10 seconds` | [controller/client/client.go:70](../controller/client/client.go#L70) | Minimum ping interval |

### Bridge Client

| Setting | Value | Location | Description |
|---------|-------|----------|-------------|
| **Time** | `30 seconds` | [pkg/bridge/client.go:43](../pkg/bridge/client.go#L43) | Keepalive time |
| **Timeout** | `10 seconds` | [pkg/bridge/client.go:44](../pkg/bridge/client.go#L44) | Keepalive timeout |

### Registry Server

| Setting | Value | Location | Description |
|---------|-------|----------|-------------|
| **Time** | `60 seconds` | [registry/server/grpc.go:508](../registry/server/grpc.go#L508) | Keepalive ping interval |
| **Timeout** | `30 seconds` | [registry/server/grpc.go:509](../registry/server/grpc.go#L509) | Keepalive timeout |
| **MinTime** | `10 seconds` | [registry/server/grpc.go:512](../registry/server/grpc.go#L512) | Minimum client ping interval |

### Registry Connection Defaults

| Setting | Value | Location | Description |
|---------|-------|----------|-------------|
| **Keepalive Time** | `60 seconds` | [pkg/registry/connection.go:23](../pkg/registry/connection.go#L23) | Default keepalive time |
| **Keepalive Timeout** | `20 seconds` | [pkg/registry/connection.go:24](../pkg/registry/connection.go#L24) | Default keepalive timeout |

---

## Command Timeouts

Timeout values for various client commands and operations.

| Command Type | Timeout | Location | Description |
|--------------|---------|----------|-------------|
| **WAF_VERSION (Download)** | `120 seconds` | [controller/client/services/commands.go:97](../controller/client/services/commands.go#L97) | WAF binary download timeout |
| **ENVOY_VERSION** | `120 seconds` | [controller/client/services/commands.go:100](../controller/client/services/commands.go#L100) | Envoy version operations |
| **Default Command** | `30 seconds` | [controller/client/services/commands.go:103](../controller/client/services/commands.go#L103) | Default command timeout |
| **FRR Commands** | `45 seconds` | [controller/client/services/commands.go:106](../controller/client/services/commands.go#L106) | FRR routing operations |
| **Service Commands** | `15 seconds` | [controller/client/services/commands.go:109](../controller/client/services/commands.go#L109) | Service management commands |
| **HTTP Forward** | `25 seconds` | [controller/client/handlers/commands.go:61](../controller/client/handlers/commands.go#L61) | HTTP proxy forward timeout |
| **Command Execution** | `60 seconds` | [controller/client/handlers/commands.go:237](../controller/client/handlers/commands.go#L237) | General command execution |
| **Client Send** | `3 seconds` | [controller/client/client.go:135](../controller/client/client.go#L135) | Client message send timeout |
| **Registry Notification** | `5 seconds` | [controller/client/services/clients.go:826](../controller/client/services/clients.go#L826) | Registry notification timeout |

### HTTP Client Configuration

| Setting | Value | Location | Description |
|---------|-------|----------|-------------|
| **Idle Connection Timeout** | `90 seconds` | [controller/client/handlers/commands.go:83](../controller/client/handlers/commands.go#L83) | How long idle connections stay open |

---

## Retry & Backoff Configuration

Exponential backoff and retry configuration for resilience.

### Bridge Client

| Setting | Value | Location | Description |
|---------|-------|----------|-------------|
| **Base Delay** | `1.0 seconds` | [pkg/bridge/client.go:53](../pkg/bridge/client.go#L53) | Initial retry delay |
| **Max Delay** | `10 seconds` | [pkg/bridge/client.go:56](../pkg/bridge/client.go#L56) | Maximum retry delay |

### Registry Connection

| Setting | Value | Location | Description |
|---------|-------|----------|-------------|
| **Connection Timeout** | `30 seconds` | [pkg/registry/connection.go:18](../pkg/registry/connection.go#L18) | Initial connection timeout |
| **Backoff Base Delay** | `1.0 seconds` | [pkg/registry/connection.go:19](../pkg/registry/connection.go#L19) | Base delay for exponential backoff |
| **Backoff Max Delay** | `30 seconds` | [pkg/registry/connection.go:22](../pkg/registry/connection.go#L22) | Maximum backoff delay |
| **Initial Backoff** | `500 milliseconds` | [pkg/registry/connection.go:38](../pkg/registry/connection.go#L38) | Fast initial retry |
| **Max Backoff** | `15 seconds` | [pkg/registry/connection.go:39](../pkg/registry/connection.go#L39) | Cap at 15 seconds |

### Control-Plane Client

| Setting | Value | Location | Description |
|---------|-------|----------|-------------|
| **Initial Backoff** | `1 second` | [pkg/registry/control_plane_client.go:224](../pkg/registry/control_plane_client.go#L224) | Initial retry delay |
| **Max Backoff** | `10 seconds` | [pkg/registry/control_plane_client.go:225](../pkg/registry/control_plane_client.go#L225) | Maximum retry delay |

---

## HTTP Server Configuration

HTTP server timeout settings for Gin framework.

| Setting | Value | Location | Description |
|---------|-------|----------|-------------|
| **Read Header Timeout** | `5 seconds` | [pkg/httpserver/httpserver.go:35](../pkg/httpserver/httpserver.go#L35) | Time to read request headers |
| **Read Timeout** | `30 seconds` | [pkg/httpserver/httpserver.go:36](../pkg/httpserver/httpserver.go#L36) | Total time to read request |
| **Write Timeout** | `45 seconds` | [pkg/httpserver/httpserver.go:37](../pkg/httpserver/httpserver.go#L37) | Total time to write response |
| **Idle Timeout** | `60 seconds` | [pkg/httpserver/httpserver.go:38](../pkg/httpserver/httpserver.go#L38) | Keep-alive connection timeout |

**Note:** Write timeout (45s) > HTTP forward timeout (25s) to prevent premature connection closure.

---

## Worker & Job Management

Async worker and job processing timing configuration.

| Component | Interval | Location | Description |
|-----------|----------|----------|-------------|
| **Worker Poll Interval** | `2 seconds` | [pkg/async/worker/pool.go:70](../pkg/async/worker/pool.go#L70) | How often workers poll for jobs |
| **Pool Shutdown Timeout** | `30 seconds` | [pkg/async/worker/pool.go:144](../pkg/async/worker/pool.go#L144) | Max wait time for graceful shutdown |
| **Audit Batch Interval** | `5 seconds` | [pkg/audit/service.go:76](../pkg/audit/service.go#L76) | How often audit logs are flushed |

---

## Miscellaneous

Other timeout and interval configurations.

| Component | Value | Location | Description |
|-----------|-------|----------|-------------|
| **AI API Timeout** | `120 seconds` | [pkg/ai/openrouter.go:108](../pkg/ai/openrouter.go#L108) | OpenRouter AI API timeout |
| **OpenStack API Timeout** | `45 seconds` | [controller/cloud/openstack/client.go:167](../controller/cloud/openstack/client.go#L167) | OpenStack API call timeout |
| **Token Expiry Buffer** | `-5 minutes` | [controller/cloud/openstack/client.go:282](../controller/cloud/openstack/client.go#L282) | Refresh tokens 5min before expiry |
| **Version Fetch Timeout** | `30 seconds` | [controller/crud/custom/available_versions.go:33](../controller/crud/custom/available_versions.go#L33) | Available versions fetch timeout |
| **Default Token Expiry** | `15 minutes` | [pkg/helper/tools.go:32](../pkg/helper/tools.go#L32) | Default JWT token expiry |
| **Remember Me Expiry** | `7 days` | [pkg/helper/tools.go:42](../pkg/helper/tools.go#L42) | Long-lived token expiry (604800 seconds) |
| **Registry Sleep** | `30 seconds` | [registry/server/control-plane.go:274](../registry/server/control-plane.go#L274) | Sleep on registry timeout |
| **Controller Sleep** | `30 seconds` | [registry/server/controller.go:368](../registry/server/controller.go#L368) | Sleep on controller timeout |
| **Controller Startup Sleep** | `2 seconds` | [cmd/controller.go:129](../cmd/controller.go#L129) | Wait before starting HTTP server |
| **Client Retry Sleep** | `100 milliseconds` | [controller/client/services/clients.go:533](../controller/client/services/clients.go#L533) | Short sleep between retries |
| **gRPC Server Sleep** | `1 second` | [controller/client/grpc/server.go:241](../controller/client/grpc/server.go#L241) | Sleep on gRPC accept error |

---

## Best Practices

### When Modifying Time Values

1. **Maintain Ratios**: Keep safety margins (e.g., stale threshold should be > 2x heartbeat interval)
2. **Consider Network Latency**: Add buffers for network delays
3. **Test Failure Scenarios**: Verify detection works when components crash
4. **Document Changes**: Update this file when changing time values
5. **Check Dependencies**: Some values depend on others (e.g., write timeout > forward timeout)

### Common Patterns

- **Heartbeat**: 15-30 seconds for most services
- **Cleanup**: 5 minutes for periodic maintenance
- **Stale Detection**: 2-10 minutes based on criticality
- **Keepalive**: 30-60 seconds with 10-20 second timeouts
- **Command Timeouts**: 15-120 seconds based on operation complexity
