# Cloud-Init Support in KubeVirt IMDS

This document describes the design and implementation of cloud-init NoCloud datasource support in KubeVirt IMDS.

## Overview

Cloud-init is the standard for customizing cloud instances at boot time. KubeVirt IMDS implements cloud-init's NoCloud datasource, enabling standard cloud images (Ubuntu Cloud, CentOS GenericCloud, etc.) to initialize automatically when running as KubeVirt VMs.

## How Cloud-Init Detection Works

Cloud-init uses a multi-stage detection process:

1. **DMI/SMBIOS Check (Before Network)**: Cloud-init reads DMI data from `/sys/class/dmi/id/` to detect the cloud provider. For NoCloud, it looks for a DMI serial number containing `ds=nocloud`.

2. **URL Probing (After Network)**: If DMI detection succeeds and specifies a URL (`s=http://...`), cloud-init fetches metadata from that URL.

This is why the DMI serial approach works: cloud-init reads DMI data early in boot (before network), finds the NoCloud marker, then fetches data from the IMDS URL once networking is available.

### DMI Serial Format

```
ds=nocloud;s=http://169.254.169.254/v1/
```

- `ds=nocloud`: Tells cloud-init to use the NoCloud datasource
- `s=http://169.254.169.254/v1/`: Specifies the HTTP seedfrom URL

## Design Decisions

### 1. NoCloud vs Cloud-Specific APIs

We chose to implement NoCloud rather than mimicking AWS, Azure, or GCP IMDS APIs because:

- **Simplicity**: NoCloud requires only 3 endpoints with simple YAML/text responses
- **Reliability**: Exact API compatibility with cloud providers is error-prone
- **Sufficiency**: NoCloud provides all necessary functionality for VM initialization

### 2. Header Exemption for Cloud-Init Endpoints

The `/v1/meta-data`, `/v1/user-data`, and `/v1/network-config` endpoints do NOT require the `Metadata: true` header, unlike `/v1/token` and `/v1/identity`.

**Reason**: Cloud-init cannot send custom HTTP headers when fetching from HTTP datasources. This is a limitation of cloud-init's NoCloud implementation.

**Security**: Link-local network isolation (169.254.169.254 only reachable from within the VM) provides SSRF protection. External attackers cannot reach these endpoints.

### 3. instance-id Format

The instance ID is formatted as `namespace-vmName` (e.g., `default-my-vm`) for cluster-wide uniqueness. Cloud-init uses this to determine if this is a new instance or a re-run.

### 4. User-Data via Annotation

User-data is provided via the `imds.kubevirt.io/user-data` annotation rather than a ConfigMap reference. This keeps the implementation simple while supporting typical cloud-config sizes.

### 5. DMI Serial as User Configuration

The webhook mutates Pods (virt-launcher), not VirtualMachine CRDs. Since DMI serial is configured in the VM spec's firmware settings, users must configure it manually.

## API Endpoints

| Endpoint | Content | Header Required |
|----------|---------|-----------------|
| `GET /v1/meta-data` | YAML: `instance-id`, `local-hostname` | No |
| `GET /v1/user-data` | Raw cloud-config from annotation | No |
| `GET /v1/network-config` | 404 (DHCP fallback) | No |

### GET /v1/meta-data

Returns instance metadata in YAML format:

```yaml
instance-id: default-my-vm
local-hostname: my-vm
```

### GET /v1/user-data

Returns the cloud-config content from the `imds.kubevirt.io/user-data` annotation. Returns 404 if not configured.

Example response:
```yaml
#cloud-config
users:
  - name: admin
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - ssh-rsa AAAA...
```

### GET /v1/network-config

Always returns 404. Cloud-init falls back to DHCP for network configuration, which is the standard behavior for KubeVirt VMs.

## User Configuration

### Complete Example

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
        imds.kubevirt.io/enabled: "true"
        imds.kubevirt.io/user-data: |
          #cloud-config
          users:
            - name: admin
              sudo: ALL=(ALL) NOPASSWD:ALL
              ssh_authorized_keys:
                - ssh-rsa AAAA...
          package_update: true
          packages:
            - curl
            - jq
    spec:
      serviceAccountName: my-service-account
      domain:
        firmware:
          serial: "ds=nocloud;s=http://169.254.169.254/v1/"
        devices:
          disks:
            - name: rootdisk
              disk:
                bus: virtio
        resources:
          requests:
            memory: 2Gi
      volumes:
        - name: rootdisk
          containerDisk:
            image: quay.io/containerdisks/ubuntu:22.04
```

### Configuration Steps

1. **Enable IMDS**: Add `imds.kubevirt.io/enabled: "true"` annotation
2. **Set DMI Serial**: Configure `spec.domain.firmware.serial` to `"ds=nocloud;s=http://169.254.169.254/v1/"`
3. **Provide User-Data** (optional): Add `imds.kubevirt.io/user-data` annotation with cloud-config content
4. **Use Cloud Image**: Use a cloud image that has cloud-init pre-installed

## Cloud-Init Boot Process

1. **init-local stage**: Cloud-init reads DMI serial, detects NoCloud datasource
2. **Network setup**: KubeVirt/DHCP configures VM networking
3. **init-network stage**: Cloud-init fetches:
   - `http://169.254.169.254/v1/meta-data` → Instance metadata
   - `http://169.254.169.254/v1/user-data` → User configuration
   - `http://169.254.169.254/v1/network-config` → 404 (uses DHCP)
4. **config/final stages**: Cloud-init applies the configuration

## Implementation Details

### Server Changes

The `Server` struct includes a `UserData` field:

```go
type Server struct {
    TokenPath          string
    Namespace          string
    VMName             string
    ServiceAccountName string
    ListenAddr         string
    UserData           string  // cloud-init user-data
    // ...
}
```

### Handler Registration

```go
mux.HandleFunc("/v1/meta-data", s.handleMetaData)
mux.HandleFunc("/v1/user-data", s.handleUserData)
mux.HandleFunc("/v1/network-config", s.handleNetworkConfig)
```

### Middleware Exemption

```go
exemptPaths := map[string]bool{
    "/healthz":           true,
    "/v1/meta-data":      true,
    "/v1/user-data":      true,
    "/v1/network-config": true,
}
```

### Webhook Injection

The webhook reads the `imds.kubevirt.io/user-data` annotation and passes it to the sidecar container via the `IMDS_USER_DATA` environment variable.

## Verification

### From Inside the VM

```bash
# Check meta-data
curl http://169.254.169.254/v1/meta-data
# Output: instance-id: default-my-vm
#         local-hostname: my-vm

# Check user-data
curl http://169.254.169.254/v1/user-data
# Output: #cloud-config
#         users:
#         ...

# Check network-config (should return 404)
curl -I http://169.254.169.254/v1/network-config
# Output: HTTP/1.1 404 Not Found
```

### Cloud-Init Logs

```bash
# Check cloud-init status
cloud-init status

# View cloud-init logs
cat /var/log/cloud-init.log
cat /var/log/cloud-init-output.log
```

## Future Enhancements

### VirtualMachine Webhook for Auto-Injection

Currently, users must manually configure the DMI serial in their VM spec. A future enhancement could add a VirtualMachine mutating webhook that:

1. Watches VirtualMachine CRDs (not just Pods)
2. When `imds.kubevirt.io/enabled: "true"` is present, automatically injects:
   ```yaml
   spec:
     template:
       spec:
         domain:
           firmware:
             serial: "ds=nocloud;s=http://169.254.169.254/v1/"
   ```

This would provide a fully automatic experience where users only need to add the annotation and user-data.

**Implementation considerations:**
- Requires a separate webhook configuration for VirtualMachine resources
- Must handle cases where user has already set firmware.serial
- Should preserve existing serial content if present

### ConfigMap Support for Large User-Data

For user-data larger than annotation limits (~256KB), support referencing a ConfigMap:

```yaml
annotations:
  imds.kubevirt.io/user-data-configmap: "my-vm-userdata"
```

### Vendor-Data Support

Add `/v1/vendor-data` endpoint for operator-provided configuration that supplements user-data.

## References

- [Cloud-Init NoCloud Documentation](https://cloudinit.readthedocs.io/en/latest/reference/datasources/nocloud.html)
- [Cloud-Init Datasource Detection](https://github.com/canonical/cloud-init/blob/main/cloudinit/sources/__init__.py)
- [KubeVirt Firmware Settings](https://kubevirt.io/api-reference/main/definitions.html#_v1_firmware)
