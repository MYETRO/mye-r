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

# Create bin directory
RUN mkdir -p /app/bin

# Build all binaries
RUN CGO_ENABLED=1 GOOS=linux go build -o /app/bin/mye-r ./cmd/main.go && \
    CGO_ENABLED=1 GOOS=linux go build -o /app/bin/getcontent ./cmd/run_getcontent.go && \
    CGO_ENABLED=1 GOOS=linux go build -o /app/bin/tmdb_indexer ./cmd/run_tmdb_indexer.go && \
    CGO_ENABLED=1 GOOS=linux go build -o /app/bin/scraper ./cmd/run_scraper.go && \
    CGO_ENABLED=1 GOOS=linux go build -o /app/bin/librarymatcher ./cmd/run_librarymatcher.go && \
    CGO_ENABLED=1 GOOS=linux go build -o /app/bin/downloader ./cmd/run_downloader.go && \
    CGO_ENABLED=1 GOOS=linux go build -o /app/bin/symlinker ./cmd/run_symlinker.go

# Final stage
FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy binaries from builder and rename them to match expected names
COPY --from=builder /app/bin/mye-r /app/mye-r
COPY --from=builder /app/bin/getcontent /app/getcontent
COPY --from=builder /app/bin/tmdb_indexer /app/tmdb_indexer
COPY --from=builder /app/bin/scraper /app/scraper
COPY --from=builder /app/bin/librarymatcher /app/librarymatcher
COPY --from=builder /app/bin/downloader /app/downloader
COPY --from=builder /app/bin/symlinker /app/symlinker

# Create necessary directories and set permissions
RUN mkdir -p /myer/data /app/library /app/rclone && \
    adduser -D -u 1000 appuser && \
    chown -R appuser:appuser /app /myer

# Switch to non-root user
USER appuser

# Command to run the application
ENTRYPOINT ["/app/mye-r"]
