## Cluster Deployment with OCM (external Traefik)

This guide uses the same OCM flow as `README-ocm-deploy.md` and assumes Traefik is already installed in the cluster.

### Prereqs
- Public DNS resolving to your Traefik LoadBalancer IP
- kubectl, Helm 3.14+, ocm CLI
- `GH_OWNER` and `GITHUB_TOKEN` for GHCR

### 1) Install controllers (OCM + kro + Flux)
```bash
# OCM registry TLS secret
TMP=$(mktemp -d); cd "$TMP"
cat > cert.conf <<'EOF'
[req]
distinguished_name=req
x509_extensions = v3_req
prompt = no
[dn]
CN = registry.ocm-system.svc.cluster.local
[v3_req]
subjectAltName = @alt_names
basicConstraints=CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
[alt_names]
DNS.1 = registry.ocm-system.svc.cluster.local
DNS.2 = registry
EOF
openssl genrsa -out ca.key 2048 >/dev/null 2>&1
openssl req -x509 -new -nodes -key ca.key -sha256 -days 3650 -subj "/CN=OCM Registry CA" -out ca.crt >/dev/null 2>&1
openssl genrsa -out tls.key 2048 >/dev/null 2>&1
openssl req -new -key tls.key -subj "/CN=registry.ocm-system.svc.cluster.local" -out server.csr >/dev/null 2>&1
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out tls.crt -days 3650 -sha256 -extensions v3_req -extfile cert.conf >/dev/null 2>&1
kubectl create ns ocm-system || true
kubectl -n ocm-system create secret generic ocm-registry-tls-certs \
  --from-file=tls.crt=tls.crt --from-file=tls.key=tls.key --from-file=ca.crt=ca.crt
cd -

# OCM controller
kubectl apply -k https://github.com/open-component-model/open-component-model/kubernetes/controller/config/default?ref=main

# KRO
export KRO_VERSION=$(curl -sL \
    https://api.github.com/repos/kubernetes-sigs/kro/releases/latest | \
    jq -r '.tag_name | ltrimstr("v")'
  )

helm install kro oci://ghcr.io/kro-run/kro/kro \
  --namespace kro \
  --create-namespace \
  --version=${KRO_VERSION}

# Flux controllers
kubectl apply -f https://github.com/fluxcd/flux2/releases/latest/download/install.yaml
```

### 2) Create credentials
```bash
# OCM to read GHCR component
kubectl -n ocm-system create secret docker-registry ghcr-credentials \
  --docker-server=ghcr.io --docker-username="$GH_OWNER" --docker-password="$GITHUB_TOKEN" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n ocm-system create serviceaccount ocm-repo-access --dry-run=client -o yaml | kubectl apply -f -
kubectl -n ocm-system patch serviceaccount ocm-repo-access -p '{"imagePullSecrets":[{"name":"ghcr-credentials"}]}'

# Workload namespace to pull operator image
kubectl create ns private-llm-system || true
kubectl -n private-llm-system create secret docker-registry ghcr-credentials \
  --docker-server=ghcr.io --docker-username="$GH_OWNER" --docker-password="$GITHUB_TOKEN" --dry-run=client -o yaml | kubectl apply -f -
```

### 3) Apply OCM bootstrap and release
```bash
envsubst < ocm/bootstrap.yaml | kubectl apply -f -
kubectl wait --for=condition=established --timeout=60s crd/resourcegraphdefinitions.kro.run
kubectl wait --for=condition=established --timeout=60s crd/privatellmoperatorreleases.kro.run
```

### 4) Apply the preconfigured cluster instance file
```bash
envsubst < ocm/instance.cluster.yaml | kubectl apply -f -
```

### 5) Create an LLMInstance and call the API
```bash
kubectl apply -f - <<'YAML'
apiVersion: llm.example.com/v1alpha1
kind: LLMInstance
metadata:
  name: llminstance-sample
  namespace: default
spec:
  model: tinyllama
YAML

ENDPOINT=$(kubectl -n default get llminstance llminstance-sample -o jsonpath='{.status.endpoint}')
kubectl -n default apply -f - <<'YAML'
apiVersion: llm.example.com/v1alpha1
kind: TokenRequest
metadata:
  name: demo-token
spec:
  instanceName: llminstance-sample
YAML
kubectl -n default wait tokenrequest/demo-token --for=jsonpath='{.status.phase}'=Ready --timeout=60s
SECRET=$(kubectl -n default get tokenrequest demo-token -o jsonpath='{.status.secretName}')
BEARER_TOKEN=$(kubectl -n default get secret "$SECRET" -o jsonpath='{.data.OPENAI_API_KEY}' | base64 -D)
curl -sSik "https://${ENDPOINT#http://}/health" -H "Authorization: Bearer $BEARER_TOKEN"
```

### Notes
- Operator enforces Traefik entrypoints `websecure,web` and `router.tls=true` via annotations.
- If `tlsSecretName` is empty, spec.tls remains unmanaged to allow Gardener/cert-manager ownership.
- Use `ingressExtraAnnotations` to persist required Ingress annotations (e.g., Gardener DNS/cert).


