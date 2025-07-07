# Elchi Backend Project

Elchi Backend is a Go-based application consisting of two main servers:
1. **gRPC Server** - Distributes configurations to Envoy instances using go-control-plane.
2. **REST Server** - Provides API endpoints for the [Elchi](https://github.com/CloudNativeWorks/elchi), handling CRUD operations with MongoDB.

## Table of Contents
- [Getting Started](#getting-started)
- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Running the Servers](#running-the-servers)
  - [gRPC Server](#grpc-server)
  - [REST Server](#rest-server)

## Getting Started

These instructions will help you set up the Elchi Backend project on your local machine for development and testing purposes.

### Prerequisites

Before you begin, ensure you have met the following requirements:
- Go 1.22 or later installed on your machine
- MongoDB running locally or accessible via network
- Docker (optional) if running MongoDB or Envoy instances locally

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

## Running the Servers

### gRPC Server

The gRPC server is responsible for distributing configurations to Envoy instances using go-control-plane. It serves as the control plane in an Envoy xDS setup.

To run the gRPC server:

```bash
go run  -ldflags "-X github.com/CloudNativeWorks/elchi-backend/pkg/version.Version=v1.33.2 elchi-control-plane
```

#### Configuration

The gRPC server uses a snapshot-based approach to distribute configurations. It supports Delta gRPC and utilizes `go-control-plane` for managing Envoy's xDS resources like CDS, EDS, LDS, and RDS.


### REST Server

The REST server provides API endpoints for the Elchi frontend application, handling CRUD operations with MongoDB.

To run the REST server:

```bash
go run  -ldflags "-X github.com/CloudNativeWorks/elchi-backend/pkg/version.Version=v1.33.2" main.go elchi-controller
```