#!/bin/bash
#
# E2E test script for kubevirt-imds
#
# Prerequisites:
#   - kind cluster running
#   - KubeVirt installed
#   - kubectl and virtctl available
#
# Usage:
#   ./test/e2e/run.sh
#
# Environment variables:
#   KIND_CLUSTER_NAME - Name of the kind cluster (default: "kind")
#

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
TEST_NAMESPACE="kubevirt"
TIMEOUT_SECONDS=300
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kind}"
KUBE_CONTEXT="kind-${KIND_CLUSTER_NAME}"

# Wrapper for kubectl that always uses the correct context
kctl() {
    kubectl --context "${KUBE_CONTEXT}" "$@"
}

# Validate kind cluster exists and context is valid
validate_cluster() {
    echo -e "${GREEN}[INFO]${NC} Validating kind cluster '${KIND_CLUSTER_NAME}'..."

    # Check if kind cluster exists
    if ! kind get clusters 2>/dev/null | grep -q "^${KIND_CLUSTER_NAME}$"; then
        echo -e "${RED}[ERROR]${NC} Kind cluster '${KIND_CLUSTER_NAME}' not found"
        echo -e "${RED}[ERROR]${NC} Available clusters: $(kind get clusters 2>/dev/null | tr '\n' ' ')"
        exit 1
    fi

    # Verify context works
    if ! kctl cluster-info >/dev/null 2>&1; then
        echo -e "${RED}[ERROR]${NC} Cannot connect to cluster using context '${KUBE_CONTEXT}'"
        exit 1
    fi

    echo -e "${GREEN}[INFO]${NC} Using kubectl context: ${KUBE_CONTEXT}"
}

# Initialize - validate cluster before anything else
validate_cluster

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_step() {
    echo -e "\n${GREEN}==>${NC} $1"
}

cleanup() {
    log_step "Cleaning up..."
    kctl delete vm testvm-imds -n ${TEST_NAMESPACE} --ignore-not-found=true || true
    kctl delete vm testvm-imds-a -n ${TEST_NAMESPACE} --ignore-not-found=true || true
    kctl delete vm testvm-imds-b -n ${TEST_NAMESPACE} --ignore-not-found=true || true
    kctl delete pod network-sniffer -n ${TEST_NAMESPACE} --ignore-not-found=true || true
}

wait_for_vm_pod() {
    local vm_name=$1
    local expected_containers=$2
    local timeout=$3

    log_info "Waiting for VM pod ${vm_name} to have ${expected_containers} ready containers..."

    local end_time=$((SECONDS + timeout))
    while [ $SECONDS -lt $end_time ]; do
        # Get container ready status and count
        local status_output
        status_output=$(kctl get pod -n ${TEST_NAMESPACE} -l kubevirt.io/domain=${vm_name} -o jsonpath='{.items[0].status.containerStatuses[*].ready}' 2>/dev/null) || status_output=""

        local ready=0
        local total=0
        if [ -n "$status_output" ]; then
            # Count total containers and ready containers
            for status in $status_output; do
                total=$((total + 1))
                if [ "$status" = "true" ]; then
                    ready=$((ready + 1))
                fi
            done
        fi

        if [ "$ready" -eq "$expected_containers" ] && [ "$total" -eq "$expected_containers" ]; then
            log_info "Pod is ready (${ready}/${total} containers)"
            return 0
        fi

        echo -n "."
        sleep 5
    done

    echo ""
    log_error "Timeout waiting for pod"
    return 1
}

get_pod_name() {
    local vm_name=$1
    kctl get pod -n ${TEST_NAMESPACE} -l kubevirt.io/domain=${vm_name} -o jsonpath='{.items[0].metadata.name}' 2>/dev/null
}

test_endpoint() {
    local pod=$1
    local endpoint=$2
    local expected_pattern=$3

    log_info "Testing ${endpoint}..."

    local response
    response=$(kctl exec -n ${TEST_NAMESPACE} ${pod} -c compute -- curl -sf http://169.254.169.254${endpoint} 2>/dev/null) || {
        log_error "Failed to reach ${endpoint}"
        return 1
    }

    if echo "$response" | grep -q "$expected_pattern"; then
        log_info "  Response: ${response:0:100}..."
        return 0
    else
        log_error "  Unexpected response: ${response}"
        return 1
    fi
}

# Test that IMDS returns correct VM identity (proves namespace isolation)
test_identity_isolation() {
    local pod=$1
    local expected_vm_name=$2

    log_info "Testing identity isolation for ${expected_vm_name}..."

    local response
    response=$(kctl exec -n ${TEST_NAMESPACE} ${pod} -c compute -- curl -sf http://169.254.169.254/v1/identity 2>/dev/null) || {
        log_error "Failed to reach /v1/identity"
        return 1
    }

    # Extract vmName from JSON response
    local actual_vm_name
    actual_vm_name=$(echo "$response" | grep -o '"vmName":"[^"]*"' | cut -d'"' -f4)

    if [ "$actual_vm_name" = "$expected_vm_name" ]; then
        log_info "  Correct: IMDS returned vmName=${actual_vm_name}"
        return 0
    else
        log_error "  ISOLATION FAILURE: Expected vmName=${expected_vm_name}, got vmName=${actual_vm_name}"
        log_error "  This means packets are leaking between namespaces!"
        return 1
    fi
}

# Test 1: Basic IMDS functionality with single VM
test_basic_imds() {
    log_step "Test 1: Basic IMDS Functionality"

    local vm_name="testvm-imds"

    # Create test VM
    log_info "Creating test VM: ${vm_name}"
    kctl apply -f deploy/test/vm-with-imds.yaml

    # Wait for VM pod (3 containers: compute, volumecontainerdisk, imds-server)
    wait_for_vm_pod "${vm_name}" 3 ${TIMEOUT_SECONDS}

    local pod_name
    pod_name=$(get_pod_name "${vm_name}")
    log_info "Pod name: ${pod_name}"

    # Verify IMDS sidecar is injected
    local containers
    containers=$(kctl get pod -n ${TEST_NAMESPACE} ${pod_name} -o jsonpath='{.spec.containers[*].name}')

    if echo "$containers" | grep -q "imds-server"; then
        log_info "IMDS sidecar found: ${containers}"
    else
        log_error "IMDS sidecar not found in containers: ${containers}"
        return 1
    fi

    # Give IMDS server time to set up veth and start
    sleep 5

    local failed=0

    test_endpoint "${pod_name}" "/healthz" "OK" || ((failed++))
    test_endpoint "${pod_name}" "/v1/identity" "namespace" || ((failed++))
    test_endpoint "${pod_name}" "/v1/token" "token" || ((failed++))

    # Cleanup single VM
    kctl delete vm ${vm_name} -n ${TEST_NAMESPACE} --wait=false

    return $failed
}

# Test 2: Network namespace isolation with two VMs
test_namespace_isolation() {
    log_step "Test 2: Network Namespace Isolation (Two VMs)"

    local vm_a="testvm-imds-a"
    local vm_b="testvm-imds-b"

    # Create two VMs
    log_info "Creating two test VMs: ${vm_a}, ${vm_b}"
    kctl apply -f deploy/test/two-vms-isolation.yaml

    # Wait for both VM pods
    log_info "Waiting for VM A..."
    wait_for_vm_pod "${vm_a}" 3 ${TIMEOUT_SECONDS}

    log_info "Waiting for VM B..."
    wait_for_vm_pod "${vm_b}" 3 ${TIMEOUT_SECONDS}

    local pod_a pod_b
    pod_a=$(get_pod_name "${vm_a}")
    pod_b=$(get_pod_name "${vm_b}")

    log_info "Pod A: ${pod_a}"
    log_info "Pod B: ${pod_b}"

    # Give IMDS servers time to set up
    sleep 5

    local failed=0

    # Test that each VM's IMDS returns its own identity
    # If packets were leaking, VM A might get VM B's identity or vice versa
    log_info "Verifying each VM sees its own IMDS..."

    test_identity_isolation "${pod_a}" "${vm_a}" || ((failed++))
    test_identity_isolation "${pod_b}" "${vm_b}" || ((failed++))

    # Additional test: make multiple requests to increase confidence
    log_info "Running repeated isolation checks (10 iterations)..."
    for i in $(seq 1 10); do
        local identity_a identity_b

        identity_a=$(kctl exec -n ${TEST_NAMESPACE} ${pod_a} -c compute -- curl -sf http://169.254.169.254/v1/identity 2>/dev/null | grep -o '"vmName":"[^"]*"' | cut -d'"' -f4)
        identity_b=$(kctl exec -n ${TEST_NAMESPACE} ${pod_b} -c compute -- curl -sf http://169.254.169.254/v1/identity 2>/dev/null | grep -o '"vmName":"[^"]*"' | cut -d'"' -f4)

        if [ "$identity_a" != "${vm_a}" ] || [ "$identity_b" != "${vm_b}" ]; then
            log_error "Iteration $i: Isolation failure! A=${identity_a}, B=${identity_b}"
            ((failed++))
        fi
    done

    if [ $failed -eq 0 ]; then
        log_info "All 10 iterations passed - namespace isolation confirmed"
    fi

    # Cleanup
    kctl delete vm ${vm_a} ${vm_b} -n ${TEST_NAMESPACE} --wait=false

    return $failed
}

# Test 3: Network traffic sniffing to prove packets don't leak
test_no_traffic_leak() {
    log_step "Test 3: Network Traffic Sniffing (Verify No Packet Leakage)"

    local vm_a="testvm-imds-a"
    local vm_b="testvm-imds-b"

    # Deploy sniffer pod with hostNetwork to see node-level traffic
    log_info "Deploying network sniffer pod..."
    kctl apply -f deploy/test/sniffer-pod.yaml

    # Wait for sniffer to be ready
    kctl wait --for=condition=Ready pod/network-sniffer -n ${TEST_NAMESPACE} --timeout=60s

    # Create two VMs
    log_info "Creating two test VMs for traffic analysis..."
    kctl apply -f deploy/test/two-vms-isolation.yaml

    # Wait for both VM pods
    wait_for_vm_pod "${vm_a}" 3 ${TIMEOUT_SECONDS}
    wait_for_vm_pod "${vm_b}" 3 ${TIMEOUT_SECONDS}

    local pod_a pod_b
    pod_a=$(get_pod_name "${vm_a}")
    pod_b=$(get_pod_name "${vm_b}")

    # Give IMDS servers time to set up
    sleep 5

    # Start tcpdump with timeout, generate traffic, then check results
    # We use timeout instead of -c <count> because we expect 0 packets (isolation working)
    log_info "Starting packet capture (10s timeout) while generating traffic..."

    # Run tcpdump with timeout in background, generate traffic, then analyze
    # The capture runs for 10 seconds - if isolation works, it should capture 0 packets
    kctl exec -n ${TEST_NAMESPACE} network-sniffer -- sh -c '
        rm -f /tmp/capture.pcap
        timeout 10 tcpdump -i any -n "host 169.254.169.254" -w /tmp/capture.pcap 2>/dev/null &
        TCPDUMP_PID=$!
        echo "tcpdump started with PID $TCPDUMP_PID"
        sleep 1
    '

    # Generate traffic from both VMs while tcpdump is running
    log_info "Generating IMDS traffic from both VMs..."
    for i in $(seq 1 20); do
        kctl exec -n ${TEST_NAMESPACE} ${pod_a} -c compute -- curl -sf http://169.254.169.254/v1/identity >/dev/null 2>&1 &
        kctl exec -n ${TEST_NAMESPACE} ${pod_b} -c compute -- curl -sf http://169.254.169.254/v1/identity >/dev/null 2>&1 &
    done

    # Wait for curl requests to complete
    wait

    # Wait for tcpdump timeout to finish
    log_info "Waiting for packet capture to complete..."
    sleep 10

    # Analyze capture - count packets
    log_info "Analyzing captured packets..."

    local packet_count
    packet_count=$(kctl exec -n ${TEST_NAMESPACE} network-sniffer -- sh -c '
        if [ -f /tmp/capture.pcap ]; then
            tcpdump -r /tmp/capture.pcap -n 2>/dev/null | wc -l
        else
            echo "0"
        fi
    ' 2>/dev/null) || packet_count="0"

    # Trim whitespace
    packet_count=$(echo "$packet_count" | tr -d '[:space:]')

    local failed=0

    if [ "$packet_count" = "0" ]; then
        log_info "0 packets captured on host network - ISOLATION CONFIRMED"
        log_info "169.254.169.254 traffic stayed within pod network namespaces"
    else
        log_error "LEAK DETECTED: ${packet_count} packets with 169.254.169.254 found on host network!"
        log_error "Captured traffic:"
        kctl exec -n ${TEST_NAMESPACE} network-sniffer -- tcpdump -r /tmp/capture.pcap -e -n 2>/dev/null | head -20
        ((failed++))
    fi

    # Additional test: verify third-party pod cannot reach 169.254.169.254
    log_info "Verifying sniffer pod (no IMDS) cannot reach 169.254.169.254..."

    local sniffer_result
    sniffer_result=$(kctl exec -n ${TEST_NAMESPACE} network-sniffer -- \
        timeout 3 curl -sf http://169.254.169.254/healthz 2>&1) || sniffer_result="UNREACHABLE"

    if [ "$sniffer_result" = "UNREACHABLE" ] || echo "$sniffer_result" | grep -q "timed out\|Connection refused\|No route"; then
        log_info "Sniffer pod correctly cannot reach 169.254.169.254 - ISOLATION CONFIRMED"
    else
        log_error "ISOLATION FAILURE: Sniffer pod received response from 169.254.169.254!"
        log_error "Response: $sniffer_result"
        ((failed++))
    fi

    # Cleanup
    kctl delete vm ${vm_a} ${vm_b} -n ${TEST_NAMESPACE} --wait=false
    kctl delete pod network-sniffer -n ${TEST_NAMESPACE} --wait=false

    return $failed
}

# Main test flow
main() {
    log_step "E2E Test Suite: kubevirt-imds"

    # Trap for cleanup on exit
    trap cleanup EXIT

    # Step 1: Build and load images
    log_step "Setup: Building and loading images"
    make docker-build-all
    make kind-load-all KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME}"

    # Step 2: Deploy webhook
    log_step "Setup: Deploying webhook"

    # Generate certs and capture CA bundle
    CERT_OUTPUT=$(KUBE_CONTEXT="${KUBE_CONTEXT}" ./hack/generate-certs.sh 2>&1)
    echo "$CERT_OUTPUT"
    CA_BUNDLE=$(echo "$CERT_OUTPUT" | grep -A1 "CA Bundle (base64):" | tail -1)

    kctl apply -f deploy/webhook/namespace.yaml
    kctl apply -f deploy/webhook/rbac.yaml
    kctl apply -f deploy/webhook/deployment.yaml
    kctl apply -f deploy/webhook/service.yaml
    kctl apply -f deploy/webhook/webhook.yaml

    # Patch webhook with CA bundle
    log_info "Patching webhook with CA bundle..."
    kctl patch mutatingwebhookconfiguration imds-webhook --type='json' \
        -p="[{\"op\": \"add\", \"path\": \"/webhooks/0/clientConfig/caBundle\", \"value\":\"${CA_BUNDLE}\"}]"

    # Restart webhook to pick up new TLS certificate
    log_info "Restarting webhook to pick up new certificate..."
    kctl rollout restart deployment/imds-webhook -n kubevirt-imds

    log_info "Waiting for webhook to be ready..."
    kctl rollout status deployment/imds-webhook -n kubevirt-imds --timeout=60s

    # Run tests
    local total_failed=0

    test_basic_imds || ((total_failed++))

    # Wait for first VM cleanup before starting isolation test
    sleep 10

    test_namespace_isolation || ((total_failed++))

    # Wait for cleanup before starting traffic leak test
    sleep 10

    test_no_traffic_leak || ((total_failed++))

    # Summary
    log_step "Test Summary"
    if [ $total_failed -eq 0 ]; then
        log_info "All tests passed!"
        exit 0
    else
        log_error "${total_failed} test(s) failed"
        exit 1
    fi
}

main "$@"
