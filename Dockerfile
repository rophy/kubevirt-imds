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

# Runtime stage - scratch for minimal attack surface
FROM scratch

COPY --from=builder /imds-server /imds-server

ENTRYPOINT ["/imds-server"]
