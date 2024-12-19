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

# Build the application
RUN CGO_ENABLED=1 GOOS=linux go build -o /app/bin/mye-r ./cmd/main.go

# Final stage
FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/bin/mye-r /app/

# Create necessary directories and set permissions
RUN mkdir -p /myer/data /app/library /app/rclone && \
    adduser -D -u 1000 appuser && \
    chown -R appuser:appuser /app /myer

# Switch to non-root user
USER appuser

# Command to run the application
ENTRYPOINT ["/app/mye-r"]
