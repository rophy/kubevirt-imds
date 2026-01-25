# Windows VM Support for KubeVirt IMDS

This document describes how to test IMDS with Windows VMs in KubeVirt.

## Overview

KubeVirt IMDS supports Windows VMs. The IMDS sidecar is injected into the virt-launcher pod and provides the same endpoints to Windows guests as it does to Linux guests.

## Preparing a Windows Image

### Option 1: Official Microsoft Evaluation VHD (Recommended)

Microsoft provides official evaluation VHDs with known credentials from the [Microsoft Evaluation Center](https://www.microsoft.com/en-us/evalcenter/evaluate-windows-server-2022).

#### Download

1. Visit https://www.microsoft.com/en-us/evalcenter/evaluate-windows-server-2022
2. Select "Download the VHD" (64-bit)
3. Fill out the registration form
4. Download the VHD file

**Default credentials** for Microsoft evaluation VHDs vary by version:
- Windows Server 2012 R2 Preview: `Administrator` / `R2Preview!`
- Windows Server 2022: Check the download page or email confirmation

#### Alternative: Archive.org Mirror

If you cannot access Microsoft Evaluation Center, a community mirror is available:

```bash
mkdir -p /tmp/winsrv2022
cd /tmp/winsrv2022

# Download compressed VHD (~2GB) from archive.org
curl -L -o winsrv2022.vhd.gz \
  "https://archive.org/download/winsrv2022-data-x64-us.vhd_202205/winsrv2022-data-x64-us.vhd.gz"

# Decompress (~11GB uncompressed)
gunzip winsrv2022.vhd.gz
```

**Note:** The archive.org image credentials are documented as `Administrator` / `nat.ee` but may not work. For reliable testing, use the official Microsoft evaluation VHD.

#### Convert to QCOW2

```bash
# Convert VHD to QCOW2 format (~4.9GB)
qemu-img convert -O qcow2 winsrv2022.vhd winsrv2022.qcow2

# Remove original VHD to save space
rm winsrv2022.vhd
```

#### Build Container Disk

```bash
cat > Dockerfile << 'EOF'
FROM scratch
ADD --chown=107:107 winsrv2022.qcow2 /disk/winsrv2022.qcow2
EOF

docker build -t winsrv2022-containerdisk:latest .
```

#### Load into Kind Cluster

```bash
kind load docker-image winsrv2022-containerdisk:latest --name kind
```

### Option 2: Cloudbase Windows Server 2012 R2 (Recommended for Headless Testing)

[Cloudbase Solutions](https://cloudbase.it/windows-cloud-images/) provides Windows Server 2012 R2 evaluation images with:
- **cloudbase-init** pre-installed (Windows equivalent of cloud-init)
- **WinRM enabled** for headless remote management
- **VirtIO drivers** included for KVM

This is ideal for automated testing without needing RDP/console access.

#### Download

1. Visit https://cloudbase.it/windows-cloud-images/
2. Scroll to download section
3. Accept Microsoft EULA
4. Complete reCAPTCHA verification
5. Download the KVM (qcow2) image

**Note:** The download requires manual browser interaction (EULA + CAPTCHA). The image cannot be downloaded via curl/wget directly.

Save the downloaded file to:
```bash
/home/rophy/projects/kubevirt-imds/tmp/windows_server_2012_r2_standard_eval_kvm.qcow2.gz
```

#### Extract and Build Container Disk

```bash
cd /home/rophy/projects/kubevirt-imds/tmp

# Decompress
gunzip windows_server_2012_r2_standard_eval_kvm.qcow2.gz

# Build container disk
cat > Dockerfile.win2012r2 << 'EOF'
FROM scratch
ADD --chown=107:107 windows_server_2012_r2_standard_eval_kvm.qcow2 /disk/windows.qcow2
EOF

docker build -f Dockerfile.win2012r2 -t win2012r2-containerdisk:latest .
kind load docker-image win2012r2-containerdisk:latest --name kind
```

#### User Access

cloudbase-init creates an `Admin` user during instance initialization. The password can be set via cloud-init userdata or retrieved via OpenStack nova API.

### Option 3: Vagrant Box with WinRM

Pre-built Windows Vagrant boxes with WinRM are available:

- [peru/windows-server-2012_r2-standard-x64-eval](https://app.vagrantup.com/peru/boxes/windows-server-2012_r2-standard-x64-eval) - libvirt/virtualbox
- [rgl/windows-2022](https://app.vagrantup.com/rgl) - Windows 2022 with WinRM, UAC disabled
- [jborean93/WindowsServer2022](https://app.vagrantup.com/jborean93) - WinRM over SSL

Vagrant libvirt boxes contain qcow2 images that can be extracted:

```bash
vagrant box add peru/windows-server-2012_r2-standard-x64-eval --provider libvirt
# Image located at: ~/.vagrant.d/boxes/peru-VAGRANTSLASH-.../libvirt/box.img
```

### Option 4: Tiny11/Nano11 (Lightweight Windows 10/11)

For testing with minimal resource usage, community-built lightweight Windows images can be used:

- **Tiny11** (~2.8GB ISO): Stripped-down Windows 11
- **Nano11** (~2.1GB ISO): Even smaller, keeps only essential drivers

Note: These are ISOs that require installation, unlike the pre-installed VHD above.

## Deploying a Windows VM

Create a VirtualMachine manifest with IMDS enabled:

```yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: winsrv2022-test
  namespace: kubevirt
spec:
  runStrategy: Always
  template:
    metadata:
      annotations:
        imds.kubevirt.io/enabled: "true"
    spec:
      domain:
        cpu:
          cores: 2
        memory:
          guest: 4Gi
        devices:
          disks:
            - name: rootdisk
              disk:
                bus: sata  # Use SATA for Windows compatibility without VirtIO
          interfaces:
            - name: default
              masquerade: {}
        machine:
          type: q35
      networks:
        - name: default
          pod: {}
      volumes:
        - name: rootdisk
          containerDisk:
            image: winsrv2022-containerdisk:latest
            imagePullPolicy: Never  # Required for kind-loaded images
```

Apply the manifest:

```bash
kubectl apply -f winsrv2022-vm.yaml
```

## Verifying IMDS

### From Pod Network Namespace

Test IMDS endpoints from the compute container:

```bash
# Get the pod name
POD=$(kubectl get pod -n kubevirt -l vm.kubevirt.io/name=winsrv2022-test \
  -o jsonpath='{.items[0].metadata.name}')

# Test /v1/token endpoint
kubectl exec -n kubevirt $POD -c compute -- \
  curl -sf -H "Metadata: true" http://169.254.169.254/v1/token

# Test /v1/identity endpoint
kubectl exec -n kubevirt $POD -c compute -- \
  curl -sf -H "Metadata: true" http://169.254.169.254/v1/identity
```

Expected output for `/v1/identity`:
```json
{"namespace":"kubevirt","serviceAccountName":"default","vmName":"winsrv2022-test"}
```

### From Inside Windows Guest

To access IMDS from inside the Windows guest, the VM needs:

1. **VirtIO network drivers** for the masquerade interface
2. **Network connectivity** to reach 169.254.169.254

From PowerShell inside the Windows VM:

```powershell
# Test IMDS endpoint
$headers = @{"Metadata" = "true"}
Invoke-RestMethod -Uri "http://169.254.169.254/v1/identity" -Headers $headers
```

## VirtIO Drivers

For optimal performance and in-guest IMDS access, install VirtIO drivers:

1. Download the VirtIO driver ISO from [Fedora](https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/stable-virtio/virtio-win.iso)
2. Attach as a secondary disk or CD-ROM
3. Install drivers from within Windows

Alternatively, use `virtio` bus instead of `sata` for disks if drivers are pre-installed:

```yaml
devices:
  disks:
    - name: rootdisk
      disk:
        bus: virtio  # Requires VirtIO drivers
```

## KubeVirt Version Compatibility

The IMDS webhook supports both old and new KubeVirt label conventions:

| KubeVirt Version | Pod Label |
|------------------|-----------|
| < 1.7 | `kubevirt.io/domain` |
| >= 1.7 | `vm.kubevirt.io/name` |

The webhook automatically detects which label is present and works with both.

## Troubleshooting

### IMDS Sidecar Not Injected

Check if the webhook is running:
```bash
kubectl get pods -n kubevirt-imds
```

Check webhook logs:
```bash
kubectl logs -n kubevirt-imds deploy/imds-webhook
```

Verify the VM has the annotation:
```bash
kubectl get vm -n kubevirt <vm-name> -o jsonpath='{.spec.template.metadata.annotations}'
```

### Certificate Errors

If you see certificate verification errors, regenerate and apply the CA bundle:

```bash
# Regenerate certificates
KUBE_CONTEXT=kind-kind ./hack/generate-certs.sh

# Get the CA bundle from output and patch the webhook
CA_BUNDLE="<base64-ca-bundle-from-output>"
kubectl patch mutatingwebhookconfiguration imds-webhook \
  --type='json' \
  -p="[{\"op\": \"add\", \"path\": \"/webhooks/0/clientConfig/caBundle\", \"value\": \"$CA_BUNDLE\"}]"

# Restart the webhook
kubectl rollout restart deployment/imds-webhook -n kubevirt-imds
```

### Windows Not Booting

- Ensure sufficient memory (4Gi recommended for Windows Server)
- Use `bus: sata` for disk if VirtIO drivers are not installed
- Check VM console: `virtctl console <vm-name>`

## OpenStack Metadata Endpoints (cloudbase-init)

KubeVirt IMDS provides OpenStack-compatible metadata endpoints for cloudbase-init on Windows:

| Endpoint | Description |
|----------|-------------|
| `/openstack/latest/meta_data.json` | Instance metadata (uuid, hostname, name) |
| `/openstack/latest/user_data` | User-provided initialization scripts |

### Passing User-Data to Windows VMs

Add the `imds.kubevirt.io/user-data` annotation to provide initialization scripts:

```yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: win-vm
  namespace: kubevirt
spec:
  template:
    metadata:
      annotations:
        imds.kubevirt.io/enabled: "true"
        imds.kubevirt.io/user-data: |
          #ps1_sysnative
          net user Administrator "MySecurePassword123!"
```

### Cloudbase-init Configuration Requirements

For cloudbase-init to properly process user-data scripts, the Windows image must have these plugins enabled in `cloudbase-init.conf`:

```ini
[DEFAULT]
plugins=cloudbaseinit.plugins.common.mtu.MTUPlugin,
        cloudbaseinit.plugins.windows.createuser.CreateUserPlugin,
        cloudbaseinit.plugins.common.setuserpassword.SetUserPasswordPlugin,
        cloudbaseinit.plugins.common.userdata.UserDataPlugin

# For user-data script execution
[UserDataPlugin]
user_data_plugins=cloudbaseinit.plugins.common.userdataplugins.shellscript.ShellScriptPlugin,
                  cloudbaseinit.plugins.common.userdataplugins.powershellscript.PowershellScriptPlugin

# For password setting via admin_pass in meta_data.json
[SetUserPasswordPlugin]
inject_user_password=true
```

### Testing Results with Cloudbase Windows Server 2012 R2 Image

Testing with the Cloudbase-provided Windows Server 2012 R2 evaluation image revealed:

| Feature | Status | Notes |
|---------|--------|-------|
| IMDS metadata fetch | ✅ Working | cloudbase-init fetches `/openstack/latest/meta_data.json` |
| IMDS user_data fetch | ✅ Working | cloudbase-init fetches `/openstack/latest/user_data` |
| Password via `admin_pass` | ❌ Not working | Image lacks `inject_user_password=true` |
| Password via user-data script | ⚠️ Partial | Image processes user-data but doesn't execute PowerShell scripts |
| cloudbase-init.conf location | ❓ Unknown | Not at standard path (`C:\Program Files\Cloudbase Solutions\...`) |

**Key Findings:**

1. **Without user-data annotation**: Windows shows 2-field password change screen (New password + Confirm)
2. **With user-data annotation**: Windows shows 3-field password change screen (Current password + New password + Confirm)

This indicates cloudbase-init IS processing the user-data and setting some password, but:
- The `UserDataPlugin` for PowerShell script execution is likely disabled
- The password set is unknown (possibly derived from user-data content)

**Recommendation**: For production use, build custom Windows images with properly configured cloudbase-init at the standard installation path.

## Current Status

**Status: Awaiting manual image download**

Windows VM testing requires a pre-built image with WinRM enabled for headless testing. The recommended Cloudbase image requires manual download due to EULA acceptance and reCAPTCHA verification that cannot be automated.

### Next Steps

1. Manually download Cloudbase Windows Server 2012 R2 image from https://cloudbase.it/windows-cloud-images/
2. Save to `tmp/windows_server_2012_r2_standard_eval_kvm.qcow2.gz`
3. Follow the "Extract and Build Container Disk" steps above
4. Deploy VM and test IMDS endpoints

### Alternative: Test from Pod Namespace

IMDS can be tested from the pod's network namespace without logging into Windows:

```bash
# Get the virt-launcher pod
POD=$(kubectl get pod -n kubevirt -l vm.kubevirt.io/name=<vm-name> \
  -o jsonpath='{.items[0].metadata.name}')

# Test IMDS from compute container (same network namespace as VM)
kubectl exec -n kubevirt $POD -c compute -- \
  curl -sf -H "Metadata: true" http://169.254.169.254/v1/identity
```

This validates IMDS functionality without requiring Windows guest access.
