# KubeVirt IMDS - Development Guidelines

## Kubernetes Context Isolation

**IMPORTANT**: The host machine may run multiple projects with different Kubernetes clusters simultaneously. To prevent conflicts:

### Shell Scripts
- **NEVER** use bare `kubectl` commands that rely on the current context
- **ALWAYS** use explicit `--context` flag: `kubectl --context kind-<cluster-name> ...`
- Create a wrapper function for convenience:
  ```bash
  KUBE_CONTEXT="kind-${KIND_CLUSTER_NAME}"
  kctl() {
      kubectl --context "${KUBE_CONTEXT}" "$@"
  }
  ```

### Makefile
- Pass `KIND_CLUSTER_NAME` to kind commands: `kind load docker-image <image> --name $(KIND_CLUSTER_NAME)`
- Support `KUBE_CONTEXT` variable for kubectl commands

### Why This Matters
- Other projects may change the kubectl context at any time
- Bare `kubectl` commands will silently operate on the wrong cluster
- This causes hard-to-debug issues where resources appear/disappear unexpectedly

## Project Structure

```
kubevirt-imds/
├── cmd/
│   ├── imds-server/     # IMDS sidecar binary
│   └── imds-webhook/    # Mutating webhook binary
├── internal/
│   ├── imds/            # IMDS server logic
│   ├── network/         # veth/bridge network setup
│   └── webhook/         # Webhook mutation logic
├── deploy/
│   ├── webhook/         # Webhook deployment manifests
│   ├── kubevirt/        # KubeVirt installation manifests
│   └── test/            # Test VM manifests
├── test/
│   └── e2e/             # E2E test scripts
└── hack/                # Helper scripts
```

## Testing

### E2E Tests
```bash
# Run with default kind cluster named "kind"
./test/e2e/run.sh

# Run with custom cluster name
KIND_CLUSTER_NAME=my-cluster ./test/e2e/run.sh
```

### Unit Tests
```bash
make test
```

## Build

```bash
# Build binaries
make build

# Build Docker images
make docker-build-all

# Load into kind cluster
make kind-load-all KIND_CLUSTER_NAME=kind
```
