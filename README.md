<div align="center">
  <img src="https://www.elchi.io/logo.png" alt="Elchi Logo" width="200"/>

  # Elchi Backend

  **Modern Envoy proxy management platform with distributed architecture, Global Server Load Balancing, and enterprise-grade service mesh capabilities.**

  [![Go Version](https://img.shields.io/badge/Go-1.19+-00ADD8?style=flat&logo=go)](https://go.dev/)
  [![MongoDB](https://img.shields.io/badge/MongoDB-4.4+-47A248?style=flat&logo=mongodb&logoColor=white)](https://www.mongodb.com/)
  [![License](https://img.shields.io/badge/License-Proprietary-red?style=flat)](LICENSE)
</div>

---

## ✨ Features

### 🎛️ **Envoy Management**
Complete xDS control-plane (ADS, CDS, EDS, LDS, RDS, VHDS) with snapshot caching and version management

### 🌍 **Global Server Load Balancing (GSLB)**
- DNS-based load balancing with health checking
- Tri-state health model (passing → warning → critical)
- Adaptive backoff strategy with circuit breaker
- 128 shards × 8 sub-shards = 1,024 logical partitions
- Supports HTTP/HTTPS/TCP probes

### 🔐 **Security & Compliance**
- **Authentication**: JWT with refresh tokens, OTP/2FA support
- **Authorization**: RBAC (Owner/Admin/Editor/Viewer)
- **WAF Protection**: Coraza WAF with OWASP CRS v4.x
- **Auto TLS/SSL**: ACME protocol (Let's Encrypt, ZeroSSL, custom CA)
- **Certificate Automation**: Auto-renewal with distributed locking (HA-ready)
- **DNS Challenges**: Cloudflare, Route53, and custom providers

### ☸️ **Kubernetes Integration**
- Auto endpoint discovery from K8s clusters
- Node health tracking and role-based filtering
- Multi-cluster support

### 🎨 **Developer Experience**
- **Scenario Builder**: Step-by-step wizard for Envoy resources
- **Component Catalog**: Pre-built templates for common patterns
- **Real-time Validation**: Configuration validation before deployment
- **Enhanced Error Tracking**: Pattern recognition with auto-resolve
- **Global Search**: Cross-resource search and dependency graph

### 📊 **Observability & Monitoring**
- Real-time health check metrics (300k+ endpoints)
- DNS query statistics (<10ms response time)
- Enhanced error categorization (21+ types)
- Comprehensive audit logging
- Performance metrics and alerts

---

## 🏗️ Architecture

```
┌─────────────┐      ┌──────────────┐      ┌───────────────┐
│  Registry   │◄────►│  Controller  │◄────►│ Control-Plane │
│   :9090     │      │    :8080     │      │    :18000     │
└─────────────┘      └──────────────┘      └───────────────┘
      │                      │                       │
      │                      ▼                       ▼
      │               ┌──────────┐           ┌──────────┐
      └──────────────►│ MongoDB  │           │  Envoys  │
                      └──────────┘           └──────────┘
```

**Three-Process Distributed System:**

| Component | Port | Purpose |
|-----------|------|---------|
| **Registry** | 9090 | Service discovery, client routing, in-memory data store |
| **Controller** | 8080 | REST API, resource CRUD, user management |
| **Control-Plane** | 18000 | Envoy xDS server, snapshot management, VHDS |

---

## 📦 Project Structure

```
elchi-backend/
├── cmd/                    # Main entry points
├── controller/             # REST API layer
│   ├── api/               # HTTP handlers, middleware, routes
│   ├── client/            # Client management (gRPC server)
│   ├── crud/              # Resource CRUD operations
│   └── handlers/          # API endpoint handlers
├── control-plane/          # xDS control-plane
│   └── server/            # Envoy xDS server implementation
├── pkg/                    # Shared packages
│   ├── gslb/              # GSLB system (health checking)
│   ├── models/            # Data models
│   ├── bridge/            # gRPC bridge services
│   └── validation/        # Input validation
└── registry/               # Service discovery
    ├── server/            # gRPC server
    └── storage/           # In-memory storage
```

---

## 🤝 Contributing

This is a private repository. For access or collaboration inquiries, contact [CloudNativeWorks](https://www.cloudnativeworks.com).

---

## 📄 License

Copyright © 2024 CloudNativeWorks. All rights reserved.

---

## 🔗 Links

- **Website**: [elchi.io](https://www.elchi.io)
- **Documentation**: [elchi.io/docs](https://elchi.io/docs)

---

<div align="center">
  <strong>Built with ❤️ by CloudNativeWorks</strong>
</div>
