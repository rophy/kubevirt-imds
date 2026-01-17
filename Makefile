.PHONY: build build-server docker-build kind-load test clean

# Image settings
IMAGE_REPO ?= kubevirt-imds
IMAGE_TAG ?= latest
IMAGE ?= $(IMAGE_REPO):$(IMAGE_TAG)

# Build the imds-server binary
build: build-server

build-server:
	go build -o bin/imds-server ./cmd/imds-server

# Build Docker image
docker-build:
	docker build -t $(IMAGE) .

# Load image into kind cluster
kind-load: docker-build
	kind load docker-image $(IMAGE)

# Run tests
test:
	go test -v ./...

# Clean build artifacts
clean:
	rm -rf bin/
	go clean

# Run go mod tidy
tidy:
	go mod tidy

# Format code
fmt:
	go fmt ./...

# Lint code
lint:
	golangci-lint run

# Build and test locally (without Docker)
local: build
	@echo "Binary built at bin/imds-server"
	@echo "Run with: sudo ./bin/imds-server init"
	@echo "Then:     sudo ./bin/imds-server serve"
