# VPN Backend Services

Microservices architecture for VPN management system.

## Architecture

```
vpn_go/
├── platform/          # Shared platform packages
│   └── pkg/
│       ├── closer/    # Graceful shutdown
│       ├── grpc/      # gRPC utilities
│       ├── logger/    # Logging (zap)
│       └── postgres/  # Database utilities
├── services/          # Microservices
│   └── gateway/       # API Gateway (HTTP → gRPC)
├── shared/            # Shared proto definitions
│   └── proto/
├── deploy/            # Deployment configs
│   ├── compose/       # Docker Compose
│   └── env/           # Environment templates
└── docs/              # Documentation
```

## Tech Stack

- **Language:** Go 1.23
- **Framework:** gRPC, HTTP (Chi/Gin)
- **Database:** PostgreSQL
- **Logging:** Zap
- **Architecture:** Microservices

## Services

### Gateway
- HTTP API Gateway
- Routes requests to microservices
- Authentication & authorization
- Rate limiting

## Development

### Prerequisites
- Go 1.23+
- PostgreSQL 16+
- Docker & Docker Compose

### Setup

```bash
# Install dependencies
go work sync

# Run services
task run-all

# Build binaries
task build-all
```

## Project Structure

Based on eng_go architecture with clean separation:
- `platform/` - Reusable platform code
- `services/` - Business logic microservices
- `shared/` - Proto definitions & generated code
- `deploy/` - Infrastructure as code

## License

Proprietary
