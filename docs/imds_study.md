# Instance Metadata Service (IMDS) Study

This document provides background on Instance Metadata Services, compares major cloud provider implementations, and offers recommendations for KubeVirt IMDS.

## What is IMDS?

An **Instance Metadata Service (IMDS)** is an HTTP-based service that provides virtual machines with information about themselves and their environment. It runs on a link-local IP address (`169.254.169.254`) accessible only from within the instance.

### Key Characteristics

- **Link-local access**: Available at `169.254.169.254`, not routable externally
- **No authentication**: Trusts the network boundary (only the VM can reach it)
- **HTTP-based**: Simple REST API, works with any HTTP client
- **Instance-scoped**: Each VM sees only its own metadata

### Common Data Categories

| Category | Description |
|----------|-------------|
| **Identity** | Instance ID, name, hostname |
| **Credentials** | Temporary security tokens for cloud APIs |
| **Placement** | Region, availability zone, rack |
| **Network** | IP addresses, MAC addresses, VPC info |
| **User Data** | Custom bootstrap scripts/config |
| **Tags/Labels** | User-defined key-value metadata |

## Why IMDS?

### The Problem for KubeVirt VMs

Kubernetes pods automatically receive ServiceAccount tokens at a well-known path (`/var/run/secrets/kubernetes.io/serviceaccount/`). Applications use these tokens to authenticate to:

- HashiCorp Vault
- SPIFFE/SPIRE
- Cloud provider APIs (via workload identity)
- Other Kubernetes-aware services

**KubeVirt VMs lack this capability.** Existing alternatives have limitations:

| Method | Limitation |
|--------|------------|
| **ISO Disk** | Static at boot, no token refresh |
| **VirtioFS** | Linux 5.4+ required, Windows tech preview only, SA volumes not supported |

### Why IMDS Solves This

| Aspect | IMDS Advantage |
|--------|----------------|
| **OS Compatibility** | Any OS with TCP/IP (Linux, Windows, FreeBSD) |
| **Token Refresh** | Always fresh - read on each request |
| **Guest Requirements** | HTTP client only (curl, wget, PowerShell) |
| **No Drivers** | No kernel modules or special drivers needed |
| **Cloud Familiarity** | Same pattern as AWS/Azure/GCP |

## Cloud Provider IMDS Comparison

### Base Endpoints

| Provider | Endpoint | Required Header |
|----------|----------|-----------------|
| **KubeVirt IMDS** | `http://169.254.169.254` | None |
| **AWS** | `http://169.254.169.254/latest/` | IMDSv2: `X-aws-ec2-metadata-token` |
| **Azure** | `http://169.254.169.254/metadata/` | `Metadata: true` |
| **GCP** | `http://metadata.google.internal/computeMetadata/v1/` | `Metadata-Flavor: Google` |

### Token/Credential Endpoints

| Provider | Endpoint | Response Format |
|----------|----------|-----------------|
| **KubeVirt IMDS** | `GET /v1/token` | `{"token": "<JWT>", "expirationTimestamp": "..."}` |
| **AWS** | `GET /latest/meta-data/iam/security-credentials/<role>` | `{"AccessKeyId", "SecretAccessKey", "Token", "Expiration"}` |
| **Azure** | `GET /metadata/identity/oauth2/token?resource=<URI>` | `{"access_token", "expires_in", "token_type"}` |
| **GCP** | `GET /instance/service-accounts/default/token` | `{"access_token", "expires_in", "token_type"}` |
| **GCP** | `GET /instance/service-accounts/default/identity?audience=<URI>` | JWT (identity token) |

### Identity Metadata Comparison

| Feature | KubeVirt IMDS | AWS | Azure | GCP |
|---------|---------------|-----|-------|-----|
| Instance ID | - | `instance-id` | `compute/vmId` | `instance/id` |
| Instance Name | `vmName` | - | `compute/name` | `instance/name` |
| Namespace/Project | `namespace` | - | `subscriptionId` | `project/project-id` |
| ServiceAccount | `serviceAccountName` | IAM role | Managed Identity | SA email |
| Pod Name | `podName` | - | - | - |
| Region/Zone | - | `placement/region` | `compute/location` | `instance/zone` |
| Instance Type | - | `instance-type` | `compute/vmSize` | `instance/machine-type` |
| Tags/Labels | - | `tags/instance` | `compute/tags` | `instance/attributes/` |
| Network Info | - | `network/interfaces/` | `network/interface/` | `instance/network-interfaces/` |

### API Versioning

| Provider | Scheme | Example |
|----------|--------|---------|
| **KubeVirt IMDS** | Path-based | `/v1/token` |
| **AWS** | Path-based | `/latest/meta-data/`, `/2021-01-25/` |
| **Azure** | Query parameter | `?api-version=2025-04-07` |
| **GCP** | Path-based | `/computeMetadata/v1/` |

### Security Features

| Feature | KubeVirt IMDS | AWS IMDSv2 | Azure | GCP |
|---------|---------------|------------|-------|-----|
| Required Header | No | Yes | Yes | Yes |
| Session Token | No | Yes (PUT first) | No | No |
| Hop Limit | N/A (veth) | Configurable | No | No |
| Network Isolation | Per-pod veth | Per-instance | Per-VM | Per-VM |

### Feature Coverage Matrix

| Category | KubeVirt | AWS | Azure | GCP |
|----------|----------|-----|-------|-----|
| Token/Credentials | Basic | Full IAM | Managed Identity | Service Account |
| Instance Metadata | Minimal | Comprehensive | Comprehensive | Comprehensive |
| User Data | - | Yes | Yes | Yes |
| Network Metadata | - | Yes | Yes | Yes |
| Scheduled Events | - | Yes | Yes | - |
| Attested Identity | - | PKCS7 | Signed doc | Signed JWT |
| Custom Audience | - | N/A | Yes | Yes |
| Multiple Identities | - | Multiple roles | User-assigned MI | Multiple SA |

### What KubeVirt IMDS Offers That Cloud Providers Don't

| Feature | Benefit |
|---------|---------|
| **Kubernetes-native tokens** | ServiceAccount JWTs work directly with Vault, SPIFFE, K8s API |
| **Pod-level isolation** | Each VM pod has its own IMDS instance |
| **Automatic rotation** | Kubelet manages token refresh, no guest-side logic |
| **Universal OS support** | Any OS with HTTP client works |

## Recommendations

### Priority 1: Security Hardening

**Add required header validation**

Cloud providers require a header to prevent SSRF attacks where an attacker tricks a service into making requests to the metadata endpoint.

```
Current:  curl http://169.254.169.254/v1/token
Proposed: curl -H "Metadata: true" http://169.254.169.254/v1/token
```

Implementation: Return `400 Bad Request` if `Metadata: true` header is missing.

### Priority 2: Extended Identity Metadata

**Add `/v1/metadata` endpoint**

Expose additional VM information to match cloud provider capabilities:

```json
{
  "instanceId": "vmi-abc123",
  "hostname": "my-app-vm",
  "zone": "rack-1",
  "nodeSelector": {
    "topology.kubernetes.io/zone": "zone-a"
  },
  "labels": {
    "app": "my-app",
    "env": "production"
  },
  "annotations": {
    "custom.io/key": "value"
  }
}
```

### Priority 3: Cloud-Init Compatibility

**Add user-data endpoints**

Support cloud-init bootstrap for standard cloud images:

```
GET /openstack/latest/meta_data.json
GET /openstack/latest/user_data
GET /openstack/latest/network_data.json
```

This enables standard cloud images (Ubuntu, CentOS, etc.) to initialize without NoCloud ISO.

### Priority 4: Custom Token Audiences

**Support audience parameter**

Allow requesting tokens for specific audiences:

```
GET /v1/token?audience=vault
GET /v1/token?audience=https://my-service.example.com
```

Requires TokenRequest API permissions but enables multi-service authentication.

### Priority 5: Attested Identity

**Add signed identity document**

Provide cryptographically signed identity for verification:

```
GET /v1/identity/document  -> JSON identity document
GET /v1/identity/signature -> Base64 signature
GET /v1/identity/pkcs7     -> PKCS7 signed document
```

Enables third parties to verify VM identity without trusting the token directly.

### Implementation Roadmap

| Phase | Features | Complexity |
|-------|----------|------------|
| **Phase 1** | Required header, `/v1/metadata` | Low |
| **Phase 2** | User-data endpoints (cloud-init) | Medium |
| **Phase 3** | Custom audiences | Medium |
| **Phase 4** | Attested identity | High |

## References

- [AWS IMDS Documentation](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-instance-metadata.html)
- [Azure IMDS Documentation](https://learn.microsoft.com/en-us/azure/virtual-machines/instance-metadata-service)
- [GCP Metadata Server Documentation](https://cloud.google.com/compute/docs/metadata/overview)
- [KubeVirt ServiceAccount Issue #1541](https://github.com/kubevirt/kubevirt/issues/1541)
- [KubeVirt ServiceAccount Issue #13311](https://github.com/kubevirt/kubevirt/issues/13311)
- [Kubernetes Projected ServiceAccount Tokens](https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/#serviceaccount-token-volume-projection)
