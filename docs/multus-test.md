# Testing IMDS with Multus (No DHCP Network)

This guide documents how to test kubevirt-imds with a Multus secondary network that has no DHCP server, simulating an enterprise physical network with static IP requirements.

## Prerequisites

- Kind cluster with KubeVirt installed
- kubectl configured with context `kind-kind`

## Setup

### 1. Install Multus CNI

```bash
kubectl --context kind-kind apply -f https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/master/deployments/multus-daemonset.yml
```

Wait for Multus to be ready:

```bash
kubectl --context kind-kind get pods -n kube-system -l app=multus
```

### 2. Install CNI Plugins on Kind Node

Kind doesn't include the bridge CNI plugin by default. Install it:

```bash
docker exec kind-control-plane sh -c '
  CNI_VERSION=v1.3.0
  curl -sL https://github.com/containernetworking/plugins/releases/download/${CNI_VERSION}/cni-plugins-linux-amd64-${CNI_VERSION}.tgz | tar -xz -C /opt/cni/bin
'
```

Verify:

```bash
docker exec kind-control-plane ls /opt/cni/bin/bridge
```

### 3. Create NetworkAttachmentDefinition

Create a network with no IPAM (no DHCP, no static IP assignment):

```bash
kubectl --context kind-kind apply -f deploy/test/net-attach-def-no-dhcp.yaml
```

The NetworkAttachmentDefinition creates a Linux bridge without any IP address management:

```yaml
apiVersion: "k8s.cni.cncf.io/v1"
kind: NetworkAttachmentDefinition
metadata:
  name: no-dhcp-net
  namespace: kubevirt
spec:
  config: |
    {
      "cniVersion": "0.3.1",
      "name": "no-dhcp-net",
      "type": "bridge",
      "bridge": "br-nodhcp",
      "isGateway": false,
      "ipMasq": false,
      "hairpinMode": true,
      "ipam": {}
    }
```

### 4. Create Test VM

Create a VM that uses the Multus network:

```bash
kubectl --context kind-kind apply -f deploy/test/vm-multus-no-dhcp.yaml
```

Wait for the VM to be ready:

```bash
kubectl --context kind-kind wait --for=condition=Ready vmi/testvm-multus-nodhcp -n kubevirt --timeout=120s
```

## Testing

### 1. Verify IMDS Sidecar is Running

```bash
POD=$(kubectl --context kind-kind get pods -n kubevirt -l kubevirt.io/domain=testvm-multus-nodhcp -o jsonpath='{.items[0].metadata.name}')
kubectl --context kind-kind logs -n kubevirt "$POD" -c imds-server --tail=10
```

Expected output:

```
Starting IMDS sidecar (waiting for VM bridge...)
Found bridge: k6t-<hash>
Successfully ensured veth pair attached to bridge k6t-<hash>
Starting IMDS server on 169.254.169.254:80
ARP responder listening on bridge k6t-<hash> for 169.254.169.254
```

### 2. Connect to VM Console

```bash
virtctl console testvm-multus-nodhcp -n kubevirt
```

Login with `cirros` / `gocubsgo`.

### 3. Verify No IP Address

The VM should have no IPv4 address (only IPv6 link-local from auto-configuration):

```bash
ip addr show eth0
```

Expected output:

```
2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc pfifo_fast qlen 1000
    link/ether 62:27:19:f3:0a:ef brd ff:ff:ff:ff:ff:ff
    inet6 fe80::6027:19ff:fef3:aef/64 scope link
       valid_lft forever preferred_lft forever
```

### 4. Configure Link-Local Address and Test IMDS

```bash
# Configure a link-local address
sudo ip addr add 169.254.100.100/16 dev eth0
sudo ip link set eth0 up

# Test IMDS access
curl -v -H "Metadata: true" --connect-timeout 5 http://169.254.169.254/v1/identity
```

Expected output:

```
< HTTP/1.1 200 OK
< Content-Type: application/json
{"namespace":"kubevirt","serviceAccountName":"default","vmName":"testvm-multus-nodhcp"}
```

### 5. Verify ARP Responder Logs

```bash
POD=$(kubectl --context kind-kind get pods -n kubevirt -l kubevirt.io/domain=testvm-multus-nodhcp -o jsonpath='{.items[0].metadata.name}')
kubectl --context kind-kind logs -n kubevirt "$POD" -c imds-server --tail=5
```

Expected output shows ARP request/reply:

```
ARP request for 169.254.169.254 from 169.254.100.100 (62:27:19:f3:0a:ef)
ARP reply sent: 169.254.169.254 is at c2:cd:45:cb:7a:6e
GET /v1/identity 58.543µs
```

## How It Works

1. VM boots on a network with no DHCP - it has no IPv4 address
2. User (or cloud-init) configures a link-local address (169.254.x.x/16)
3. VM sends ARP request for 169.254.169.254
4. IMDS sidecar's ARP responder (listening on the bridge) replies with veth-imds MAC
5. VM sends HTTP request to that MAC address
6. Traffic reaches veth-imds and is handled by the IMDS server

This enables IMDS access without requiring:
- DHCP on the network
- A gateway/router
- Any pre-configured routes

## Cleanup

```bash
kubectl --context kind-kind delete vm testvm-multus-nodhcp -n kubevirt
kubectl --context kind-kind delete net-attach-def no-dhcp-net -n kubevirt
```
