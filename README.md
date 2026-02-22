# Frigate Plugin for Slidebolt

The Frigate Plugin provides integration for the Frigate NVR system. It allows for camera discovery and real-time event monitoring through the Slidebolt Framework.

## Features

- **Event Monitoring**: Receives and processes camera events from Frigate.
- **Isolated Service**: Runs as a standalone sidecar service communicating via NATS.

## Architecture

This plugin follows the Slidebolt "Isolated Service" pattern:
- **`pkg/bundle`**: Implementation of the `sdk.Plugin` interface.
- **`pkg/logic`**: Core logic for interacting with Frigate.
- **`cmd/main.go`**: Service entry point.

## Development

### Prerequisites
- Go (v1.25.6+)
- Slidebolt `plugin-sdk` and `plugin-framework` repos sitting as siblings.

### Local Build
Initialize the Go workspace to link sibling dependencies:
```bash
go work init . ../plugin-sdk ../plugin-framework
go build -o bin/plugin-frigate ./cmd/main.go
```

### Testing
```bash
go test ./...
```

## Docker Deployment

### Build the Image
To build with local sibling modules:
```bash
make docker-build-local
```

To build from remote GitHub repositories:
```bash
make docker-build-prod
```

### Run via Docker Compose
Add the following to your `docker-compose.yml`:
```yaml
services:
  frigate-plugin:
    image: slidebolt-plugin-frigate:latest
    environment:
      - NATS_URL=nats://core:4222
    restart: always
```

## License
Refer to the root project license.
