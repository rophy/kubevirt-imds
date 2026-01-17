# Build stage
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /imds-server ./cmd/imds-server

# Runtime stage
FROM alpine:3.19

# Install iproute2 for debugging (optional, can be removed for smaller image)
RUN apk add --no-cache ca-certificates

COPY --from=builder /imds-server /imds-server

ENTRYPOINT ["/imds-server"]
