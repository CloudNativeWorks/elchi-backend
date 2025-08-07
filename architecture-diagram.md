# Elchi Platform Complete Architecture with Gateway Envoy

## System Overview - Traffic Flow Through Gateway Envoy

```mermaid
graph TB
    subgraph "External Traffic"
        USER[Users/Browsers]
        ENVOY_CLIENT[Envoy Clients<br/>Microservices]
    end

    subgraph "Gateway Layer - Port 1989"
        GATEWAY[Gateway Envoy<br/>Port: 1989<br/>Traffic Router & Load Balancer]
    end

    subgraph "Registry Service - Port 9090"
        REG[Elchi-Registry<br/>ext_proc Service<br/>Dynamic Routing Decisions]
    end

    subgraph "Frontend - Port 80"
        UI[Elchi UI<br/>React Application]
    end

    subgraph "Controller Pods"
        CTRL1[Controller-v1-24-0<br/>REST: 8099<br/>gRPC: 50051]
        CTRL2[Controller-v1-24-1<br/>REST: 8099<br/>gRPC: 50051]
        CTRL3[Controller-v1-25-0<br/>REST: 8099<br/>gRPC: 50051]
    end

    subgraph "Control-Plane Pods"
        CP1[Control-Plane-v1-24-0<br/>xDS: 18000]
        CP2[Control-Plane-v1-24-1<br/>xDS: 18000]
        CP3[Control-Plane-v1-25-0<br/>xDS: 18000]
    end

    subgraph "Database"
        DB[MongoDB<br/>Replica Set]
    end

    subgraph "Monitoring"
        OTEL[OTEL Collector<br/>Port: 4317]
        VM[VictoriaMetrics<br/>Port: 8428]
    end

    USER -->|HTTP/HTTPS| GATEWAY
    ENVOY_CLIENT -->|xDS Requests| GATEWAY

    GATEWAY -->|ext_proc<br/>Header Processing| REG
    REG -->|Routing Decision<br/>x-target-cluster| GATEWAY

    GATEWAY -->|Default Route| UI
    GATEWAY -->|from-elchi header| CTRL1
    GATEWAY -->|from-elchi header| CTRL2
    GATEWAY -->|from-elchi header| CTRL3

    GATEWAY -->|x-target-cluster:<br/>controller-*| CTRL1
    GATEWAY -->|x-target-cluster:<br/>controller-*| CTRL2
    GATEWAY -->|x-target-cluster:<br/>controller-*| CTRL3

    GATEWAY -->|x-target-cluster:<br/>control-plane-*| CP1
    GATEWAY -->|x-target-cluster:<br/>control-plane-*| CP2
    GATEWAY -->|x-target-cluster:<br/>control-plane-*| CP3

    GATEWAY -->|/opentelemetry/*| OTEL
    GATEWAY -->|/api/v1/query_range| VM

    UI -->|API Calls| GATEWAY
    
    CTRL1 --> DB
    CTRL2 --> DB
    CTRL3 --> DB
    
    CP1 --> DB
    CP2 --> DB
    CP3 --> DB

    CTRL1 -.->|Register| REG
    CTRL2 -.->|Register| REG
    CTRL3 -.->|Register| REG

    CP1 -.->|Register| REG
    CP2 -.->|Register| REG
    CP3 -.->|Register| REG

    classDef gateway fill:#ffebee,stroke:#c62828,stroke-width:3px
    classDef registry fill:#f3e5f5,stroke:#4a148c,stroke-width:2px
    classDef frontend fill:#e1f5fe,stroke:#01579b,stroke-width:2px
    classDef controller fill:#fff3e0,stroke:#e65100,stroke-width:2px
    classDef controlplane fill:#e8f5e9,stroke:#1b5e20,stroke-width:2px
    classDef database fill:#fce4ec,stroke:#880e4f,stroke-width:2px
    classDef monitoring fill:#e0f7fa,stroke:#00838f,stroke-width:2px

    class GATEWAY gateway
    class REG registry
    class UI frontend
    class CTRL1,CTRL2,CTRL3 controller
    class CP1,CP2,CP3 controlplane
    class DB database
    class OTEL,VM monitoring
```

## Gateway Envoy Routing Logic

```mermaid
sequenceDiagram
    participant Client
    participant Gateway as Gateway Envoy<br/>Port 1989
    participant Registry as Registry<br/>ext_proc
    participant Target as Target Service

    Note over Gateway: Listener on 0.0.0.0:1989

    Client->>Gateway: HTTP Request
    Gateway->>Registry: ext_proc: Send Headers
    
    alt Envoy xDS Request
        Registry->>Registry: Detect Envoy client<br/>nodeid, version headers
        Registry->>Registry: Find compatible Control-Plane
        Registry->>Gateway: Add x-target-cluster header<br/>e.g. control-plane-v1-24-0
    else Controller gRPC Request
        Registry->>Registry: Detect gRPC client
        Registry->>Gateway: Add x-target-cluster header<br/>e.g. controller-v1-24-0
    else UI/API Request
        Registry->>Registry: Check from-elchi header
        Registry->>Gateway: Route to appropriate service
    else Default
        Registry->>Gateway: Route to Elchi UI
    end
    
    Gateway->>Gateway: Match route based on<br/>x-target-cluster header
    Gateway->>Target: Forward request
    Target->>Gateway: Response
    Gateway->>Client: Response
```

## Detailed Service Communication Flow

### 1. Envoy Client xDS Request Flow

```mermaid
graph LR
    subgraph "Envoy Client"
        ENV[Envoy v1.24<br/>Microservice Sidecar]
    end

    subgraph "Gateway Processing"
        GW[Gateway Envoy]
        REG[Registry ext_proc]
    end

    subgraph "Target"
        CP[Control-Plane-v1-24-0]
    end

    ENV -->|1. xDS Request<br/>Headers: nodeid, version| GW
    GW -->|2. ext_proc| REG
    REG -->|3. x-target-cluster:<br/>control-plane-v1-24-0| GW
    GW -->|4. Route to cluster| CP
    CP -->|5. xDS Response<br/>CDS, EDS, LDS, RDS| GW
    GW -->|6. Forward Response| ENV
```

### 2. UI to Backend Flow

```mermaid
graph LR
    subgraph "Browser"
        USER[User Browser]
    end

    subgraph "Gateway"
        GW[Gateway Envoy]
        REG[Registry]
    end

    subgraph "Services"
        UI[Elchi UI]
        CTRL[Controller REST API]
    end

    USER -->|1. https://elchi.domain| GW
    GW -->|2. Default Route| UI
    UI -->|3. API Call<br/>Header: from-elchi=yes| GW
    GW -->|4. ext_proc| REG
    REG -->|5. Route to controller| GW
    GW -->|6. Forward to REST:8099| CTRL
    CTRL -->|7. Response| GW
    GW -->|8. Response| UI
    UI -->|9. Render| USER
```

## Kubernetes Service Architecture

```mermaid
graph TB
    subgraph "Kubernetes Namespace"
        subgraph "Gateway Service"
            GW_SVC[envoy-gateway-svc<br/>Type: LoadBalancer<br/>Port: 1989]
            GW_POD[envoy-gateway-pod<br/>ConfigMap: envoy-config]
        end

        subgraph "Registry Service"
            REG_SVC[elchi-registry<br/>ClusterIP<br/>Port: 9090]
            REG_POD[registry-pod]
        end

        subgraph "Frontend Service"
            UI_SVC[elchi<br/>ClusterIP<br/>Port: 80]
            UI_POD[ui-pod-1]
            UI_POD2[ui-pod-2]
        end

        subgraph "Controller Services"
            CTRL_SVC[elchi-controller-*-headless<br/>Headless Service]
            CTRL_POD1[controller-v1-24-0]
            CTRL_POD2[controller-v1-24-1]
            CTRL_POD3[controller-v1-25-0]
        end

        subgraph "Control-Plane Services"
            CP_SVC[elchi-control-plane-*-headless<br/>Headless Service]
            CP_POD1[control-plane-v1-24-0]
            CP_POD2[control-plane-v1-24-1]
            CP_POD3[control-plane-v1-25-0]
        end

        subgraph "Data Services"
            DB_SVC[mongodb<br/>ClusterIP]
            DB_POD1[mongo-0]
            DB_POD2[mongo-1]
            DB_POD3[mongo-2]
        end

        subgraph "Monitoring Services"
            OTEL_SVC[otel-collector<br/>ClusterIP<br/>Port: 4317]
            VM_SVC[victoriametrics<br/>ClusterIP<br/>Port: 8428]
        end
    end

    GW_SVC --> GW_POD
    REG_SVC --> REG_POD
    UI_SVC --> UI_POD
    UI_SVC --> UI_POD2
    CTRL_SVC --> CTRL_POD1
    CTRL_SVC --> CTRL_POD2
    CTRL_SVC --> CTRL_POD3
    CP_SVC --> CP_POD1
    CP_SVC --> CP_POD2
    CP_SVC --> CP_POD3
    DB_SVC --> DB_POD1
    DB_SVC --> DB_POD2
    DB_SVC --> DB_POD3

    GW_POD -.->|Routes to| REG_POD
    GW_POD -.->|Routes to| UI_POD
    GW_POD -.->|Routes to| CTRL_POD1
    GW_POD -.->|Routes to| CP_POD1
    GW_POD -.->|Routes to| OTEL_SVC
    GW_POD -.->|Routes to| VM_SVC
```

## Registry ext_proc Decision Flow

```mermaid
flowchart TD
    START[Request Headers Received]
    
    START --> CHECK_NODEID{Has nodeid header?}
    
    CHECK_NODEID -->|Yes| ENVOY_PATH[Envoy Client Path]
    CHECK_NODEID -->|No| CHECK_FROM_ELCHI{Has from-elchi header?}
    
    ENVOY_PATH --> GET_VERSION[Extract Envoy Version]
    GET_VERSION --> FIND_CP[Find Compatible Control-Plane]
    FIND_CP --> SET_CP_TARGET[Set x-target-cluster:<br/>control-plane-v*-*]
    
    CHECK_FROM_ELCHI -->|Yes| UI_API_PATH[UI API Request]
    CHECK_FROM_ELCHI -->|No| CHECK_CLIENT{Has client-id header?}
    
    UI_API_PATH --> ROUTE_CONTROLLER[Route to Controller REST]
    
    CHECK_CLIENT -->|Yes| CLIENT_PATH[CLI Client Path]
    CHECK_CLIENT -->|No| DEFAULT_PATH[Default UI Path]
    
    CLIENT_PATH --> GET_CLIENT_TARGET[Get Target Controller]
    GET_CLIENT_TARGET --> SET_CTRL_TARGET[Set x-target-cluster:<br/>controller-v*-*]
    
    DEFAULT_PATH --> ROUTE_UI[Route to Elchi UI]
    
    SET_CP_TARGET --> RETURN[Return Modified Headers]
    ROUTE_CONTROLLER --> RETURN
    SET_CTRL_TARGET --> RETURN
    ROUTE_UI --> RETURN
```

## Gateway Envoy Clusters Configuration

```mermaid
graph TD
    subgraph "Gateway Envoy Clusters"
        subgraph "Service Discovery"
            REG_CLUSTER[registry-cluster<br/>STRICT_DNS<br/>elchi-registry:9090]
        end
        
        subgraph "Frontend"
            UI_CLUSTER[elchi-cluster<br/>STRICT_DNS<br/>elchi:80]
        end
        
        subgraph "Controller Clusters"
            CTRL_REST[controller-rest-cluster<br/>STRICT_DNS<br/>All Controllers:8099]
            CTRL_GRPC1[controller-grpc-cluster-v1-24-0<br/>LOGICAL_DNS<br/>Specific Pod:50051]
            CTRL_GRPC2[controller-grpc-cluster-v1-24-1<br/>LOGICAL_DNS<br/>Specific Pod:50051]
        end
        
        subgraph "Control-Plane Clusters"
            CP_CLUSTER1[control-plane-cluster-v1-24-0<br/>LOGICAL_DNS<br/>Specific Pod:18000]
            CP_CLUSTER2[control-plane-cluster-v1-24-1<br/>LOGICAL_DNS<br/>Specific Pod:18000]
        end
        
        subgraph "Monitoring Clusters"
            OTEL_CLUSTER[otel-cluster<br/>STRICT_DNS<br/>otel-collector:4317]
            VM_CLUSTER[victoriametrics-cluster<br/>STRICT_DNS<br/>victoriametrics:8428]
        end
    end
```

## Complete Request Types and Routing

| Request Type | Source | Headers | Target Cluster | Backend Service |
|-------------|--------|---------|----------------|-----------------|
| UI Access | Browser | - | elchi-cluster | Elchi UI React |
| UI API Call | Elchi UI | from-elchi=yes | controller-rest-cluster | Controller REST API |
| Envoy xDS | Envoy Proxy | nodeid, envoy-version | control-plane-cluster-* | Control-Plane xDS |
| CLI gRPC | CLI Client | client-id, x-target-cluster | controller-grpc-cluster-* | Controller gRPC |
| Metrics | OTEL Agent | - | otel-cluster | OTEL Collector |
| Queries | Prometheus/Grafana | - | victoriametrics-cluster | VictoriaMetrics |
| Health Check | K8s | - | Direct to Pods | TCP Check |

## High Availability and Scaling

```mermaid
graph TB
    subgraph "External Load Balancer"
        LB[Cloud LB / MetalLB<br/>Public IP]
    end

    subgraph "Gateway Envoy Pods"
        GW1[envoy-gateway-1<br/>Port: 1989]
        GW2[envoy-gateway-2<br/>Port: 1989]
        GW3[envoy-gateway-3<br/>Port: 1989]
    end

    subgraph "Registry HA"
        REG_PRIMARY[registry-primary]
        REG_BACKUP[registry-backup<br/>Standby]
    end

    subgraph "Multi-Version Support"
        subgraph "v1.24 Support"
            CP_24_1[control-plane-v1-24-0]
            CP_24_2[control-plane-v1-24-1]
            CTRL_24_1[controller-v1-24-0]
            CTRL_24_2[controller-v1-24-1]
        end
        
        subgraph "v1.25 Support"
            CP_25_1[control-plane-v1-25-0]
            CP_25_2[control-plane-v1-25-1]
            CTRL_25_1[controller-v1-25-0]
            CTRL_25_2[controller-v1-25-1]
        end
    end

    LB --> GW1
    LB --> GW2
    LB --> GW3

    GW1 --> REG_PRIMARY
    GW2 --> REG_PRIMARY
    GW3 --> REG_PRIMARY
    
    REG_PRIMARY -.->|Failover| REG_BACKUP

    style REG_BACKUP stroke-dasharray: 5 5
```

## Key Architecture Principles

### 1. **Gateway Envoy as Central Router**
- All traffic enters through Gateway Envoy on port 1989
- Handles HTTP/1.1, HTTP/2, and gRPC protocols
- Provides load balancing, health checking, and failover

### 2. **Registry as ext_proc Service**
- Processes all request headers via External Processing filter
- Makes intelligent routing decisions based on request metadata
- Maintains service discovery and routing tables
- No direct traffic handling, only routing decisions

### 3. **Version-Based Pod Naming**
- Pods named with version and instance: `service-v1-24-0`
- Enables precise routing to specific versions
- Supports rolling updates and canary deployments

### 4. **Headless Services for Direct Pod Access**
- Controllers and Control-Planes use headless services
- Gateway routes directly to specific pods
- Enables session affinity and version-specific routing

### 5. **Multi-Protocol Support**
- REST API on port 8099 - Controllers
- gRPC on port 50051 - Controllers  
- xDS gRPC on port 18000 - Control-Planes
- HTTP/2 with keepalive for long-lived connections

## Traffic Flow Summary

```mermaid
graph LR
    subgraph "Entry Point"
        TRAFFIC[All Traffic<br/>Port 1989]
    end

    subgraph "Processing"
        GATEWAY[Gateway Envoy]
        REGISTRY[Registry ext_proc]
    end

    subgraph "Destinations"
        UI[UI - Default]
        CTRL[Controllers - API/gRPC]
        CP[Control-Planes - xDS]
        MON[Monitoring - Metrics]
    end

    TRAFFIC --> GATEWAY
    GATEWAY <--> REGISTRY
    GATEWAY --> UI
    GATEWAY --> CTRL
    GATEWAY --> CP
    GATEWAY --> MON
```

This architecture provides:
- **Single entry point** for all traffic
- **Dynamic routing** based on request characteristics
- **Version-aware** service discovery
- **High availability** with multiple replicas
- **Observability** with integrated monitoring
- **Scalability** through horizontal pod scaling