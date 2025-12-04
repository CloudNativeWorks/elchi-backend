# Elchi Backend

Modern Envoy proxy management platform with distributed architecture, featuring xDS control-plane, WAF protection, and multi-tenancy support for enterprise service mesh deployments.

## Architecture

Three-process distributed system:

- **Registry** (`:9090`) - Service discovery and routing
- **Controller** (`:8080`) - REST API and resource management
- **Control-Plane** (`:18000`) - Envoy xDS (ADS, CDS, EDS, LDS, RDS, VHDS)

## Quick Start

```bash
# Install dependencies
go mod tidy

# Start Registry
go run main.go elchi-registry --config config.yaml

# Start Controller
go run main.go elchi-controller --config config.yaml

# Start Control-Plane
go run main.go elchi-control-plane --config config.yaml
```

## Prerequisites

- Go 1.19+
- MongoDB 4.4+
- Kubernetes cluster (optional, for K8s Discovery)

## Core Features

- **Envoy Management** - Complete xDS implementation with snapshot caching
- **Multi-tenancy** - Project-based resource isolation with RBAC
- **K8s Integration** - Automatic endpoint discovery from Kubernetes
- **Distributed** - Scalable multi-instance architecture
- **Security** - JWT authentication with role-based authorization and OTP support
- **WAF Protection** - Embedded Coraza WAF with CRS rules integration
- **Scenario Builder** - Advanced configuration wizard for Envoy resources
- **Enhanced Error Tracking** - Intelligent error analysis with auto-resolve mechanism
- **Global Search** - Cross-resource search capabilities

## License

Copyright © 2024 CloudNativeWorks
