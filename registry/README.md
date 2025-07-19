# Elchi Registry Service

Elchi Registry Service is an advanced service discovery and routing service for distributed controller architecture. It integrates with Envoy proxy to provide dynamic routing capabilities.

## Features

- **Controller Registry**: Registration and address sharing of controllers
- **Client Location Tracking**: Tracking which controller clients are connected to
- **Control-Plane Routing**: Routing Envoy xDS requests to control-planes with version-based routing
- **External Processing**: Real-time request routing integration with Envoy ext_proc protocol
- **In-Memory Storage**: In-memory data storage for high performance
- **gRPC API**: Service only over gRPC

## Services

### 1. Registry Service
Existing controller registry service - manages controllers and client locations.

### 2. Routing Service  
Envoy routing service - routes xDS requests from Envoy to control-planes.

### 3. External Processor Service ⭐ NEW
Envoy ext_proc service - integrates with Envoy for real-time HTTP request routing.

## Architecture

```
┌─────────────────┐
│   Load Balancer │
└─────────────────┘
          │
    ┌─────┴─────┐
    │           │
    ▼           ▼
┌──────────┐ ┌──────────┐ ┌──────────┐
│Controller│ │Controller│ │Controller│
│    1     │ │    2     │ │    3     │
└──────────┘ └──────────┘ └──────────┘
    │           │           │
    ▼           ▼           ▼
┌─────────┐ ┌─────────┐ ┌─────────┐
│Client-A │ │Client-B │ │Client-C │
│Client-D │ │Client-E │ │Client-F │
└─────────┘ └─────────┘ └─────────┘
                  │
                  ▼
        ┌─────────────────┐
        │ Registry Service│
        │   (Port 50051)  │
        │                 │
        │ - Controller    │
        │   Registry      │
        │ - Client        │
        │   Location      │
        └─────────────────┘

┌─────────────────┐
│     Envoy       │
│   (xDS Client)  │
└─────────────────┘
         │
         ▼
┌─────────────────┐
│ Elchi Registry  │
│   (Port 50051)  │
│                 │
│ - Registry      │
│   Service       │
│ - Routing       │
│   Service       │
│ - External      │
│   Processor     │
└─────────────────┘
         │
         ▼
┌──────────┐ ┌──────────┐ ┌──────────┐
│Control-  │ │Control-  │ │Control-  │
│Plane-1   │ │Plane-2   │ │Plane-3   │
│(v1.33.5) │ │(v1.33.5) │ │(v1.34.3) │
└──────────┘ └──────────┘ └──────────┘
```

## Envoy ext_proc Integration

### Flow
1. **Envoy ADS Request** → Goes to Control Plane
2. **Control Plane** → Middleware Envoy returns response  
3. **Middleware Envoy** → Comes to our service via ext_proc
4. **External Processor** → Parses ADS metadata:
   - `nodeid`: "deney::683b2148ff7e3ae67d825cfa::10.10.20.51"
   - `envoy-version`: "v1.33.5"
5. **Routing Decision** → Two priority levels:
   - **Priority 1**: Is this nodeID already mapped to a control-plane?
   - **Priority 2**: Is there an available control-plane for this version?
6. **Response** → Returns with `x-target-cluster` header

### ext_proc Response Format
```json
{
  "response": {
    "requestHeaders": {
      "response": {
        "status": "CONTINUE",
        "headerMutation": {
          "setHeaders": [
            {
              "header": {
                "key": "x-target-cluster",
                "value": "elchi-control-plane-v1.33.5"
              }
            },
            {
              "header": {
                "key": "x-routing-service", 
                "value": "elchi-registry"
              }
            }
          ]
        }
      }
    }
  }
}
```

## Running

```bash
# Run all services on single port (gRPC: 50051)
go run cmd/main.go

# Run with custom config
go run cmd/main.go --config=config/config.yaml

# Run as binary
./elchi-registry --config=config/config.yaml

# Version info
./elchi-registry --version
```

## Configuration

### config.yaml
```yaml
server:
  port: 50051
  timeout: 30s

logging:
  level: info  # debug, info, warn, error
  format: text # json, text
  output: stdout # stdout, file
  file_path: logs/elchi-registry.log
  max_size: 100      # MB
  max_backups: 3
  max_age: 28        # days
  compress: true
```

### Environment Variables
```bash
GRPC_PORT=50051
GRPC_TIMEOUT=30s
LOG_LEVEL=info
LOG_FORMAT=text
LOG_OUTPUT=stdout
LOG_FILE_PATH=logs/elchi-registry.log
```

## gRPC API

### Registry Service API
Registry service provides 3 basic operations:

#### 1. RegisterController
Register controller:
```protobuf
message ControllerInfo {
    string controller_id = 1;
    string grpc_address = 2;
}

rpc RegisterController(ControllerInfo) returns (ControllerResponse);
```

#### 2. GetClientLocation  
Find which controller a client is on:
```protobuf
message ClientLocationRequest {
    string client_id = 1;
}

message ClientLocationResponse {
    bool found = 1;
    string controller_id = 2;
    string controller_fqdn = 3;
}

rpc GetClientLocation(ClientLocationRequest) returns (ClientLocationResponse);
```

### Routing Service API
Routing service provides 5 basic operations:

#### 1. RegisterControlPlane
Register control-plane:
```protobuf
message RegisterControlPlaneRequest {
    string control_plane_id = 1;
    string cluster_name = 2;     // control-plane-v1-33-2
    string version = 3;          // v1.33.5
    string grpc_address = 4;     // control-plane-v1-33-2:50051
}

rpc RegisterControlPlane(RegisterControlPlaneRequest) returns (RegisterControlPlaneResponse);
```

#### 2. GetControlPlaneCluster
Routing request from Envoy:
```protobuf
message GetControlPlaneClusterRequest {
    string node_id = 1;          // deney::683b2148ff7e3ae67d825cfa::10.10.20.51
    string version = 2;          // v1.33.5
}

message GetControlPlaneClusterResponse {
    bool found = 1;
    string cluster_name = 2;     // control-plane-v1-33-2
    string grpc_address = 3;     // control-plane-v1-33-2:50051
    string control_plane_id = 4; // cp-v1-33-2-abc123
}

rpc GetControlPlaneCluster(GetControlPlaneClusterRequest) returns (GetControlPlaneClusterResponse);
```

#### 3. NotifySnapshotDelivered
Snapshot delivery notification:
```protobuf
message NotifySnapshotDeliveredRequest {
    string control_plane_id = 1;
    string node_id = 2;
    string version = 3;
}

rpc NotifySnapshotDelivered(NotifySnapshotDeliveredRequest) returns (NotifySnapshotDeliveredResponse);
```

#### 4. UpdateNodeList
Bulk node list update (every 30 seconds):
```protobuf
message UpdateNodeListRequest {
    string control_plane_id = 1;
    repeated NodeInfo nodes = 2;
}

rpc UpdateNodeList(UpdateNodeListRequest) returns (UpdateNodeListResponse);
```

#### 5. HealthCheck
Health check:
```protobuf
message HealthCheckRequest {
    string service = 1;
}

message HealthCheckResponse {
    bool healthy = 1;
    string message = 2;
    int64 timestamp = 3;
}

rpc HealthCheck(HealthCheckRequest) returns (HealthCheckResponse);
```

### External Processor API
Integrates with Envoy ext_proc protocol:

#### Process (Bidirectional Stream)
```protobuf
rpc Process(stream ProcessingRequest) returns (stream ProcessingResponse);
```

**Supported Processing Steps:**
1. **Request Headers** - Process HTTP request headers
2. **Request Body** - Process HTTP request body  
3. **Request Trailers** - Process HTTP request trailers
4. **Response Headers** - Process HTTP response headers
5. **Response Body** - Process HTTP response body
6. **Response Trailers** - Process HTTP response trailers

## Usage Scenarios

### Registry Service
1. **Controller Startup**: Controller registers itself to registry when starting up
2. **Client Connection**: When a client connects to a controller, the controller notifies the registry
3. **Request Routing**: 
   - Request comes to controller-A
   - Controller-A asks registry with client_id
   - Registry returns which controller it's on
   - Controller-A connects to that address via gRPC and forwards the request

### Routing Service
1. **Control-Plane Registration**: Control-plane registers itself to routing service when starting up
2. **Envoy Request**: Routing request comes from Envoy with `node_id` + `version`
3. **Routing Decision**: Service finds appropriate control-plane and returns cluster name
4. **Snapshot Delivery**: Control-plane notifies registry after delivering snapshot
5. **Bulk Health Check**: Every 30 seconds, control-planes send their node lists

### External Processor Service ⭐
1. **Envoy ext_proc**: Envoy consults our service for every HTTP request
2. **ADS Metadata Parsing**: `nodeid` and `envoy-version` are extracted from request headers
3. **Routing Decision**: Appropriate control-plane cluster is determined
4. **Header Injection**: `x-target-cluster` header is added to response
5. **Request Routing**: Envoy routes the request based on this header

## Routing Algorithm

### Exact Version Matching ⭐
- **Only exact version matching** is performed
- `v1.33.5` request → only control-plane with version `v1.33.5`
- Prefix matching (`v1.33` → `v1.33.5`) **NOT SUPPORTED**
- Major version matching (`v1` → `v1.33.5`) **NOT SUPPORTED**

### Routing Priorities
1. **Priority 1**: Is this nodeID already mapped to a control-plane?
2. **Priority 2**: Is there an available control-plane for this version?
3. **Load Balance**: If multiple control-planes exist for same version, random selection

### ext_proc Response Logic
- **If suitable cluster found**: Returns cluster name with `x-target-cluster` header
- **If suitable cluster not found**: No header added, only `CONTINUE` status returned
- **If no header added**: Envoy doesn't change existing routing, request is not forwarded upstream

## Example Code

### Registry Service
```go
// Controller registration
client := grpc.Dial("localhost:50051")
registry := NewRegistryClient(client)

// Register itself
_, err := registry.RegisterController(ctx, &ControllerInfo{
    ControllerID: "ctrl-001",
    GRPCAddress:  "controller1.example.com:50051",
})

// Set client location
registry.SetClientLocation("client-123", "ctrl-001")

// Query client location
resp, err := registry.GetClientLocation(ctx, &ClientLocationRequest{
    ClientID: "client-123",
})
if resp.Found {
    // Forward command to resp.ControllerFQDN
}
```

### Routing Service
```go
// Control-plane registration
client := grpc.Dial("localhost:50051")
routing := NewRoutingClient(client)

// Register itself
_, err := routing.RegisterControlPlane(ctx, &RegisterControlPlaneRequest{
    ControlPlaneId: "cp-v1-33-2-abc123",
    ClusterName:    "control-plane-v1-33-2",
    Version:        "v1.33.5",
    GrpcAddress:    "control-plane-v1-33-2:50051",
})

// Envoy routing request
resp, err := routing.GetControlPlaneCluster(ctx, &GetControlPlaneClusterRequest{
    NodeId:  "deney::683b2148ff7e3ae67d825cfa::10.10.20.51",
    Version: "v1.33.5",
})
if resp.Found {
    // Route to resp.ClusterName
}

// Snapshot delivered notification
routing.NotifySnapshotDelivered(ctx, &NotifySnapshotDeliveredRequest{
    ControlPlaneId: "cp-v1-33-2-abc123",
    NodeId:         "deney::683b2148ff7e3ae67d825cfa::10.10.20.51",
    Version:        "v1.33.5",
})
```

## Envoy Configuration

### ext_proc Filter Configuration
```yaml
http_filters:
  - name: envoy.filters.http.ext_proc
    typed_config:
      "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor
      grpc_service:
        envoy_grpc:
          cluster_name: elchi-registry
      processing_mode:
        request_header_mode: SEND
        response_header_mode: SKIP
        request_body_mode: SKIP
        response_body_mode: SKIP
        request_trailer_mode: SKIP
        response_trailer_mode: SKIP
      failure_mode_allow: false
      message_timeout: 1s
```

### Cluster Configuration
```yaml
clusters:
  - name: elchi-registry
    type: STRICT_DNS
    lb_policy: ROUND_ROBIN
    load_assignment:
      cluster_name: elchi-registry
      endpoints:
        - lb_endpoints:
          - endpoint:
              address:
                socket_address:
                  address: elchi-registry
                  port_value: 50051
```

### Header-based Routing (Optional)
```yaml
http_filters:
  - name: envoy.filters.http.router
    typed_config:
      "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
      dynamic_stats: true
      start_child_span: true
      upstream_log:
        name: envoy.access_loggers.file
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.access_loggers.file.v3.FileAccessLog
          path: /dev/stdout
          format: |
            [%START_TIME%] "%REQ(:METHOD)% %REQ(X-ENVOY-ORIGINAL-PATH?:PATH)% %PROTOCOL%"
            %RESPONSE_CODE% %RESPONSE_FLAGS% %BYTES_RECEIVED% %BYTES_SENT%
            %DURATION% %RESP(X-ENVOY-UPSTREAM-SERVICE-TIME)% "%REQ(X-FORWARDED-FOR)%"
            "%REQ(USER-AGENT)%" "%REQ(X-REQUEST-ID)%" "%REQ(:AUTHORITY)%" "%UPSTREAM_HOST%"
            "%REQ(X-TARGET-CLUSTER)%"
```

## Header Constants

```go
const (
    HeaderNodeID         = "nodeid"
    HeaderVersion        = "envoy-version"
    HeaderTargetCluster  = "x-target-cluster"
    HeaderRoutingService = "x-routing-service"
)
```

## Development

### Build
```bash
go build -o elchi-registry cmd/main.go
```

### Test
```bash
go test ./...
```

### Run with Docker
```bash
docker build -t elchi-registry .
docker run -p 50051:50051 elchi-registry
``` 