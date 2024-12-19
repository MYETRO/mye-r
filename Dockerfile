# Build stage
FROM golang:1.23.4-alpine AS builder

# Install git and build dependencies
RUN apk add --no-cache git gcc musl-dev

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build all binaries
RUN CGO_ENABLED=1 GOOS=linux go build -o /app/bin/mye-r ./cmd/main.go && \
    CGO_ENABLED=1 GOOS=linux go build -o /app/bin/getcontent ./cmd/getcontent/main.go && \
    CGO_ENABLED=1 GOOS=linux go build -o /app/bin/tmdb_indexer ./cmd/tmdb_indexer/main.go && \
    CGO_ENABLED=1 GOOS=linux go build -o /app/bin/scraper ./cmd/scraper/main.go && \
    CGO_ENABLED=1 GOOS=linux go build -o /app/bin/library_matcher ./cmd/library_matcher/main.go && \
    CGO_ENABLED=1 GOOS=linux go build -o /app/bin/downloader ./cmd/downloader/main.go && \
    CGO_ENABLED=1 GOOS=linux go build -o /app/bin/symlinker ./cmd/symlinker/main.go

# Final stage
FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy binaries from builder
COPY --from=builder /app/bin/* /app/
COPY --from=builder /app/migrations /app/migrations

# Create necessary directories and set permissions
RUN mkdir -p /myer/data /app/library /app/rclone && \
    adduser -D -u 1000 appuser && \
    chown -R appuser:appuser /app /myer

# Switch to non-root user
USER appuser

# Command to run the application
ENTRYPOINT ["/app/mye-r"]
