# KubeVirt IMDS

Instance Metadata Service (IMDS) for KubeVirt virtual machines. Provides ServiceAccount tokens to VMs via HTTP at the standard cloud metadata endpoint `169.254.169.254`.

## Problem

Kubernetes pods automatically receive ServiceAccount tokens for authenticating to external services (Vault, SPIFFE, cloud providers). KubeVirt VMs lack this capability - there's no standard way for a VM to obtain its Kubernetes identity.

## Solution

KubeVirt IMDS injects a sidecar into VM pods that serves ServiceAccount tokens over HTTP at `169.254.169.254`. This works on any guest OS with TCP/IP networking (Linux, Windows, FreeBSD, etc.) without requiring special drivers or kernel versions.

```
┌─────────────────────────────────────────────────────────────┐
│  virt-launcher Pod                                          │
│                                                             │
│  ┌─────────────────────┐      ┌───────────────────────────┐ │
│  │  VM (any OS)        │      │  imds-sidecar             │ │
│  │                     │      │                           │ │
│  │  curl 169.254.169   │ ───► │  HTTP server              │ │
│  │       .254/v1/token │      │  169.254.169.254:80       │ │
│  │                     │      │                           │ │
│  └─────────────────────┘      └───────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

## Quick Start

### Prerequisites

- Kubernetes cluster with KubeVirt installed
- kubectl configured to access the cluster

### 1. Deploy the webhook

```bash
# Clone the repository
git clone https://github.com/kubevirt/kubevirt-imds.git
cd kubevirt-imds

# Build and load images (for kind clusters)
make docker-build-all
make kind-load-all

# Generate TLS certificates and deploy
make generate-certs
kubectl apply -f deploy/webhook/
```

### 2. Create a VM with IMDS enabled

Add the annotation `imds.kubevirt.io/enabled: "true"` to your VM's template:

```yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: my-vm
spec:
  runStrategy: Always
  template:
    metadata:
      annotations:
        imds.kubevirt.io/enabled: "true"  # Enable IMDS
    spec:
      serviceAccountName: my-service-account  # ServiceAccount to use
      domain:
        devices:
          disks:
            - name: rootdisk
              disk:
                bus: virtio
        resources:
          requests:
            memory: 1Gi
      volumes:
        - name: rootdisk
          containerDisk:
            image: quay.io/containerdisks/ubuntu:22.04
```

### 3. Access tokens from inside the VM

Connect to the VM console and use curl to access the IMDS:

```bash
$ virtctl console my-vm
Successfully connected to my-vm console. The escape sequence is ^]

$ curl http://169.254.169.254/v1/identity
{"namespace":"default","serviceAccountName":"my-service-account","vmName":"my-vm","podName":"virt-launcher-my-vm-xyz789"}

$ curl http://169.254.169.254/v1/token
{"token":"eyJhbGciOiJSUzI1NiIsImtpZCI6Ijk3...","expirationTimestamp":"2026-01-17T12:00:00Z"}

$ curl http://169.254.169.254/healthz
OK
```

## API Reference

### GET /v1/token

Returns the ServiceAccount token.

**Response:**
```json
{
  "token": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
  "expirationTimestamp": "2026-01-17T12:00:00Z"
}
```

### GET /v1/identity

Returns VM identity information.

**Response:**
```json
{
  "namespace": "default",
  "serviceAccountName": "my-service-account",
  "vmName": "my-vm",
  "podName": "virt-launcher-my-vm-abc123"
}
```

### GET /healthz

Health check endpoint. Returns `OK` with status 200.

## Usage Examples

### Vault Authentication (Linux)

```bash
TOKEN=$(curl -s http://169.254.169.254/v1/token | jq -r .token)
vault write auth/kubernetes/login role="my-role" jwt="$TOKEN"
```

### Vault Authentication (Windows PowerShell)

```powershell
$response = Invoke-RestMethod -Uri "http://169.254.169.254/v1/token"
vault write auth/kubernetes/login role="my-role" jwt="$($response.token)"
```

### Kubernetes API Access

```bash
TOKEN=$(curl -s http://169.254.169.254/v1/token | jq -r .token)
curl -s -k \
  -H "Authorization: Bearer $TOKEN" \
  https://kubernetes.default.svc/api/v1/namespaces/default/pods
```

### Python Application

```python
import requests

def get_k8s_token():
    response = requests.get("http://169.254.169.254/v1/token")
    return response.json()["token"]

token = get_k8s_token()
# Use token to authenticate to Vault, Kubernetes API, etc.
```

## Configuration

| Annotation | Default | Description |
|------------|---------|-------------|
| `imds.kubevirt.io/enabled` | `"false"` | Enable IMDS sidecar injection |
| `imds.kubevirt.io/bridge-name` | (auto-detect) | Override VM bridge name |

## How It Works

1. A mutating webhook watches for VM pods with the `imds.kubevirt.io/enabled: "true"` annotation
2. The webhook injects an IMDS sidecar container into the pod
3. The sidecar creates a veth pair attached to the VM's bridge network
4. The sidecar listens on `169.254.169.254:80` (link-local, only reachable from the VM)
5. When the VM requests a token, the sidecar reads it from a projected ServiceAccount volume
6. Tokens are automatically rotated by the kubelet

## Security

- **Link-local only**: The IMDS endpoint is only reachable from within the VM's network namespace
- **No credentials stored**: Tokens are read from projected volumes managed by Kubernetes
- **Automatic rotation**: Kubelet rotates tokens before expiry
- **Minimal permissions**: The sidecar only needs NET_ADMIN capability to set up networking

## Development

```bash
# Build binaries
make build

# Build Docker images
make docker-build-all

# Run tests
make test

# Load images into kind cluster
make kind-load-all
```

## License

Apache License 2.0
