# KubeVirt IMDS: Implementation Plan

This document outlines the implementation plan for KubeVirt IMDS using Go.

## Project Structure

```
kubevirt-imds/
├── cmd/
│   ├── imds-server/          # IMDS sidecar server
│   │   └── main.go
│   └── imds-webhook/         # Mutating admission webhook
│       └── main.go
├── pkg/
│   ├── imds/                  # IMDS server logic
│   │   ├── server.go          # HTTP server
│   │   └── handlers.go        # API endpoint handlers
│   ├── network/               # Network setup
│   │   ├── bridge.go          # Bridge discovery
│   │   └── veth.go            # veth pair creation
│   └── webhook/               # Webhook logic
│       ├── server.go          # Webhook server
│       └── mutate.go          # Pod mutation logic
├── deploy/
│   ├── webhook/               # Webhook deployment manifests
│   │   ├── deployment.yaml
│   │   ├── service.yaml
│   │   ├── webhook.yaml       # MutatingWebhookConfiguration
│   │   └── rbac.yaml
│   └── test/                  # Test VM manifests
│       └── vm-with-imds.yaml
├── hack/
│   ├── generate-certs.sh      # Generate webhook TLS certs
│   └── kind-config.yaml       # Kind cluster config for testing
├── Dockerfile                 # IMDS sidecar image
├── Dockerfile.webhook         # Webhook image
├── go.mod
├── go.sum
└── Makefile
```

## Components

### 1. IMDS Server (Sidecar)

The sidecar container that serves the IMDS API to VMs.

#### Responsibilities

1. **Init phase** (runs as init container):
   - Discover VM bridge name (e.g., `k6t-eth0`)
   - Create veth pair (`veth-imds` / `veth-imds-br`)
   - Attach `veth-imds-br` to VM bridge
   - Configure `169.254.169.254/32` on `veth-imds`

2. **Server phase** (runs as main container):
   - Listen on `169.254.169.254:80`
   - Serve `/v1/token`, `/v1/identity`, `/healthz` endpoints
   - Read token from projected volume on each request

#### Key Packages

```go
// pkg/network/veth.go
package network

// SetupVeth creates a veth pair and attaches to the bridge
func SetupVeth(bridgeName string) error

// DiscoverBridge finds the VM bridge (k6t-*)
func DiscoverBridge() (string, error)
```

```go
// pkg/imds/server.go
package imds

type Server struct {
    tokenPath    string
    namespace    string
    podName      string
    vmName       string
    saName       string
    listenAddr   string
}

func (s *Server) Run(ctx context.Context) error
```

```go
// pkg/imds/handlers.go
package imds

// GET /v1/token
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request)

// GET /v1/identity
func (s *Server) handleIdentity(w http.ResponseWriter, r *http.Request)

// GET /healthz
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request)
```

#### Environment Variables

| Variable | Description | Example |
|----------|-------------|---------|
| `IMDS_TOKEN_PATH` | Path to SA token file | `/var/run/secrets/tokens/token` |
| `IMDS_NAMESPACE` | VM namespace | `default` |
| `IMDS_POD_NAME` | virt-launcher pod name | `virt-launcher-myvm-abc123` |
| `IMDS_VM_NAME` | VM name | `myvm` |
| `IMDS_SA_NAME` | ServiceAccount name | `my-sa` |
| `IMDS_BRIDGE_NAME` | Bridge name override (optional) | `k6t-eth0` |

### 2. Mutating Webhook

Intercepts VirtualMachineInstance creation and injects the IMDS sidecar.

#### Responsibilities

1. Watch for VMI pods with annotation `imds.kubevirt.io/enabled: "true"`
2. Inject IMDS init container (network setup)
3. Inject IMDS server container
4. Add projected ServiceAccount token volume
5. Pass metadata via environment variables

#### Mutation Logic

```go
// pkg/webhook/mutate.go
package webhook

func MutatePod(pod *corev1.Pod) (*corev1.Pod, error) {
    // 1. Check for IMDS annotation
    // 2. Add projected SA token volume
    // 3. Add init container for veth setup
    // 4. Add IMDS server container
    // 5. Return mutated pod
}
```

#### Injected Containers

**Init Container** (network setup):
```yaml
- name: imds-init
  image: kubevirt-imds-server:latest
  command: ["/imds-server", "init"]
  securityContext:
    capabilities:
      add: ["NET_ADMIN"]
  env:
    - name: IMDS_BRIDGE_NAME
      value: ""  # auto-detect
```

**Server Container**:
```yaml
- name: imds-server
  image: kubevirt-imds-server:latest
  command: ["/imds-server", "serve"]
  env:
    - name: IMDS_TOKEN_PATH
      value: /var/run/secrets/tokens/token
    - name: IMDS_NAMESPACE
      valueFrom:
        fieldRef:
          fieldPath: metadata.namespace
    # ... other env vars from downward API
  volumeMounts:
    - name: imds-token
      mountPath: /var/run/secrets/tokens
      readOnly: true
```

**Projected Volume**:
```yaml
volumes:
  - name: imds-token
    projected:
      sources:
        - serviceAccountToken:
            path: token
            expirationSeconds: 3600
```

## Build

### Makefile Targets

```makefile
# Build binaries
build: build-server build-webhook

build-server:
	go build -o bin/imds-server ./cmd/imds-server

build-webhook:
	go build -o bin/imds-webhook ./cmd/imds-webhook

# Build container images
docker-build: docker-build-server docker-build-webhook

docker-build-server:
	docker build -f Dockerfile.server -t kubevirt-imds-server:latest .

docker-build-webhook:
	docker build -f Dockerfile.webhook -t kubevirt-imds-webhook:latest .

# Load images into kind
kind-load:
	kind load docker-image kubevirt-imds-server:latest
	kind load docker-image kubevirt-imds-webhook:latest

# Generate webhook certs
generate-certs:
	./hack/generate-certs.sh

# Deploy to cluster
deploy: deploy-webhook

deploy-webhook:
	kubectl apply -f deploy/webhook/

# Run tests
test:
	go test ./...

test-e2e:
	go test ./test/e2e/... -v
```

## Deployment

### Prerequisites

1. Kubernetes cluster with KubeVirt installed
2. cert-manager (recommended) or manual TLS cert generation

### Step 1: Build and Load Images

```bash
# Build images
make docker-build

# For kind clusters
make kind-load
```

### Step 2: Generate Webhook Certificates

Option A: Using cert-manager (recommended):
```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: imds-webhook-cert
  namespace: kubevirt-imds
spec:
  secretName: imds-webhook-tls
  dnsNames:
    - imds-webhook.kubevirt-imds.svc
    - imds-webhook.kubevirt-imds.svc.cluster.local
  issuerRef:
    name: selfsigned-issuer
    kind: ClusterIssuer
```

Option B: Manual generation:
```bash
./hack/generate-certs.sh
```

### Step 3: Deploy Webhook

```bash
kubectl create namespace kubevirt-imds
kubectl apply -f deploy/webhook/
```

### Step 4: Verify Deployment

```bash
# Check webhook is running
kubectl get pods -n kubevirt-imds

# Check webhook configuration
kubectl get mutatingwebhookconfiguration imds-webhook
```

## Testing

### Unit Tests

```bash
make test
```

### Manual Testing with Kind

#### 1. Set Up Test Environment

```bash
# Create kind cluster (if not exists)
kind create cluster --config hack/kind-config.yaml

# Install KubeVirt
export KUBEVIRT_VERSION=$(curl -s https://storage.googleapis.com/kubevirt-prow/release/kubevirt/kubevirt/stable.txt)
kubectl create -f https://github.com/kubevirt/kubevirt/releases/download/${KUBEVIRT_VERSION}/kubevirt-operator.yaml
kubectl create -f https://github.com/kubevirt/kubevirt/releases/download/${KUBEVIRT_VERSION}/kubevirt-cr.yaml

# Wait for KubeVirt
kubectl wait --for=jsonpath='{.status.phase}'=Deployed kubevirt/kubevirt -n kubevirt --timeout=300s

# Build and deploy IMDS
make docker-build kind-load deploy
```

#### 2. Create Test VM

```yaml
# deploy/test/vm-with-imds.yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: testvm-imds
  annotations:
    imds.kubevirt.io/enabled: "true"
spec:
  runStrategy: Always
  template:
    metadata:
      labels:
        kubevirt.io/domain: testvm-imds
    spec:
      serviceAccountName: default
      domain:
        devices:
          disks:
            - name: containerdisk
              disk:
                bus: virtio
            - name: cloudinitdisk
              disk:
                bus: virtio
          interfaces:
            - name: default
              masquerade: {}
        resources:
          requests:
            memory: 128M
      networks:
        - name: default
          pod: {}
      volumes:
        - name: containerdisk
          containerDisk:
            image: quay.io/kubevirt/cirros-container-disk-demo
        - name: cloudinitdisk
          cloudInitNoCloud:
            userData: |
              #!/bin/sh
              echo "Testing IMDS..."
```

```bash
kubectl apply -f deploy/test/vm-with-imds.yaml
```

#### 3. Verify IMDS Injection

```bash
# Check that virt-launcher pod has IMDS containers
kubectl get pod -l kubevirt.io/domain=testvm-imds -o jsonpath='{.items[0].spec.containers[*].name}'
# Expected: compute, imds-server

kubectl get pod -l kubevirt.io/domain=testvm-imds -o jsonpath='{.items[0].spec.initContainers[*].name}'
# Expected: ..., imds-init
```

#### 4. Test IMDS from Inside VM

```bash
# Console into VM
virtctl console testvm-imds

# Inside VM (cirros)
$ curl -s http://169.254.169.254/healthz
OK

$ curl -s http://169.254.169.254/v1/identity
{"namespace":"default","serviceAccountName":"default","vmName":"testvm-imds",...}

$ curl -s http://169.254.169.254/v1/token | head -c 50
{"token":"eyJhbGciOiJSUzI1NiIsImtpZCI6I...
```

### E2E Tests

```go
// test/e2e/imds_test.go
package e2e

func TestIMDSTokenEndpoint(t *testing.T) {
    // 1. Create VM with IMDS enabled
    // 2. Wait for VM to be running
    // 3. Execute curl inside VM to fetch token
    // 4. Verify token is valid JWT
    // 5. Cleanup
}

func TestIMDSIdentityEndpoint(t *testing.T) {
    // 1. Create VM with IMDS enabled
    // 2. Wait for VM to be running
    // 3. Execute curl inside VM to fetch identity
    // 4. Verify identity matches VM metadata
    // 5. Cleanup
}

func TestIMDSBridgeMode(t *testing.T) {
    // 1. Create VM with bridge networking + IMDS
    // 2. Verify IMDS works in bridge mode
    // 3. Cleanup
}
```

## Implementation Phases

### Phase 1: Core IMDS Server

1. Implement basic HTTP server with `/healthz` endpoint
2. Implement `/v1/token` endpoint (read from file)
3. Implement `/v1/identity` endpoint (read from env vars)
4. Add veth setup logic (`init` command)
5. Build Docker image

**Deliverable**: Working IMDS server that can be manually deployed

### Phase 2: Mutating Webhook

1. Implement webhook server with TLS
2. Implement pod mutation logic
3. Add projected volume injection
4. Add container injection
5. Create deployment manifests

**Deliverable**: Automatic IMDS injection via annotation

### Phase 3: Testing & Polish

1. Unit tests for all packages
2. E2E tests with kind + KubeVirt
3. Documentation
4. CI/CD pipeline

**Deliverable**: Production-ready release

## Dependencies

```go
// go.mod
module github.com/example/kubevirt-imds

go 1.21

require (
    github.com/vishvananda/netlink v1.1.0  // veth/bridge manipulation
    k8s.io/api v0.29.0
    k8s.io/apimachinery v0.29.0
    k8s.io/client-go v0.29.0
    sigs.k8s.io/controller-runtime v0.17.0  // webhook helpers
)
```

## Security Considerations

1. **Webhook TLS**: Always use TLS for webhook communication
2. **RBAC**: Webhook needs minimal permissions (no cluster-admin)
3. **Image security**: Use distroless base images, scan for vulnerabilities
4. **Network policy**: Consider adding NetworkPolicy to restrict webhook access
