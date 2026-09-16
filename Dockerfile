# Multi-stage Dockerfile for Phosphor Gateway
# Stage 1: Build binary using pure-Go compiler (CGO_ENABLED=0)
FROM golang:alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache ca-certificates git tzdata

# Cache Go modules layer
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY cmd/ cmd/
COPY internal/ internal/

# Build static binary without CGO (pure-Go SQLite via modernc.org/sqlite)
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=0.1.0" \
    -trimpath \
    -o /build/phosphor ./cmd/phosphor

# Stage 2: Minimal, secure runtime container
FROM alpine:3.21

# Install runtime dependencies for HTTPS and timezone resolution
RUN apk add --no-cache ca-certificates tzdata

# Create dedicated non-root system user and group
RUN addgroup -g 1000 phosphor && \
    adduser -u 1000 -G phosphor -s /bin/sh -D phosphor

# Setup data and configuration directories
RUN mkdir -p /data /etc/phosphor && \
    chown -R phosphor:phosphor /data /etc/phosphor

WORKDIR /home/phosphor

# Copy compiled binary from builder stage
COPY --from=builder /build/phosphor /usr/local/bin/phosphor

USER phosphor

# Expose standard gateway port
EXPOSE 8080

# Built-in container healthcheck using Phosphor ping
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD phosphor ping --url http://127.0.0.1:8080 || exit 1

# Persistent SQLite database volume
VOLUME ["/data"]

ENTRYPOINT ["phosphor"]
CMD ["start", "--config", "/etc/phosphor/config.yaml"]
