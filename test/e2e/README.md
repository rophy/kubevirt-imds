# E2E Tests for KubeVirt IMDS

This directory contains end-to-end tests for the KubeVirt IMDS project.

## Prerequisites

Before running the E2E tests, ensure you have the following:

1. **Kind cluster** running with nested virtualization support
2. **KubeVirt** installed and ready
3. **kubectl** configured to access the cluster
4. **virtctl** CLI tool installed

### Setting Up the Test Environment

```bash
# Create a kind cluster (if not exists)
kind create cluster --config hack/kind-config.yaml

# Install KubeVirt
kubectl apply -f deploy/kubevirt/kubevirt-operator.yaml
kubectl apply -f deploy/kubevirt/kubevirt-cr.yaml

# Wait for KubeVirt to be ready
kubectl wait --for=jsonpath='{.status.phase}'=Deployed \
  kubevirt/kubevirt -n kubevirt --timeout=300s
```

## Running the Tests

```bash
# Run the full E2E test suite
./test/e2e/run.sh
```

The script will:
1. Build Docker images for the IMDS server and webhook
2. Load images into the kind cluster
3. Deploy the mutating webhook
4. Run all test cases
5. Clean up test resources

## Test Cases

### Test 1: Basic IMDS Functionality

Creates a single VM with IMDS enabled and verifies:

| Check | Description |
|-------|-------------|
| Sidecar Injection | `imds-server` container is present in the VM pod |
| `/healthz` endpoint | Returns "OK" |
| `/v1/identity` endpoint | Returns VM metadata (namespace, vmName, etc.) |
| `/v1/token` endpoint | Returns ServiceAccount token |

### Test 2: Network Namespace Isolation

Creates two VMs simultaneously and verifies that 169.254.169.254 is isolated per-pod:

```
┌─────────────────────┐    ┌─────────────────────┐
│      VM Pod A       │    │      VM Pod B       │
│                     │    │                     │
│  IMDS: vmName=A     │    │  IMDS: vmName=B     │
│  169.254.169.254    │    │  169.254.169.254    │
│                     │    │                     │
│  (isolated network  │    │  (isolated network  │
│   namespace)        │    │   namespace)        │
└─────────────────────┘    └─────────────────────┘
        │                          │
        └── Cannot reach ──────────┘
```

| Check | Description |
|-------|-------------|
| Identity Isolation | VM A's IMDS returns `vmName: testvm-imds-a` |
| Identity Isolation | VM B's IMDS returns `vmName: testvm-imds-b` |
| Repeated Checks | 10 iterations to confirm no packet leakage |

**Why this matters**: If network namespaces weren't properly isolated, VM A might receive responses meant for VM B (or vice versa). This test confirms that each pod's 169.254.169.254 is truly local.

### Test 3: Network Traffic Sniffing

Deploys a sniffer pod with `hostNetwork: true` to capture traffic at the node level and prove packets don't leak:

```
┌─────────────────────────────────────────────────────────────────┐
│                         Node                                    │
│                                                                 │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────┐  │
│  │   VM Pod A   │  │   VM Pod B   │  │   Sniffer Pod        │  │
│  │   (netns 1)  │  │   (netns 2)  │  │   (hostNetwork)      │  │
│  │              │  │              │  │                      │  │
│  │ 169.254.     │  │ 169.254.     │  │  tcpdump -i any      │  │
│  │ 169.254      │  │ 169.254      │  │  host 169.254.169.254│  │
│  └──────────────┘  └──────────────┘  └──────────────────────┘  │
│         │                 │                     │               │
│         └────── Should NOT appear ──────────────┘               │
└─────────────────────────────────────────────────────────────────┘
```

| Check | Description |
|-------|-------------|
| Packet Capture | No 169.254.169.254 traffic on node interfaces (eth0, cni0) |
| Third-Party Unreachable | Sniffer pod cannot curl 169.254.169.254 |

**What this proves**:
1. **Traffic stays in namespace**: 169.254.169.254 packets don't appear on host-level interfaces
2. **No cross-pod leakage**: A pod without IMDS cannot reach 169.254.169.254

## Test Configuration

The following variables can be modified in `run.sh`:

| Variable | Default | Description |
|----------|---------|-------------|
| `TEST_NAMESPACE` | `kubevirt` | Namespace for test VMs |
| `TIMEOUT_SECONDS` | `300` | Timeout for waiting operations |

## Test Manifests

| File | Description |
|------|-------------|
| `deploy/test/vm-with-imds.yaml` | Single VM for basic functionality test |
| `deploy/test/two-vms-isolation.yaml` | Two VMs for namespace isolation test |
| `deploy/test/sniffer-pod.yaml` | hostNetwork pod for traffic capture |

## Cleanup

The test script automatically cleans up test VMs on exit. To manually clean up:

```bash
kubectl delete vm testvm-imds testvm-imds-a testvm-imds-b -n kubevirt --ignore-not-found
kubectl delete pod network-sniffer -n kubevirt --ignore-not-found
kubectl delete -f deploy/webhook/
```

## Troubleshooting

### VM pod stuck in Pending

Check if KubeVirt is ready:
```bash
kubectl get kubevirt -n kubevirt
```

### IMDS endpoints not reachable

Check the IMDS server logs:
```bash
# For single VM test
kubectl logs -n kubevirt -l kubevirt.io/domain=testvm-imds -c imds-server

# For isolation test
kubectl logs -n kubevirt -l kubevirt.io/domain=testvm-imds-a -c imds-server
kubectl logs -n kubevirt -l kubevirt.io/domain=testvm-imds-b -c imds-server
```

### Webhook not injecting sidecar

Verify the webhook is running:
```bash
kubectl get pods -n kubevirt-imds
kubectl logs -n kubevirt-imds -l app=imds-webhook
```

Check webhook configuration:
```bash
kubectl get mutatingwebhookconfiguration imds-webhook -o yaml
```

### Isolation test fails

If the isolation test reports wrong vmName, check:
1. Both pods are running in separate network namespaces
2. The bridge attachment is correct in each pod
3. No unexpected network policies are in place

```bash
# Inspect network interfaces in each pod
kubectl exec -n kubevirt <pod-a> -c imds-server -- ip addr
kubectl exec -n kubevirt <pod-b> -c imds-server -- ip addr
```
