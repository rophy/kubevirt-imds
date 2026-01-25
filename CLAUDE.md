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
├── hack/                # Helper scripts
└── tmp/                 # Downloaded assets (gitignored)
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

## Downloads and Temporary Files

When downloading large files (VM images, ISOs, etc.), always download to the project's `tmp/` folder:

```bash
# Good - download to project tmp folder
curl -L -o /home/rophy/projects/kubevirt-imds/tmp/image.qcow2 <url>

# Bad - download to /tmp (easy to lose track)
curl -L -o /tmp/image.qcow2 <url>
```

This keeps all downloaded assets in one place and makes cleanup easier.

## Testing IMDS from VMs

### Avoid Fedora for IMDS Testing
Fedora's SELinux blocks `qemu-guest-agent` (running as `virt_qemu_ga_t`) from making network connections. This causes `curl` via guest-agent to fail with "Permission denied" even when IMDS is working correctly.

**Use cirros instead**: `quay.io/kubevirt/cirros-container-disk-demo` has no SELinux and works reliably.

### Alternative: Test from compute container
The `compute` container shares the network namespace with the VM pod:
```bash
POD=$(kubectl --context kind-kind get pods -n kubevirt -l kubevirt.io/domain=<vm-name> -o jsonpath='{.items[0].metadata.name}')
kubectl --context kind-kind exec -n kubevirt "$POD" -c compute -- curl -s -H "Metadata: true" http://169.254.169.254/v1/identity
```

## IMDS Network Architecture

### How VMs reach IMDS (169.254.169.254)

**Normal case (VM has gateway):**
1. VM sends packet to `169.254.169.254:80`
2. VM routes through gateway (`10.0.2.1` in masquerade mode)
3. Gateway (`k6t-eth0`) receives the packet
4. Kernel sees `169.254.169.254` is a LOCAL address (on `veth-imds`)
5. Packet delivered to IMDS server socket

**Link-local only case (VM has no gateway):**
1. VM has only a link-local address (169.254.x.x), no gateway
2. VM ARPs for `169.254.169.254` on the local network
3. ARP responder (`internal/network/arp.go`) responds with veth-imds MAC
4. VM sends packet directly to veth-imds at L2
5. Packet delivered to IMDS server socket

### ARP Responder
The IMDS sidecar includes a userspace ARP responder for VMs that:
- Have only link-local addresses (APIPA/auto-configured)
- Have no DHCP and no gateway
- Need to reach IMDS before cloud-init configures networking

This is similar to how cloud providers (AWS, GCP, Azure) handle IMDS - they intercept at L2/hypervisor level so VMs can reach IMDS without any routing.

### Testing Containers
For cloud-init testing, use Ubuntu containerdisks (`quay.io/containerdisks/ubuntu:22.04`) which have full cloud-init support including netplan v2 network config.

Note: Ubuntu with link-local-only networking will wait at boot for `systemd-networkd-wait-online.service`. This is expected behavior - the network is "configured" but not "online" (no routable addresses).

## Windows VM VNC Testing

For testing Windows VMs without direct display access, use VNC proxy with vncdo for control and virtctl for screenshots.

### Setup VNC Proxy
```bash
# Start VNC proxy on fixed port 5901 (runs in background)
virtctl --context kind-kind vnc <vm-name> -n kubevirt --proxy-only --port 5901 &

# Verify it's listening
ss -tlnp | grep 5901
```

**Note:** The VNC proxy times out after 1 minute of no connections. Restart it before sending commands if needed.

### Control VM with vncdo
```bash
# Install vncdo (if not installed)
pip install vncdotool

# Send keyboard input (use double colon for port)
vncdo -s 127.0.0.1::5901 key space              # Wake from sleep
vncdo -s 127.0.0.1::5901 key ctrl-alt-del       # Ctrl+Alt+Del
vncdo -s 127.0.0.1::5901 type 'password123'     # Type text
vncdo -s 127.0.0.1::5901 key enter              # Press Enter

# Mouse control
vncdo -s 127.0.0.1::5901 move 580 300 click 1   # Move and left-click
```

### Take Screenshots
```bash
# Save screenshot to file
virtctl --context kind-kind vnc screenshot <vm-name> -n kubevirt --file=/path/to/screenshot.png
```

### Example: Login to Windows VM
```bash
# 1. Start VNC proxy
virtctl --context kind-kind vnc win2012r2-bridge -n kubevirt --proxy-only --port 5901 &
sleep 3

# 2. Wake screen and send Ctrl+Alt+Del
vncdo -s 127.0.0.1::5901 key ctrl-alt-del
sleep 2

# 3. Take screenshot to see login screen
virtctl --context kind-kind vnc screenshot win2012r2-bridge -n kubevirt --file=tmp/win-login.png

# 4. Click on user, type password, press Enter
vncdo -s 127.0.0.1::5901 move 580 300 click 1   # Click Administrator
sleep 1
vncdo -s 127.0.0.1::5901 type 'MyPassword123!'
vncdo -s 127.0.0.1::5901 key enter
```
