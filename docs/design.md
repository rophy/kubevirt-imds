# KubeVirt IMDS: Conceptual Design

## Overview

This document describes a conceptual design for implementing an Instance Metadata Service (IMDS) for KubeVirt virtual machines, with a focus on ServiceAccount identity support.

## Problem Statement

In Kubernetes, pods automatically receive ServiceAccount tokens mounted at a well-known path (`/var/run/secrets/kubernetes.io/serviceaccount/`). Applications use these tokens to authenticate to external services like Vault, SPIFFE/SPIRE, or other systems that trust Kubernetes ServiceAccount tokens.

KubeVirt VMs lack this capability:

- VMs do not automatically receive ServiceAccount tokens
- No standard mechanism exists for VMs to obtain Kubernetes identity
- Existing KubeVirt mount mechanisms have significant limitations (see below)

This creates a gap where VMs cannot participate in Kubernetes-native workload identity patterns.

### Limitations of Existing Mount Mechanisms

KubeVirt currently offers two ways to expose secrets/tokens to VMs:

| Method | How it Works | Limitations |
|--------|--------------|-------------|
| **Disk (ISO)** | Creates ISO image attached as virtual disk | Static at boot time, no token refresh |
| **VirtioFS** | Shared filesystem via virtiofsd | Linux kernel 5.4+ required, Windows support is tech preview only, ServiceAccount volumes not supported |

Neither method provides a universal, cross-platform solution with token refresh capabilities.

## Goals

1. **ServiceAccount binding**: VMs can be associated with a Kubernetes ServiceAccount
2. **Token availability**: VMs can access their ServiceAccount token via HTTP endpoint at 169.254.169.254
3. **Token refresh**: Tokens are automatically rotated before expiry
4. **Cross-platform support**: Works on any guest OS with TCP/IP networking (Linux, Windows, FreeBSD, etc.)
5. **External service authentication**: VMs can authenticate to services that trust Kubernetes SA tokens

## Non-Goals

- Full AWS/Azure/GCP IMDS API compatibility
- Cloud-init metadata (userdata, network config) - may be added later
- Node-level metadata exposure
- Multi-tenancy within a single VM
- Filesystem-based token mounting (virtiofs) - out of scope due to OS compatibility issues

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│  virt-launcher Pod                                                  │
│                                                                     │
│  ┌──────────────────────┐      ┌──────────────────────────────────┐ │
│  │  compute container   │      │  imds-sidecar                    │ │
│  │                      │      │                                  │ │
│  │  ┌────────────────┐  │      │  ┌────────────────────────────┐  │ │
│  │  │      VM        │  │      │  │  Projected SA Token        │  │ │
│  │  │  (any OS)      │  │      │  │  (auto-rotated by kubelet) │  │ │
│  │  │                │  │      │  └─────────────┬──────────────┘  │ │
│  │  │  ┌──────────┐  │  │      │                │                 │ │
│  │  │  │   App    │  │  │      │                ▼                 │ │
│  │  │  └────┬─────┘  │  │      │  ┌────────────────────────────┐  │ │
│  │  │       │        │  │      │  │      IMDS Server           │  │ │
│  │  │       │ HTTP   │  │      │  │   169.254.169.254:80       │  │ │
│  │  │       │        │◄─────────► │                            │  │ │
│  │  │       ▼        │  │      │  │   GET /v1/token            │  │ │
│  │  │   ┌────────┐   │  │      │  │   GET /v1/identity         │  │ │
│  │  │   │ curl   │   │  │      │  └────────────────────────────┘  │ │
│  │  │   │ wget   │   │  │      │                                  │ │
│  │  │   │ SDK    │   │  │      │                                  │ │
│  │  │   └────────┘   │  │      │                                  │ │
│  │  └────────────────┘  │      │                                  │ │
│  └──────────────────────┘      └──────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────────┘
```

### Why IMDS over Filesystem Mounts

| Aspect | IMDS (HTTP) | Disk (ISO) | VirtioFS |
|--------|-------------|------------|----------|
| Linux support | ✅ Any version | ✅ Any version | ⚠️ Kernel 5.4+ |
| Windows support | ✅ Any version | ✅ Any version | ⚠️ Tech preview |
| FreeBSD/other | ✅ Any with TCP/IP | ✅ If ISO works | ❌ Not supported |
| Token refresh | ✅ Always fresh | ❌ Static | ⚠️ If SA supported |
| Guest requirements | HTTP client only | Mount capability | Kernel module + drivers |
| ServiceAccount | ✅ Supported | ✅ Supported | ❌ Not supported |

**IMDS is the only method that provides universal OS support with token refresh.**

### Components

#### 1. IMDS Sidecar Container

A lightweight container that runs alongside the compute container in the virt-launcher pod.

**Responsibilities:**
- Serve HTTP requests on 169.254.169.254
- Read ServiceAccount token from projected volume
- Expose token and identity information via REST API

**Inputs:**
- Projected ServiceAccount token volume (mounted by Kubernetes)
- VM metadata from downward API (namespace, name, labels)

#### 2. Mutating Webhook

A Kubernetes mutating admission webhook that modifies VirtualMachine/VirtualMachineInstance resources.

**Responsibilities:**
- Detect VMs that opt-in to IMDS (via annotation)
- Inject imds-sidecar container specification
- Configure projected ServiceAccount token volume
- Configure network routing for 169.254.169.254

**Trigger:**
- Annotation: `imds.kubevirt.io/enabled: "true"`

#### 3. Network Configuration

The sidecar must intercept traffic destined for 169.254.169.254 from the VM.

##### KubeVirt Networking Modes

KubeVirt supports multiple networking modes with different traffic flows:

| Mode | How it Works |
|------|--------------|
| **Masquerade** | VM traffic NAT'd through pod network namespace |
| **Bridge** | VM directly bridged to pod/external network |
| **SR-IOV** | Direct hardware passthrough |

All modes use a Linux bridge (e.g., `k6t-eth0`) to connect the VM's tap device to the network. The difference is what else is attached to that bridge (NAT gateway, external uplink, or SR-IOV VF).

##### Unified veth Approach

Rather than implementing mode-specific solutions, we use a unified approach: **attach a veth pair to the VM bridge**. This works for all networking modes because the bridge exists in all cases.

```
┌─────────────────────────────────────────────────────────────────────┐
│  virt-launcher Pod                                                  │
│                                                                     │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │  Bridge (k6t-eth0)                                            │  │
│  │       │              │                  │                     │  │
│  │     tap0         (uplink)          veth-imds-br               │  │
│  │    (to VM)    (NAT, eth0, or VF)   (to sidecar)               │  │
│  │       │              │                  │                     │  │
│  └───────│──────────────│──────────────────│─────────────────────┘  │
│          │              │                  │                        │
│          ▼              ▼                  ▼                        │
│  ┌──────────────┐  ┌─────────────┐  ┌────────────────────────────┐  │
│  │  VM (guest)  │  │  External   │  │  imds-sidecar              │  │
│  │              │  │  Network    │  │                            │  │
│  │  curl 169.254│  │             │  │  veth-imds                 │  │
│  │  .169.254/.. │──┼─────────────┼──►  169.254.169.254           │  │
│  │              │  │             │  │                            │  │
│  └──────────────┘  └─────────────┘  │  IMDS Server listening     │  │
│                                     └────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

The VM sees `169.254.169.254` as another device on the same Layer 2 segment, regardless of which networking mode is in use.

##### Implementation

The IMDS sidecar init container performs these steps:

1. **Create veth pair:**
   ```bash
   ip link add veth-imds type veth peer name veth-imds-br
   ```

2. **Attach one end to VM bridge:**
   ```bash
   # Bridge name varies: k6t-eth0, k6t-net0, etc.
   ip link set veth-imds-br master k6t-eth0
   ip link set veth-imds-br up
   ```

3. **Configure IMDS endpoint on other end:**
   ```bash
   ip addr add 169.254.169.254/32 dev veth-imds
   ip link set veth-imds up
   ```

4. **Start IMDS server listening on veth-imds interface**

##### Requirements

- **NET_ADMIN capability**: Required to create veth pairs and attach to bridge. This is acceptable because the sidecar is an admin component that VM workloads cannot access directly.
- **Bridge name discovery**: Sidecar must discover the VM bridge name (via environment variable or auto-detection)
- **Timing**: Sidecar init must run after the bridge is created but before VM starts

## API Design

### Base URL

```
http://169.254.169.254
```

### Required Header

All requests (except `/healthz`) must include the `Metadata: true` header:

```bash
curl -H "Metadata: true" http://169.254.169.254/v1/token
```

This header is required to protect against SSRF (Server-Side Request Forgery) attacks, following the same pattern as Azure IMDS. Requests without this header receive a `400 Bad Request` response.

### Endpoints

#### GET /v1/identity

Returns information about the VM's Kubernetes identity.

**Response:**
```json
{
  "namespace": "default",
  "serviceAccountName": "my-app-sa",
  "vmName": "my-app-vm",
  "podName": "virt-launcher-my-app-vm-abc123"
}
```

#### GET /v1/token

Returns the current ServiceAccount token.

**Response:**
```json
{
  "token": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
  "expirationTimestamp": "2026-01-16T12:00:00Z"
}
```

The `expirationTimestamp` is parsed from the JWT's `exp` claim.

#### GET /healthz

Health check endpoint.

**Response:**
```
200 OK
```

### Error Responses

```json
{
  "error": "token_expired",
  "message": "ServiceAccount token has expired and could not be refreshed"
}
```

| Status Code | Meaning |
|-------------|---------|
| 200 | Success |
| 400 | Bad request (missing `Metadata: true` header) |
| 404 | Endpoint not found |
| 500 | Internal error (token unavailable, etc.) |
| 503 | Service not ready |

## Token Handling

### Projected ServiceAccount Tokens

The sidecar uses Kubernetes projected ServiceAccount tokens:

```yaml
volumes:
  - name: sa-token
    projected:
      sources:
        - serviceAccountToken:
            path: token
            expirationSeconds: 3600
```

**Benefits:**
- Automatic rotation by kubelet before expiry
- Bound to pod identity (more secure than legacy tokens)
- Support for custom audiences via TokenRequest API

### Token Refresh

The IMDS approach provides automatic token refresh:

1. Kubelet rotates the projected token before expiry
2. IMDS sidecar reads the current token on each request (no caching needed)
3. VM always receives a valid, fresh token via HTTP
4. No guest-side file watching or refresh logic needed

This is a significant advantage over filesystem-based approaches where the guest must handle token refresh.

### Design Decisions

**No token caching:** The sidecar reads the token file on each request. Since the file is small (~1-2KB) and stored in tmpfs (memory-backed), this is effectively instant and guarantees the freshest token.

**No custom audience support:** The projected token uses the default Kubernetes API audience. Services like Vault can be configured to accept this audience. Dynamic audience support would require TokenRequest API permissions and adds complexity without significant benefit for most use cases.

## VM Specification

### Opt-in via Annotation

```yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: my-app-vm
  annotations:
    imds.kubevirt.io/enabled: "true"
spec:
  template:
    spec:
      serviceAccountName: my-app-sa  # ServiceAccount to use
      domain:
        devices:
          disks:
            - name: rootdisk
              disk:
                bus: virtio
      volumes:
        - name: rootdisk
          containerDisk:
            image: quay.io/containerdisks/ubuntu:22.04
```

### Configuration Options

| Annotation | Default | Description |
|------------|---------|-------------|
| `imds.kubevirt.io/enabled` | `"false"` | Enable IMDS sidecar injection |
| `imds.kubevirt.io/bridge-name` | (auto-detect) | Override VM bridge name (e.g., `k6t-eth0`) |

## Security Considerations

### Network Isolation

- IMDS endpoint (169.254.169.254) is link-local, not routable outside the pod
- Only the VM within the same pod can reach the IMDS sidecar
- No authentication required (same trust model as cloud provider IMDS)

### Token Scope

- Tokens are scoped to the specified ServiceAccount
- Follow principle of least privilege when assigning ServiceAccounts to VMs
- Use separate ServiceAccounts for different workloads

### Sidecar Permissions

The IMDS sidecar requires:

- **NET_ADMIN capability**: Required to create veth pairs and attach to the VM bridge. This is acceptable because:
  - The sidecar is an infrastructure component, not user-accessible
  - VM workloads can only interact via HTTP at `169.254.169.254`
  - The virt-launcher pod already runs privileged containers for QEMU/KVM
  - NET_ADMIN scope is limited to the pod's network namespace
- **Read access to projected token volume**: Automatic via volume mount
- **TokenRequest API access**: Only if custom audiences are needed
- **No other Kubernetes API access**: Minimizes attack surface

### Comparison to Pod Security

| Aspect | Pod | VM with IMDS |
|--------|-----|--------------|
| Token auto-mount | Default on | Explicit opt-in |
| Token path | Fixed filesystem | HTTP endpoint |
| Network access to IMDS | N/A | Link-local only |
| Token rotation | Automatic | Automatic |

## Guest OS Compatibility

IMDS works on any guest OS that supports:
- TCP/IP networking
- HTTP client (curl, wget, PowerShell, or any HTTP library)

### Linux

```bash
# Using curl
TOKEN=$(curl -s -H "Metadata: true" http://169.254.169.254/v1/token | jq -r .token)

# Using wget
TOKEN=$(wget -qO- --header="Metadata: true" http://169.254.169.254/v1/token | jq -r .token)
```

### Windows

```powershell
# Using PowerShell
$response = Invoke-RestMethod -Headers @{"Metadata"="true"} -Uri "http://169.254.169.254/v1/token"
$token = $response.token

# Using curl (if installed)
$token = (curl.exe -s -H "Metadata: true" http://169.254.169.254/v1/token | ConvertFrom-Json).token
```

### FreeBSD / Other Unix

```sh
# Using fetch
TOKEN=$(fetch -qo - --header="Metadata: true" http://169.254.169.254/v1/token | jq -r .token)

# Using curl (if installed)
TOKEN=$(curl -s -H "Metadata: true" http://169.254.169.254/v1/token | jq -r .token)
```

## Usage Examples

### Example 1: Vault Authentication (Linux)

```bash
# Inside VM
TOKEN=$(curl -s -H "Metadata: true" "http://169.254.169.254/v1/token" | jq -r .token)
vault write auth/kubernetes/login role="my-role" jwt="$TOKEN"
```

### Example 2: Vault Authentication (Windows)

```powershell
# Inside VM
$response = Invoke-RestMethod -Headers @{"Metadata"="true"} -Uri "http://169.254.169.254/v1/token"
vault write auth/kubernetes/login role="my-role" jwt="$($response.token)"
```

### Example 3: Kubernetes API Access

```bash
# Inside VM (Linux)
TOKEN=$(curl -s -H "Metadata: true" http://169.254.169.254/v1/token | jq -r .token)
APISERVER="https://kubernetes.default.svc"

curl -s -k \
  -H "Authorization: Bearer $TOKEN" \
  $APISERVER/api/v1/namespaces/default/pods
```

### Example 4: Custom Application

Any application can fetch tokens via HTTP:

```python
# Python example
import requests

def get_k8s_token():
    response = requests.get(
        "http://169.254.169.254/v1/token",
        headers={"Metadata": "true"}
    )
    return response.json()["token"]

# Use token to authenticate to Vault, SPIFFE, etc.
token = get_k8s_token()
```

## Future Considerations

### Cloud-Init Metadata

Extend IMDS to serve cloud-init compatible metadata:

```
GET /openstack/latest/meta_data.json
GET /openstack/latest/user_data
GET /openstack/latest/network_data.json
```

This would allow standard cloud images to initialize without NoCloud ISO.

### Instance Metadata

Expose additional VM metadata:

```
GET /v1/metadata
{
  "instanceId": "vm-abc123",
  "hostname": "my-app-vm",
  "zone": "rack-1",
  "labels": {
    "app": "my-app",
    "env": "production"
  }
}
```

### Multiple Audiences

Pre-configure multiple audiences for different services:

```yaml
annotations:
  imds.kubevirt.io/audiences: "vault,spiffe,custom-service"
```

### Integration with External Secret Operators

Potential integration with external-secrets, cert-manager, or other operators that could inject additional credentials into the IMDS response.

## References

- [AWS IMDS Documentation](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-instance-metadata.html)
- [KubeVirt ServiceAccount Issue #1541](https://github.com/kubevirt/kubevirt/issues/1541)
- [KubeVirt ServiceAccount Issue #13311](https://github.com/kubevirt/kubevirt/issues/13311)
- [KubeVirt Disks and Volumes](https://kubevirt.io/user-guide/storage/disks_and_volumes/)
- [KubeVirt Interfaces and Networks](https://kubevirt.io/user-guide/network/interfaces_and_networks/)
- [KubeVirt Network Deep Dive](https://kubevirt.io/2018/KubeVirt-Network-Deep-Dive.html)
- [Kubernetes Projected ServiceAccount Tokens](https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/#serviceaccount-token-volume-projection)
- [Windows VirtioFS Support](https://github.com/virtio-win/kvm-guest-drivers-windows/wiki/Virtiofs:-Shared-file-system)
