# GSLB API Reference

Complete API documentation for Elchi GSLB (Global Server Load Balancing) system.

**Version**: 2.3
**Last Updated**: 2026-01-12

---

## What's New in Version 2.3

### Advanced GSLB List Filtering (2026-01-12)
- Added `probe_type` filter to List GSLB Records endpoint (http/https/tcp)
- Added `probe_interval` filter to List GSLB Records endpoint (10/20/30/60/90/120/180/300)
- Added `ttl` filter to List GSLB Records endpoint (1-86400)
- Allows filtering records by probe configuration and DNS settings
- All filters can be combined for precise record queries
- Examples: Find all HTTPS records with 30s interval, or all records with 60s TTL

### IP Status History Management (2026-01-12)
- Added `DELETE /api/v3/gslb/ip/:id/history` endpoint to clear IP status history
- Allows administrators to clean up accumulated historical data
- Useful for database optimization and fresh starts after configuration changes
- Irreversible operation - permanently removes historical probe results
- Current health state and monitoring continues unaffected

---

## What's New in Version 2.2

### IP Creation Source Tracking (2026-01-11)
- Added `is_manual` field to IP health records
- Distinguishes between manually added IPs (`true`) and auto-generated IPs from service deployments (`false`)
- Helps identify which IPs are managed by administrators vs automated systems
- Available in all IP health API responses

### Flexible IP Management for All Records (2026-01-11)
- Manual IPs can now be added to both manual AND auto-created GSLB records via API
- Manual IPs can be removed from any record (only IPs with `is_manual: true`)
- Health state can be manually updated for both manual and auto-created records
- Auto-generated IPs (from service deployments) are protected from deletion but can have their health state overridden
- Enables hybrid management: automated service deployment + manual backup/external IPs

### Per-Record Failover Zones (2026-01-11)
- Settings now support multiple failover zones array (`failover_zones`)
- Each GSLB record can have its own failover zone (`failover_zone` field)
- Default failover zone is the first one in settings array
- DNS API returns per-record failover FQDN in response
- DNS API endpoint changed from `/api/v3/dns/snapshot?project=X` to `/dns/snapshot?zone=X`
- DNS response format updated to match CoreDNS plugin expectations:
  - `version` → `version_hash`
  - `fqdn` → `name`
  - Added `enabled` and `failover` fields to each record
- Optimized string operations for better performance

---

## What's New in Version 2.1

### Error Message Tracking (2026-01-10)
- Added `error_message` field to `status_history` entries
- Captures detailed error information for failed health probes
- Helps troubleshoot connectivity issues, DNS failures, TLS errors, and more
- Available in all IP health responses and status history

### Performance Optimizations (2026-01-10)
- MongoDB query optimization: 96% faster health check cycles (13s → 0.6s)
- Probe latency metrics: avg, min, max tracking
- Enhanced error categorization: 21+ error types

---

## Table of Contents

1. [Settings API](#settings-api)
   - [Get GSLB Settings](#get-gslb-settings)
   - [Update GSLB Settings](#update-gslb-settings)
   - [Get GSLB Failover Zones](#get-gslb-failover-zones) ← NEW
2. [GSLB Records API](#gslb-records-api)
   - [List GSLB Records](#list-gslb-records)
   - [Get GSLB Record](#get-gslb-record)
   - [Create GSLB Record](#create-gslb-record)
   - [Update GSLB Record](#update-gslb-record)
   - [Bulk Update GSLB Records](#bulk-update-gslb-records) ← NEW
   - [Delete GSLB Record](#delete-gslb-record)
3. [IP Management API](#ip-management-api)
   - [Add IP to GSLB Record](#add-ip-to-gslb-record)
   - [Update IP Health State](#update-ip-health-state)
   - [Remove IP from GSLB Record](#remove-ip-from-gslb-record)
   - [List IPs for GSLB Record](#list-ips-for-gslb-record)
   - [Clear IP Status History](#clear-ip-status-history) ← NEW
4. [DNS API](#dns-api)

---

## Authentication

All API endpoints (except DNS API) require JWT authentication:

```http
Authorization: Bearer <jwt_token>
```

DNS API endpoints use secret-based authentication:

```http
X-Elchi-DNS-Secret: <dns_secret>
```

---

## Settings API

### Get GSLB Settings

Get current GSLB configuration.

**Endpoint**: `GET /api/v3/setting/gslb`

**Authorization**: All authenticated users

**Request**:
```http
GET /api/v3/setting/gslb HTTP/1.1
Host: controller.example.com
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
```

**Success Response** (200 OK):
```json
{
  "enabled": true,
  "zone": "global.example.com",
  "failover_zones": ["backup.example.com", "backup2.example.com"],
  "dns_secret": "my-super-secret-key-xyz",
  "default_ttl": 60
}
```

**Error Response** (404 Not Found):
```json
{
  "error": "GSLB settings not found"
}
```

---

### Update GSLB Settings

Update or create GSLB configuration.

**Endpoint**: `PUT /api/v3/setting/gslb`

**Authorization**: Admin/Owner only

**Request**:
```http
PUT /api/v3/setting/gslb HTTP/1.1
Host: controller.example.com
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
Content-Type: application/json

{
  "enabled": true,
  "zone": "global.example.com",
  "failover_zones": ["backup.example.com", "backup2.example.com"],
  "dns_secret": "my-super-secret-key-xyz",
  "default_ttl": 60
}
```

**Request Fields**:
- `enabled`: boolean (required) - Enable/disable GSLB system
- `zone`: string (required) - Primary DNS zone
- `failover_zones`: array of strings (optional) - Backup DNS zones (first one is default)
- `dns_secret`: string (required) - Secret for CoreDNS authentication
- `default_ttl`: integer (required) - Default DNS TTL in seconds (1-86400)

**Success Response** (200 OK):
```json
{
  "message": "GSLB settings updated successfully"
}
```

**Error Responses**:

400 Bad Request:
```json
{
  "error": "Invalid input: default_ttl must be between 1 and 86400"
}
```

403 Forbidden:
```json
{
  "error": "Only Admin and Owner can update GSLB settings"
}
```

---

### Get GSLB Failover Zones

Get the list of configured failover zones from GSLB settings.

**Endpoint**: `GET /api/v3/setting/gslb/failover-zones`

**Authorization**: All authenticated users

**Request**:
```http
GET /api/v3/setting/gslb/failover-zones?project=68ac8add4d6ae9208b24492b HTTP/1.1
Host: controller.example.com
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
```

**Query Parameters**:
- `project` (required): Project ID

**Success Response** (200 OK):
```json
{
  "failover_zones": [
    "backup.example.com",
    "backup2.example.com"
  ],
  "project": "68ac8add4d6ae9208b24492b"
}
```

**Success Response when GSLB not configured** (200 OK):
```json
{
  "failover_zones": [],
  "project": "68ac8add4d6ae9208b24492b",
  "message": "No GSLB configuration found for this project"
}
```

**Error Response** (400 Bad Request):
```json
{
  "message": "project parameter is required"
}
```

**Error Response** (500 Internal Server Error):
```json
{
  "message": "Failed to get GSLB config"
}
```

**Notes**:
- Returns an empty array if GSLB is not configured for the project
- Returns an empty array if no failover zones are configured
- The first zone in the array is the default failover zone used for auto-created records
- This endpoint is useful for populating dropdowns in the UI for failover zone selection

---

## GSLB Records API

### List GSLB Records

Get all GSLB records for a project with IP statistics (paginated).

**Endpoint**: `GET /api/v3/gslb`

**Query Parameters**:
- `project` (required): Project ID (ObjectID)
- `page` (optional): Page number (default: 1)
- `limit` (optional): Records per page (default: 10, max: 100)
- `search` (optional): Search in FQDN (case-insensitive)
- `status` (optional): Filter by status ("enabled" or "disabled")
- `probe_type` (optional): Filter by probe type ("http", "https", or "tcp")
- `probe_interval` (optional): Filter by probe interval (10, 20, 30, 60, 90, 120, 180, 300)
- `ttl` (optional): Filter by TTL value (1-86400)

**Authorization**: All authenticated users

**Request Examples**:
```http
# Basic list
GET /api/v3/gslb?project=68ac8add4d6ae9208b24492b&page=1&limit=10 HTTP/1.1

# Search by FQDN
GET /api/v3/gslb?project=68ac8add4d6ae9208b24492b&search=api HTTP/1.1

# Filter by status
GET /api/v3/gslb?project=68ac8add4d6ae9208b24492b&status=enabled HTTP/1.1

# Filter by probe type
GET /api/v3/gslb?project=68ac8add4d6ae9208b24492b&probe_type=https HTTP/1.1

# Filter by probe interval
GET /api/v3/gslb?project=68ac8add4d6ae9208b24492b&probe_interval=30 HTTP/1.1

# Filter by TTL
GET /api/v3/gslb?project=68ac8add4d6ae9208b24492b&ttl=60 HTTP/1.1

# Combined filters
GET /api/v3/gslb?project=68ac8add4d6ae9208b24492b&status=enabled&probe_type=https&probe_interval=30&ttl=60 HTTP/1.1

Host: controller.example.com
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
```

**Success Response** (200 OK):
```json
{
  "records": [
    {
      "id": "695d55824e4be7259940cb93",
      "fqdn": "api.global.example.com.",
      "service_id": "",
      "project": "68ac8add4d6ae9208b24492b",
      "version": "v1",
      "zone": "global.example.com",
      "failover_zone": "backup.example.com",
      "shard_id": 42,
      "enabled": true,
      "ttl": 120,
      "total_ips": 3,
      "healthy_ips": 2,
      "unhealthy_ips": 1,
      "probe": {
        "type": "https",
        "port": 443,
        "path": "/health",
        "host_header": "api.example.com",
        "interval": 30,
        "timeout": 2.5,
        "warning_threshold": 2,
        "critical_threshold": 3,
        "expected_status_codes": ["200-299"]
      },
      "created_at": "2026-01-06T10:00:00Z",
      "updated_at": "2026-01-06T10:00:00Z",
      "created_by": "user-123"
    }
  ],
  "count": 1,
  "page": 1,
  "limit": 10,
  "total_pages": 1
}
```

---

### Get GSLB Record

Get a single GSLB record by ID.

**Endpoint**: `GET /api/v3/gslb/:id`

**Path Parameters**:
- `id` (required): GSLB record ObjectID

**Authorization**: All authenticated users

**Request**:
```http
GET /api/v3/gslb/507f1f77bcf86cd799439011 HTTP/1.1
Host: controller.example.com
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
```

**Success Response** (200 OK):
```json
{
  "id": "507f1f77bcf86cd799439011",
  "fqdn": "api.global.example.com.",
  "service_id": "",
  "project": "production",
  "version": "v1",
  "zone": "global.example.com",
  "failover_zone": "backup.example.com",
  "shard_id": 42,
  "enabled": true,
  "ttl": 120,
  "probe": {
    "type": "https",
    "port": 443,
    "path": "/health",
    "host_header": "api.example.com",
    "interval": 30,
    "timeout": 2.5,
    "warning_threshold": 2,
    "critical_threshold": 3,
    "expected_status_codes": ["200-299"],
    "follow_redirects": true
  },
  "created_at": "2026-01-06T10:00:00Z",
  "updated_at": "2026-01-06T10:00:00Z",
  "created_by": "user-123"
}
```

**Error Response** (404 Not Found):
```json
{
  "error": "GSLB record not found"
}
```

---

### Create GSLB Record

Create a new GSLB record (manual records only).

**Endpoint**: `POST /api/v3/gslb`

**Authorization**: Admin/Owner only

**Request**:
```http
POST /api/v3/gslb HTTP/1.1
Host: controller.example.com
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
Content-Type: application/json

{
  "fqdn": "api.global.example.com",
  "project": "production",
  "version": "v1",
  "enabled": true,
  "ttl": 120,
  "failover_zone": "backup.example.com",
  "probe": {
    "type": "https",
    "port": 443,
    "path": "/health",
    "host_header": "api.example.com",
    "interval": 30,
    "timeout": 2.5,
    "warning_threshold": 2,
    "critical_threshold": 3,
    "expected_status_codes": ["200-299"]
  }
}
```

**Request Fields**:
- `fqdn`: string (required) - Fully qualified domain name
- `project`: string (required) - Project name
- `version`: string (required) - Version identifier
- `enabled`: boolean (required) - Enable/disable health checking
- `ttl`: integer (required) - DNS TTL in seconds (1-86400)
- `failover_zone`: string (optional) - Per-record failover zone (defaults to first zone in settings.failover_zones)
- `probe`: object (optional) - Health check configuration
  - `type`: string (required) - "http", "https", or "tcp"
  - `port`: integer (required) - Target port
  - `path`: string (optional) - HTTP/HTTPS path (default: "/")
  - `host_header`: string (optional) - HTTP Host header
  - `interval`: integer (required) - Probe interval in seconds (10, 20, 30, 60, 90, 120, 180, 300)
  - `timeout`: float (required) - Probe timeout in seconds (0.1-3.0)
  - `enabled`: boolean (optional) - Enable/disable probe execution (default: true, false = paused)
  - `warning_threshold`: integer (required) - Failures before warning state (1-10)
  - `critical_threshold`: integer (required) - Failures before critical state (2-20)
  - `expected_status_codes`: array of strings (optional) - Expected HTTP status codes (default: ["200-399"])
  - `follow_redirects`: boolean (optional) - HTTP/HTTPS only - Follow HTTP redirects (default: true)
  - `skip_ssl_verify`: boolean (optional) - HTTPS only - Skip SSL certificate verification (default: false, use true for self-signed certs)

**Success Response** (201 Created):
```json
{
  "id": "507f1f77bcf86cd799439011",
  "fqdn": "api.global.example.com.",
  "service_id": "",
  "project": "production",
  "version": "v1",
  "zone": "global.example.com",
  "failover_zone": "backup.example.com",
  "shard_id": 42,
  "enabled": true,
  "ttl": 120,
  "probe": {
    "type": "https",
    "port": 443,
    "path": "/health",
    "host_header": "api.example.com",
    "interval": 30,
    "timeout": 2.5,
    "warning_threshold": 2,
    "critical_threshold": 3,
    "expected_status_codes": ["200-299"],
    "follow_redirects": true
  },
  "created_at": "2026-01-06T10:00:00Z",
  "updated_at": "2026-01-06T10:00:00Z",
  "created_by": "user-123"
}
```

**Error Responses**:

400 Bad Request:
```json
{
  "error": "Invalid input: ttl must be between 1 and 86400"
}
```

409 Conflict:
```json
{
  "error": "GSLB record with FQDN 'api.global.example.com.' already exists"
}
```

---

### Update GSLB Record

Update an existing GSLB record (probe configuration, enabled status, TTL only).

**Endpoint**: `PUT /api/v3/gslb/:id`

**Path Parameters**:
- `id` (required): GSLB record ObjectID

**Authorization**: Admin/Owner only

**Request**:
```http
PUT /api/v3/gslb/507f1f77bcf86cd799439011 HTTP/1.1
Host: controller.example.com
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
Content-Type: application/json

{
  "enabled": false,
  "ttl": 60,
  "failover_zone": "backup2.example.com",
  "probe": {
    "type": "https",
    "port": 443,
    "path": "/healthz",
    "interval": 60,
    "timeout": 3.0,
    "warning_threshold": 3,
    "critical_threshold": 5,
    "expected_status_codes": ["200-299", "301"]
  }
}
```

**Request Fields**:
- `enabled`: boolean (optional) - Enable/disable health checking
- `ttl`: integer (optional) - DNS TTL in seconds (1-86400)
- `failover_zone`: string (optional) - Per-record failover zone
- `probe`: object or null (optional) - Health check configuration
  - Provide probe object: Updates probe configuration (same structure as create)
  - Provide `null`: Removes probe configuration entirely (deletes all probe settings)
  - Omit field: Keeps existing probe configuration unchanged

**Probe Management Examples**:

1. **Pause probe temporarily** (keeps configuration, stops probing):
```json
{
  "enabled": true,
  "ttl": 30,
  "probe": {
    "type": "https",
    "port": 443,
    "path": "/health",
    "interval": 30,
    "timeout": 2.5,
    "enabled": false,
    "warning_threshold": 2,
    "critical_threshold": 3
  }
}
```

2. **Resume probe** (re-enable with same config):
```json
{
  "enabled": true,
  "probe": {
    "type": "https",
    "port": 443,
    "path": "/health",
    "interval": 30,
    "timeout": 2.5,
    "enabled": true,
    "warning_threshold": 2,
    "critical_threshold": 3
  }
}
```

3. **Remove probe completely** (delete all probe settings):
```json
{
  "enabled": true,
  "ttl": 30,
  "probe": null
}
```

**Note**: When probe is updated or removed, all IP backoff timers are automatically reset to give IPs a fresh start.

**Success Response** (200 OK):
```json
{
  "message": "GSLB record updated successfully",
  "id": "507f1f77bcf86cd799439011"
}
```

**Error Responses**:

400 Bad Request:
```json
{
  "error": "Cannot update auto-created GSLB records. Delete the service instead."
}
```

404 Not Found:
```json
{
  "error": "GSLB record not found"
}
```

---

### Bulk Update GSLB Records

Update multiple GSLB records at once (enable/disable multiple records in a single operation).

**Endpoint**: `PUT /api/v3/gslb/batch`

**Authorization**: Admin/Owner only

**Request**:
```http
PUT /api/v3/gslb/batch HTTP/1.1
Host: controller.example.com
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
Content-Type: application/json

{
  "record_ids": [
    "507f1f77bcf86cd799439011",
    "507f1f77bcf86cd799439012",
    "507f1f77bcf86cd799439013"
  ],
  "enabled": false
}
```

**Request Fields**:
- `record_ids`: array of strings (required) - Array of GSLB record ObjectIDs (max 100 records)
- `enabled`: boolean (required) - Enable or disable all specified records

**Success Response** (200 OK):
```json
{
  "message": "Successfully disabled 3 GSLB records",
  "matched_count": 3,
  "modified_count": 3
}
```

**Response Fields**:
- `message`: Success message with action (enabled/disabled) and count
- `matched_count`: Number of records found with provided IDs
- `modified_count`: Number of records actually updated (may be less if some were already in target state)

**Error Responses**:

400 Bad Request (empty array):
```json
{
  "error": "record_ids cannot be empty"
}
```

400 Bad Request (too many records):
```json
{
  "error": "Cannot update more than 100 records at once"
}
```

400 Bad Request (invalid ID):
```json
{
  "error": "Invalid record ID: 507f1f77bcf86cd79943901X"
}
```

403 Forbidden:
```json
{
  "error": "Only Admin and Owner can bulk update GSLB records"
}
```

404 Not Found:
```json
{
  "error": "No GSLB records found with provided IDs"
}
```

**Use Cases**:

1. **Bulk Disable for Maintenance**:
```json
{
  "record_ids": ["id1", "id2", "id3", "id4", "id5"],
  "enabled": false
}
```

2. **Bulk Enable After Maintenance**:
```json
{
  "record_ids": ["id1", "id2", "id3", "id4", "id5"],
  "enabled": true
}
```

**Notes**:
- Updates all records in a single MongoDB operation for efficiency
- Triggers bucket reload to apply changes immediately
- All records must be valid ObjectIDs
- Maximum 100 records per request to prevent performance issues
- If any ID is invalid, entire operation fails (atomic validation)
- Works with both manual and auto-created records

---

### Delete GSLB Record

Delete a GSLB record and all associated IP health records.

**Endpoint**: `DELETE /api/v3/gslb/:id`

**Path Parameters**:
- `id` (required): GSLB record ObjectID

**Authorization**: Admin/Owner only

**Request**:
```http
DELETE /api/v3/gslb/507f1f77bcf86cd799439011 HTTP/1.1
Host: controller.example.com
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
```

**Success Response** (200 OK):
```json
{
  "message": "GSLB record deleted successfully",
  "deleted_ips": 3
}
```

**Error Responses**:

400 Bad Request:
```json
{
  "error": "Cannot delete auto-created GSLB records. Delete the service instead."
}
```

404 Not Found:
```json
{
  "error": "GSLB record not found"
}
```

---

## IP Management API

### Add IP to GSLB Record

Add an IP address to a GSLB record.

**Endpoint**: `POST /api/v3/gslb/:id/ips`

**Path Parameters**:
- `id` (required): GSLB record ObjectID

**Authorization**: Admin/Owner only

**Request**:
```http
POST /api/v3/gslb/507f1f77bcf86cd799439011/ips HTTP/1.1
Host: controller.example.com
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
Content-Type: application/json

{
  "ip": "203.0.113.15",
  "client_id": "external-lb-4",
  "health_state": "passing"
}
```

**Request Fields**:
- `ip`: string (required) - Valid IPv4 or IPv6 address
- `client_id`: string (optional) - Client identifier (can be empty string)
- `health_state`: string (optional) - Initial health state: "passing", "warning", or "critical" (default: "passing")

**Success Response** (200 OK):
```json
{
  "message": "IP added successfully to GSLB health collection",
  "ip": "203.0.113.15",
  "client_id": "external-lb-4",
  "shard": "42/3",
  "health_state": "passing",
  "fqdn": "api.global.example.com.",
  "is_manual": true,
  "created_at": "2026-01-06T15:30:00Z"
}
```

**Response Fields**:
- `message`: Success message
- `ip`: IP address that was added
- `client_id`: Client identifier (same as input, empty string if not provided)
- `shard`: Shard assignment (format: "shard_id/sub_shard_id")
- `health_state`: Health state string: "passing", "warning", or "critical"
- `fqdn`: Fully qualified domain name from parent GSLB record
- `is_manual`: Always `true` for manually added IPs via API
- `created_at`: Timestamp when IP was added

**Notes**:
- Manual IPs can be added to **both** manual records (service_id == "") and auto-created records (service_id != "")
- All IPs added via this API will have `is_manual: true`
- This allows adding external load balancers or backup IPs to auto-created service records

**Error Responses**:

409 Conflict:
```json
{
  "error": "IP 203.0.113.15 already exists in this GSLB record"
}
```

404 Not Found:
```json
{
  "error": "GSLB record not found"
}
```

---

### Update IP Health State

Manually update the health state of an IP address. Use this for manual drain operations, maintenance mode, or gradual health degradation scenarios.

**Endpoint**: `PUT /api/v3/gslb/:id/ips/:ip`

**Path Parameters**:
- `id` (required): GSLB record ObjectID
- `ip` (required): IP address (URL-encoded)

**Authorization**: Admin/Owner only

**Request**:
```http
PUT /api/v3/gslb/507f1f77bcf86cd799439011/ips/203.0.113.15 HTTP/1.1
Host: controller.example.com
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
Content-Type: application/json

{
  "health_state": "critical"
}
```

**Request Fields**:
- `health_state`: string (required) - Health state: "passing" (fully healthy), "warning" (degraded), or "critical" (down/removed from DNS)

**Success Response** (200 OK):
```json
{
  "message": "IP 203.0.113.15 health state updated successfully",
  "ip": "203.0.113.15",
  "health_state": "critical"
}
```

**Response Fields**:
- `message`: Success message
- `ip`: IP address that was updated
- `health_state`: Updated health state string: "passing", "warning", or "critical"

**Notes**:
- Manual health state updates are allowed for **both** manual records (service_id == "") and auto-created records (service_id != "")
- This allows administrators to override health checker decisions for maintenance, drain operations, or emergency situations
- The `manual_reset_at` timestamp will be set to track when the state was manually changed
- After manual update, the health checker will continue monitoring and may change the state again based on probe results

**Error Responses**:

404 Not Found:
```json
{
  "error": "IP 203.0.113.15 not found in this GSLB record"
}
```

---

### Remove IP from GSLB Record

Remove an IP address from a GSLB record.

**Endpoint**: `DELETE /api/v3/gslb/:id/ips/:ip`

**Path Parameters**:
- `id` (required): GSLB record ObjectID
- `ip` (required): IP address (URL-encoded)

**Authorization**: Admin/Owner only

**Request**:
```http
DELETE /api/v3/gslb/507f1f77bcf86cd799439011/ips/203.0.113.15 HTTP/1.1
Host: controller.example.com
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
```

**Success Response** (200 OK):
```json
{
  "message": "IP 203.0.113.15 removed successfully from GSLB health collection"
}
```

**Notes**:
- Only manually added IPs (`is_manual: true`) can be removed via this API
- Auto-generated IPs (`is_manual: false`) from service deployments are protected and can only be removed via undeploy operations
- This prevents accidental deletion of IPs managed by the automated deployment system

**Error Responses**:

400 Bad Request (trying to remove auto-generated IP):
```json
{
  "error": "Cannot remove auto-generated IPs. Only manually added IPs (is_manual=true) can be removed via API."
}
```

404 Not Found:
```json
{
  "error": "IP 203.0.113.15 not found in this GSLB record"
}
```

---

### List IPs for GSLB Record

Get all IP health records for a GSLB record.

**Endpoint**: `GET /api/v3/gslb/:id/ips`

**Path Parameters**:
- `id` (required): GSLB record ObjectID

**Authorization**: All authenticated users

**Request**:
```http
GET /api/v3/gslb/507f1f77bcf86cd799439011/ips HTTP/1.1
Host: controller.example.com
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
```

**Success Response** (200 OK):
```json
{
  "ips": [
    {
      "id": "507f1f77bcf86cd799439012",
      "record_id": "507f1f77bcf86cd799439011",
      "fqdn": "api.global.example.com.",
      "ip": "203.0.113.15",
      "client_id": "external-lb-4",
      "shard_id": 42,
      "sub_shard_id": 3,
      "health_state": "passing",
      "last_status_change": "2026-01-06T15:30:00Z",
      "backoff_until": "0001-01-01T00:00:00Z",
      "current_backoff": 0,
      "status_history": [
        {
          "state": "passing",
          "datetime": "2026-01-06T15:35:00Z",
          "response_code": 200,
          "response_time": 0.125,
          "error_message": ""
        },
        {
          "state": "critical",
          "datetime": "2026-01-06T15:30:00Z",
          "response_code": 0,
          "response_time": 0,
          "error_message": "dial tcp 203.0.113.15:443: connect: connection refused"
        }
      ],
      "is_manual": true,
      "created_at": "2026-01-06T15:30:00Z",
      "updated_at": "2026-01-06T15:35:00Z"
    }
  ]
}
```

---

### Clear IP Status History

Clear the status history for a specific IP health record.

**Endpoint**: `DELETE /api/v3/gslb/ip/:id/history`

**Path Parameters**:
- `id` (required): IP health document ObjectID (from gslb_ip_health collection)

**Authorization**: Admin/Owner only

**Request**:
```http
DELETE /api/v3/gslb/ip/507f1f77bcf86cd799439012/history HTTP/1.1
Host: controller.example.com
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
```

**Success Response** (200 OK):
```json
{
  "message": "IP status history cleared successfully",
  "id": "507f1f77bcf86cd799439012"
}
```

**Response Fields**:
- `message`: Success message
- `id`: IP health document ID that was cleared

**Notes**:
- Clears the entire `status_history` array for the specified IP health document
- Current health state (`health_state`, `last_status_change`) remains unchanged
- Only clears historical data, does not affect ongoing health monitoring
- Health checker will continue to add new history entries after clearing
- Useful for cleaning up test data or reducing database size
- **IMPORTANT**: This operation is irreversible - history data is permanently deleted

**Error Responses**:

400 Bad Request (invalid ID format):
```json
{
  "error": "Invalid IP health ID"
}
```

401 Unauthorized:
```json
{
  "error": "User not authenticated"
}
```

403 Forbidden:
```json
{
  "error": "Only Admin and Owner can clear IP history"
}
```

404 Not Found:
```json
{
  "error": "IP health record not found"
}
```

500 Internal Server Error:
```json
{
  "error": "Failed to clear IP history"
}
```

**Use Cases**:

1. **Clean up test data**:
   - After testing health probes, clear accumulated history

2. **Database optimization**:
   - Reduce document size by removing old history entries

3. **Fresh start after configuration changes**:
   - Clear history when probe settings are significantly changed

---

## DNS API

### Get DNS Snapshot

Get complete DNS snapshot for CoreDNS plugin.

**Endpoint**: `GET /dns/snapshot`

**Query Parameters**:
- `zone` (required): DNS zone

**Authentication**: DNS secret header

**Request**:
```http
GET /dns/snapshot?zone=global.example.com HTTP/1.1
Host: controller.example.com
X-Elchi-DNS-Secret: my-super-secret-key-xyz
```

**Success Response** (200 OK):
```json
{
  "zone": "global.example.com",
  "version_hash": "6c01cd9773b1e87f67aec142e5d60d2ce79ef5d26a4e2f9ccbf58bcfc9d7efb7",
  "records": [
    {
      "name": "api.global.example.com.",
      "type": "A",
      "ttl": 120,
      "ips": ["203.0.113.15", "203.0.113.16"],
      "enabled": true,
      "failover": "api.backup.example.com."
    },
    {
      "name": "web.global.example.com.",
      "type": "A",
      "ttl": 60,
      "ips": ["203.0.113.20"],
      "enabled": true,
      "failover": "web.backup.example.com."
    }
  ]
}
```

**Response Fields**:
- `zone`: DNS zone
- `version_hash`: SHA256 hash of sorted records (for caching)
- `records`: Array of DNS records
  - `name`: Fully qualified domain name
  - `type`: Record type (always "A" for now)
  - `ttl`: Time to live in seconds
  - `ips`: Array of healthy IP addresses (empty if all IPs are critical)
  - `enabled`: Record enabled status (always true in response, disabled records are filtered)
  - `failover`: Per-record failover FQDN (used when ips array is empty)

**Error Responses**:

401 Unauthorized:
```json
{
  "error": "Invalid DNS secret"
}
```

400 Bad Request:
```json
{
  "error": "zone parameter is required"
}
```

---

## Error Codes

| HTTP Status | Description |
|-------------|-------------|
| 200 | Success |
| 201 | Created |
| 400 | Bad Request - Invalid input |
| 401 | Unauthorized - Missing or invalid authentication |
| 403 | Forbidden - Insufficient permissions |
| 404 | Not Found - Resource not found |
| 409 | Conflict - Duplicate resource |
| 500 | Internal Server Error |

---

## Data Models

### GSLB Record

```json
{
  "id": "string (ObjectID)",
  "fqdn": "string (normalized with trailing dot)",
  "service_id": "string (empty for manual records)",
  "project": "string",
  "version": "string",
  "zone": "string",
  "failover_zone": "string (optional, per-record failover zone)",
  "shard_id": "integer (0-127)",
  "enabled": "boolean",
  "ttl": "integer (1-86400)",
  "probe": {
    "type": "string (http|https|tcp)",
    "port": "integer",
    "path": "string (HTTP/HTTPS only)",
    "host_header": "string (HTTP/HTTPS only)",
    "interval": "integer (10|20|30|60|90|120|180|300)",
    "timeout": "float (0.1-3.0)",
    "warning_threshold": "integer (1-10)",
    "critical_threshold": "integer (2-20)",
    "expected_status_codes": ["string array"],
    "follow_redirects": "boolean (HTTP/HTTPS only, default: true)",
    "skip_ssl_verify": "boolean (HTTPS only, default: false)"
  },
  "created_at": "string (ISO 8601)",
  "updated_at": "string (ISO 8601)",
  "created_by": "string"
}
```

### IP Health Record

```json
{
  "id": "string (ObjectID)",
  "record_id": "string (ObjectID)",
  "fqdn": "string",
  "ip": "string (IPv4 or IPv6)",
  "client_id": "string",
  "shard_id": "integer (0-127)",
  "sub_shard_id": "integer (0-7)",
  "health_state": "string (passing|warning|critical)",
  "last_status_change": "string (ISO 8601)",
  "backoff_until": "string (ISO 8601)",
  "current_backoff": "integer (seconds)",
  "status_history": [
    {
      "state": "string",
      "datetime": "string (ISO 8601)",
      "response_code": "integer",
      "response_time": "float (seconds)",
      "error_message": "string (optional, error details for failed probes)"
    }
  ],
  "is_manual": "boolean (true = manually added by admin, false = auto-generated from service deployment)",
  "created_at": "string (ISO 8601)",
  "updated_at": "string (ISO 8601)"
}
```

**Field Descriptions**:
- `is_manual`: Indicates the source of the IP record
  - `true`: IP was manually added by an administrator via API
  - `false`: IP was auto-generated when a client was deployed to a GSLB-enabled service

**Note**: The `healthy` field has been removed from the data model. Use `health_state` instead, which provides more granular information:
- `health_state = "passing"` or `"warning"` → IP is included in DNS responses
- `health_state = "critical"` → IP is excluded from DNS responses

### GSLB Settings

```json
{
  "enabled": "boolean",
  "zone": "string",
  "failover_zones": ["string array (first one is default)"],
  "dns_secret": "string",
  "default_ttl": "integer (1-86400)"
}
```

---

## Notes

### Auto-Created vs Manual Records

- **Auto-Created** (service_id != ""):
  - Created automatically when client deployed to service
  - IPs managed via deploy/undeploy operations (all IPs have `is_manual: false`)
  - Cannot be modified or deleted manually
  - Cannot add/remove IPs via API

- **Manual** (service_id == ""):
  - Created via API
  - IPs managed via IP Management API (all IPs have `is_manual: true`)
  - Can be updated and deleted via API
  - Full control over health state

### IP Creation Source (`is_manual` field)

The `is_manual` field indicates how an IP was added to the GSLB system:

- **`is_manual: false`** (Auto-Generated):
  - IP was automatically added when a client was deployed to a GSLB-enabled service
  - Created via service deployment operations
  - Managed by the system's automated service discovery

- **`is_manual: true`** (Manually Added):
  - IP was explicitly added by an administrator via API (`POST /api/v3/gslb/:id/ips`)
  - Requires Admin/Owner privileges to add
  - Can be used for external load balancers or IPs not managed by the deployment system

### Health States

GSLB uses a tri-state health model for gradual degradation:

- **passing**: 0-1 consecutive failures (default), fully healthy, included in DNS responses
- **warning**: 2 consecutive failures (configurable via `warning_threshold`), degraded but still included in DNS responses
- **critical**: 3+ consecutive failures (configurable via `critical_threshold`), unhealthy and **excluded** from DNS responses

**Health State Transitions**:
- `passing` → `warning`: When consecutive failures reach `warning_threshold`
- `warning` → `critical`: When consecutive failures reach `critical_threshold`
- `warning`/`critical` → `passing`: On first successful probe

**DNS Inclusion**:
- IPs with `health_state = "passing"` or `"warning"` are included in DNS responses
- IPs with `health_state = "critical"` are excluded from DNS responses

### Circuit Breaker

- Exponential backoff for critical state IPs
- Formula: `2^failures * interval` (max 3600 seconds)
- Manual health state changes clear backoff

---

**End of API Reference**
