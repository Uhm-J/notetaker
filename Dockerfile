# Build stage
FROM golang:1.21-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o discord-notetaker ./cmd/discord-notetaker

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk --no-cache add ca-certificates tzdata

# Create app directory
WORKDIR /app

# Copy binary from builder stage
COPY --from=builder /app/discord-notetaker .

# Create necessary directories
RUN mkdir -p data/transcripts data/notes models

# Set permissions
RUN chmod +x discord-notetaker

# Expose health check endpoint (if implemented)
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD pgrep discord-notetaker || exit 1

# Run as non-root user
RUN addgroup -g 1001 -S discord && \
    adduser -S discord -u 1001 -G discord

# Change ownership of app directory
RUN chown -R discord:discord /app

USER discord

# Run the application
CMD ["./discord-notetaker"]