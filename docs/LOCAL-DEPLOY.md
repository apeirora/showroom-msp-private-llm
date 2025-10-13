## Local Deployment with OCM (kind)

This guide mirrors the OCM workflow described in `README-ocm-deploy.md`, adapted for kind.

### Prereqs
- Docker, kind, kubectl, Helm 3.14+
- ocm CLI
- `GH_OWNER` and `GITHUB_TOKEN` for GHCR (to read the OCM component; or publish manually as in README-ocm-deploy.md)

### 0) Create a kind cluster
```bash
kind delete cluster --name private-llm || true
kind create cluster --name private-llm
```

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

### 3) Traefik via bundled Helm dependency
The operator Helm chart already includes a Traefik dependency. For kind, set NodePort ports via the release spec:
  - `spec.traefikEnabled: true`
  - `spec.traefikServiceType: NodePort`
  - `spec.traefikNodePortWeb: 30080`
  - `spec.traefikNodePortWebsecure: 30443`

### 4) Bootstrap the OCM RGD and deploy the operator
```bash
envsubst < ocm/bootstrap.yaml | kubectl apply -f -
kubectl wait --for=condition=established --timeout=60s crd/resourcegraphdefinitions.kro.run
kubectl wait --for=condition=established --timeout=60s crd/privatellmoperatorreleases.kro.run
```
Then apply the preconfigured local instance file:

```bash
envsubst < ocm/instance.local.yaml | kubectl apply -f -
```

### 5) Create an instance and test via OCM-installed operator
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
  namespace: default
spec:
  instanceName: llminstance-sample
YAML
kubectl -n default wait tokenrequest/demo-token --for=jsonpath='{.status.phase}'=Ready --timeout=60s
SECRET=$(kubectl -n default get tokenrequest demo-token -o jsonpath='{.status.secretName}')
BEARER_TOKEN=$(kubectl -n default get secret "$SECRET" -o jsonpath='{.data.OPENAI_API_KEY}' | base64 -D)

# With NodePort on kind and HTTP scheme, append :30080 to the published host
HTTP_ENDPOINT="http://${ENDPOINT#http://}:30080"
curl -sSik "$HTTP_ENDPOINT/health" -H "Authorization: Bearer $BEARER_TOKEN"
```

### 6) Cleanup
```bash
kubectl delete -f ocm/instance.local.yaml || true
kind delete cluster --name private-llm || true
```


