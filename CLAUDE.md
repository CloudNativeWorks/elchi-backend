# Claude Instructions

## 🎯 Project Overview
Elchi Backend is a comprehensive service mesh management platform developed for Envoy proxy management. The system operates through 3 main processes, providing a distributed architecture.

## 🏗️ System Architecture

### 3 Main Processes

#### 1. **Registry Process** (Port: 9090)
- **Command:** `elchi-registry`
- **Purpose:** Service discovery and routing service
- **Features:**
  - Controller registration and address sharing
  - Client location tracking (which controller they're connected to)
  - Control-plane routing (version-based routing)
  - External Processing (integration with Envoy ext_proc protocol)
  - In-memory data storage (high performance)
  - gRPC API service
  - Stale data cleanup every 15 minutes

#### 2. **Controller Process** (HTTP Port: Configurable)
- **Command:** `elchi-controller`
- **Purpose:** Main management and API service
- **Features:**
  - REST API endpoints
  - Client management and command dispatching
  - XDS resource management (clusters, listeners, routes, endpoints)
  - User and authorization management
  - MongoDB database integration
  - Continuous health check and synchronization with registry
  - JWT-based authentication

#### 3. **Control-Plane Process** (gRPC Port: 18000)
- **Command:** `elchi-control-plane`
- **Purpose:** Envoy xDS management service
- **Features:**
  - Envoy ADS (Aggregated Discovery Service) service
  - VHDS (Virtual Host Discovery Service) service
  - Snapshot management and cache system
  - Bridge services (snapshot, resource, poke)
  - Automatic registration with registry and node list updates
  - Health check service

## 📊 Database Structure (MongoDB)

### Main Collections:

#### User Management:
- **users**: User information, JWT tokens
- **groups**: User groups
- **projects**: Project definitions
- **settings**: API tokens and settings

#### Envoy Resources:
- **bootstrap**: Envoy bootstrap configurations
- **clusters**: Upstream cluster definitions
- **listeners**: Network listener configurations
- **routes**: HTTP route definitions
- **endpoints**: Service endpoints
- **virtual_hosts**: Virtual host configurations

#### Extensions and Filters:
- **filters**: HTTP/Network filters (RBAC, CORS, Rate Limit, etc.)
- **extensions**: Access loggers and other extensions
- **secrets**: TLS certificates and security settings
- **tls**: TLS context configurations

#### Service Management:
- **envoys**: Connected Envoy instances and their status

## 🔌 API Endpoints

### Authentication
- `POST /auth/login` - User login
- `POST /logout` - Logout
- `POST /refresh` - Token refresh

### Client Operations
- `GET /api/op/clients` - List clients
- `POST /api/op/clients` - Execute client commands
- `GET /api/op/clients/:client_id` - Client details

### Service Operations
- `GET /api/op/services` - List services
- `GET /api/op/services/:service_id` - Service details
- `GET /api/op/services/envoys/:service_id` - Envoy details

### XDS Resource Management
- `GET/POST/PUT/DELETE /api/v3/xds/:collection` - XDS resource CRUD operations

### Extension Management
- `GET/POST/PUT/DELETE /api/v3/eo/:collection/:type` - Extension CRUD operations

### User and Permission Management
- User, Group, Project CRUD operations (`/api/v3/setting/*`)
- Permission management
- Token management

### Other
- Dependency analysis
- Scenario management
- Bridge operations
- Registry data viewing

## ⚙️ Configuration

### Environment Variables:
```yaml
# Elchi Settings
ELCHI_ADDRESS: Server address
ELCHI_PORT: HTTP port
ELCHI_TLS_ENABLED: TLS status
ELCHI_ENABLE_DEMO: Demo mode
ELCHI_VERSIONS: Supported Envoy versions
ELCHI_NAMESPACE: Kubernetes namespace

# MongoDB Settings
MONGODB_HOSTS: MongoDB host addresses
MONGODB_USERNAME: Username
MONGODB_PASSWORD: Password
MONGODB_DATABASE: Database name
MONGODB_PORT: Port
MONGODB_REPLICASET: Replica set name
MONGODB_TLS_ENABLED: TLS status

# Registry Settings
REGISTRY_ADDRESS: Registry address
REGISTRY_PORT: Registry port (9090)

# Logging
LOGGING_LEVEL: Log level (debug/info/warn/error)
LOGGING_FORMAT: Log format (text/json)
LOGGING_OUTPUT_PATH: Log output path
```

## 🔄 Process Communication

### Registry Central Communication:
1. **Controller → Registry**
   - Controller registers itself with registry at startup
   - Reports client connections
   - Periodic health check

2. **Control-Plane → Registry**
   - Control-plane registers itself at startup
   - Updates node list every 30 seconds
   - Snapshot delivery notifications

3. **Envoy → Registry → Control-Plane**
   - Envoy xDS requests are routed through registry
   - Version-based routing to appropriate control-plane

### Client-Controller Communication:
- Clients connect to controller via gRPC
- Controller processes and responds to client commands
- Access to clients on other controllers through registry

## 🔐 Security Features

- JWT-based authentication
- Role-based access control (admin, editor, viewer, owner)
- Project and group-based authorization
- Resource-level permission control
- TLS/mTLS support
- API token management

## 📈 Performance and Scalability

- In-memory cache (Registry and Control-plane)
- Distributed architecture (multiple controller/control-plane)
- Load balancing support
- Connection pooling
- Stale data cleanup mechanisms
- gRPC keepalive and health check

## 🛠️ Development Environment

### Requirements:
- Go 1.19+
- MongoDB 4.4+
- Docker (optional)

### Run Commands:
```bash
# Start Registry
go run main.go elchi-registry --config config.yaml

# Start Controller
go run main.go elchi-controller --config config.yaml

# Start Control-plane
go run main.go elchi-control-plane --config config.yaml
```

## 📝 Important Notes

1. **Version Compatibility**: Control-planes are routed based on Envoy versions
2. **Snapshot Management**: Control-plane caches and distributes snapshots to envoys
3. **Health Monitoring**: All components perform continuous health checks
4. **Resource Dependencies**: Inter-resource dependencies are automatically tracked
5. **Multi-tenancy**: Project-based resource isolation is supported

## 🔍 Debugging and Monitoring

- Detailed log levels (debug/info/warn/error)
- Connection analysis with gRPC interceptors
- Envoy error tracking
- Client connection status monitoring
- Registry data cleanup logs