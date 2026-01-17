#!/bin/bash
# Generate self-signed TLS certificates for the webhook
# Usage: ./hack/generate-certs.sh

set -euo pipefail

NAMESPACE="${NAMESPACE:-kubevirt-imds}"
SERVICE="${SERVICE:-imds-webhook}"
SECRET="${SECRET:-imds-webhook-tls}"
TMPDIR="${TMPDIR:-/tmp/imds-webhook-certs}"

# Create temp directory
mkdir -p "${TMPDIR}"
cd "${TMPDIR}"

# Generate CA
openssl genrsa -out ca.key 2048
openssl req -new -x509 -days 365 -key ca.key -out ca.crt -subj "/CN=IMDS Webhook CA"

# Generate server key and CSR
openssl genrsa -out server.key 2048
cat > csr.conf <<EOF
[req]
req_extensions = v3_req
distinguished_name = req_distinguished_name
prompt = no

[req_distinguished_name]
CN = ${SERVICE}.${NAMESPACE}.svc

[v3_req]
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = ${SERVICE}
DNS.2 = ${SERVICE}.${NAMESPACE}
DNS.3 = ${SERVICE}.${NAMESPACE}.svc
DNS.4 = ${SERVICE}.${NAMESPACE}.svc.cluster.local
EOF

openssl req -new -key server.key -out server.csr -config csr.conf

# Sign server cert with CA
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
    -out server.crt -days 365 -extensions v3_req -extfile csr.conf

# Create namespace if it doesn't exist
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

# Create or update secret
kubectl create secret tls "${SECRET}" \
    --cert=server.crt \
    --key=server.key \
    --namespace="${NAMESPACE}" \
    --dry-run=client -o yaml | kubectl apply -f -

# Get CA bundle for webhook configuration
CA_BUNDLE=$(base64 < ca.crt | tr -d '\n')

echo ""
echo "Certificates generated successfully!"
echo ""
echo "CA Bundle (base64):"
echo "${CA_BUNDLE}"
echo ""
echo "To update the webhook configuration, run:"
echo "kubectl patch mutatingwebhookconfiguration imds-webhook --type='json' -p='[{\"op\": \"add\", \"path\": \"/webhooks/0/clientConfig/caBundle\", \"value\":\"${CA_BUNDLE}\"}]'"
echo ""

# Clean up
rm -rf "${TMPDIR}"
