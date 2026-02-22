# --- Build Stage ---
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
WORKDIR /src

# Explicit build mode: "prod" (default) or "local"
ARG BUILD_MODE=prod

# Copy source
COPY . .

# Pre-GitHub Bridge
RUN if [ "$BUILD_MODE" = "local" ]; then \
        echo "Building in LOCAL mode using staged siblings..." && \
        rm -f go.work && \
        go work init . ./.stage/plugin-sdk ./.stage/plugin-framework; \
    else \
        echo "Building in PROD mode using remote modules..."; \
    fi

# Build the standalone binary
RUN go build -o /app/plugin-frigate ./cmd/main.go

# --- Run Stage ---
FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/plugin-frigate /app/plugin-frigate

CMD ["/app/plugin-frigate"]