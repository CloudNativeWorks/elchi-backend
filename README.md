# Elchi Backend Project

Elchi Backend is a comprehensive service mesh management platform built with Go, consisting of three main processes:

1. **Registry Process** (Port: 9090) - Service discovery and routing service
2. **Controller Process** (HTTP Port: Configurable) - Main management and REST API service
3. **Control-Plane Process** (gRPC Port: 18000) - Envoy xDS management service

This distributed architecture provides scalable Envoy proxy management with automatic service discovery, configuration distribution, and real-time monitoring capabilities.

## Table of Contents
- [Architecture Overview](#architecture-overview)
- [Getting Started](#getting-started)
- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Configuration](#configuration)
- [Running the Processes](#running-the-processes)
  - [Registry Process](#registry-process)
  - [Controller Process](#controller-process)
  - [Control-Plane Process](#control-plane-process)
- [Features](#features)
- [API Endpoints](#api-endpoints)
- [Environment Variables](#environment-variables)

## Architecture Overview

### 3 Main Processes

#### 1. Registry Process (Port: 9090)
- Service discovery and routing service
- Controller registration and address sharing
- Client location tracking
- Control-plane routing (version-based)
- External Processing integration
- In-memory data storage with cleanup

#### 2. Controller Process (HTTP Port: Configurable)
- REST API endpoints
- Client management and command dispatching
- XDS resource management
- User and authorization management
- MongoDB database integration
- JWT-based authentication
- AI-powered configuration analysis
- K8s Discovery system integration

#### 3. Control-Plane Process (gRPC Port: 18000)
- Envoy ADS (Aggregated Discovery Service)
- VHDS (Virtual Host Discovery Service)
- Snapshot management and cache system
- Bridge services (snapshot, resource, poke)
- Automatic registration with registry

## Getting Started

These instructions will help you set up the Elchi Backend project on your local machine for development and testing purposes.

### Prerequisites

Before you begin, ensure you have met the following requirements:
- Go 1.19 or later installed on your machine
- MongoDB 4.4+ running locally or accessible via network
- Docker (optional) for containerized deployment
- Kubernetes cluster (optional) for K8s Discovery feature

### Installation

Clone the repository to your local machine:

```bash
git clone https://github.com/CloudNativeWorks/elchi-backend.git
cd elchi-backend
```

Install the required Go modules:

```bash
go mod tidy
```

## Configuration

Create a `config.yaml` file with the following structure:

```yaml
# Elchi Settings
elchi:
  address: "0.0.0.0"
  port: 8080
  tls_enabled: false
  enable_demo: false
  versions: ["1.27", "1.28", "1.29"]
  namespace: "elchi-system"

# MongoDB Settings
mongodb:
  hosts: ["localhost:27017"]
  username: ""
  password: ""
  database: "elchi"
  replica_set: ""
  tls_enabled: false

# Registry Settings
registry:
  address: "localhost"
  port: 9090

# Logging
logging:
  level: "info"
  format: "json"
  output_path: "stdout"
```

## Running the Processes

### Registry Process

The registry process handles service discovery and routing. Start it first:

```bash
go run main.go elchi-registry --config config.yaml
```

### Controller Process

The controller provides REST API endpoints and manages resources:

```bash
go run main.go elchi-controller --config config.yaml
```

### Control-Plane Process

The control-plane manages Envoy xDS services:

```bash
go run main.go elchi-control-plane --config config.yaml
```

## Features

### Core Features
- **Multi-process Architecture**: Scalable distributed system
- **Envoy xDS Management**: Complete ADS, CDS, EDS, LDS, RDS, VHDS support
- **Service Discovery**: Automatic service registration and routing
- **Resource Management**: Full CRUD operations for all Envoy resources
- **User Management**: JWT authentication and RBAC authorization
- **Project Isolation**: Multi-tenant resource management

### Advanced Features
- **AI Configuration Analysis**: Claude-powered config analysis and troubleshooting
- **Log Analysis**: AI-powered Envoy log analysis with context awareness
- **K8s Discovery**: Automatic endpoint updates from Kubernetes clusters
- **Snapshot Management**: Efficient configuration distribution
- **Health Monitoring**: Continuous health checks and status reporting
- **External Processing**: Envoy ext_proc protocol integration

## System Architecture Details

### Process Communication Flow

#### Registry-Centered Communication
1. **Controller → Registry**
   - Controller registers itself at startup
   - Reports client connections and status
   - Periodic health checks every 30 seconds

2. **Control-Plane → Registry**
   - Control-plane registers itself at startup
   - Updates node list every 30 seconds
   - Reports snapshot delivery status

3. **Envoy → Registry → Control-Plane**
   - Envoy xDS requests routed through registry
   - Version-based routing to appropriate control-plane
   - Load balancing across multiple control-planes

#### Client-Controller Communication
- Clients connect to controller via gRPC
- Controller processes commands and forwards responses
- Cross-controller client access through registry

### Database Structure (MongoDB)

#### Core Collections
- **users**: User accounts and JWT tokens
- **groups**: User groups and permissions
- **projects**: Project definitions and isolation
- **settings**: API tokens, Claude tokens, discovery tokens

#### Envoy Resources
- **bootstrap**: Envoy bootstrap configurations
- **clusters**: Upstream cluster definitions
- **listeners**: Network listener configurations
- **routes**: HTTP route configurations
- **endpoints**: Service endpoint definitions
- **virtual_hosts**: Virtual host configurations

#### Extensions & Security
- **filters**: HTTP/Network filters (RBAC, CORS, Rate Limit)
- **extensions**: Access loggers and other extensions
- **secrets**: TLS certificates and security settings
- **tls**: TLS context configurations

#### Operational Data
- **envoys**: Connected Envoy instances and status
- **discovery**: K8s discovered clusters and nodes

### Security & Authorization

#### Authentication Flow
1. User login via JWT tokens
2. Token validation on each request
3. Automatic token refresh mechanism
4. Role-based access control (RBAC)

#### Permission Levels
- **Owner**: Full project access and management
- **Admin**: Resource management within project
- **Editor**: Create, update, delete resources
- **Viewer**: Read-only access to resources

#### Multi-tenancy
- Project-based resource isolation
- User-group-project hierarchy
- Cross-project access controls
- Resource-level permissions

### Performance & Scalability

#### Caching Strategies
- **Registry**: In-memory service discovery cache
- **Control-Plane**: Snapshot cache with incremental updates
- **Controller**: Connection pooling and request optimization

#### Health Monitoring
- Continuous health checks across all processes
- Automatic service recovery and failover
- Connection status monitoring and reporting
- Stale data cleanup (every 15 minutes)

#### Load Balancing
- Multiple controller instances supported
- Multiple control-plane instances with version routing
- Client request distribution via registry

## Environment Variables

### Core Settings
```bash
ELCHI_ADDRESS=0.0.0.0
ELCHI_PORT=8080
ELCHI_TLS_ENABLED=false
ELCHI_ENABLE_DEMO=false
ELCHI_VERSIONS="1.27,1.28,1.29"
ELCHI_NAMESPACE=elchi-system
```

### MongoDB Settings
```bash
MONGODB_HOSTS=localhost:27017
MONGODB_USERNAME=
MONGODB_PASSWORD=
MONGODB_DATABASE=elchi
MONGODB_PORT=27017
MONGODB_REPLICASET=
MONGODB_TLS_ENABLED=false
```

### Registry Settings
```bash
REGISTRY_ADDRESS=localhost
REGISTRY_PORT=9090
```

### Logging Settings
```bash
LOGGING_LEVEL=info
LOGGING_FORMAT=json
LOGGING_OUTPUT_PATH=stdout
```