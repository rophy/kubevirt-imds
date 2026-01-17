.PHONY: build build-server build-webhook docker-build docker-build-all kind-load kind-load-all test clean deploy generate-certs

# Image settings
IMAGE_REPO ?= kubevirt-imds
IMAGE_TAG ?= latest
SERVER_IMAGE ?= $(IMAGE_REPO):$(IMAGE_TAG)
WEBHOOK_IMAGE ?= $(IMAGE_REPO)-webhook:$(IMAGE_TAG)

# Build all binaries
build: build-server build-webhook

build-server:
	go build -o bin/imds-server ./cmd/imds-server

build-webhook:
	go build -o bin/imds-webhook ./cmd/imds-webhook

# Build Docker images
docker-build: docker-build-server

docker-build-server:
	docker build -t $(SERVER_IMAGE) -f Dockerfile .

docker-build-webhook:
	docker build -t $(WEBHOOK_IMAGE) -f Dockerfile.webhook .

docker-build-all: docker-build-server docker-build-webhook

# Load images into kind cluster
kind-load: kind-load-server

kind-load-server: docker-build-server
	kind load docker-image $(SERVER_IMAGE)

kind-load-webhook: docker-build-webhook
	kind load docker-image $(WEBHOOK_IMAGE)

kind-load-all: kind-load-server kind-load-webhook

# Generate TLS certificates for webhook
generate-certs:
	./hack/generate-certs.sh

# Deploy webhook to cluster
deploy: kind-load-all generate-certs
	kubectl apply -f deploy/webhook/namespace.yaml
	kubectl apply -f deploy/webhook/rbac.yaml
	kubectl apply -f deploy/webhook/deployment.yaml
	kubectl apply -f deploy/webhook/service.yaml
	kubectl apply -f deploy/webhook/webhook.yaml
	@echo "Waiting for webhook to be ready..."
	kubectl wait --for=condition=Available deployment/imds-webhook -n kubevirt-imds --timeout=60s

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
	@echo "Binaries built at bin/"
	@echo "Run IMDS server with: sudo ./bin/imds-server init && sudo ./bin/imds-server serve"
	@echo "Run webhook with: ./bin/imds-webhook --imds-image=kubevirt-imds:latest"
