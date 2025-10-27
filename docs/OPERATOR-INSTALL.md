## Operator installation

This guide describes how to install Private LLM operator via OCM.

### Prereqs
- **OCM Controller, KRO and Flux are installed in the target cluster**
- Public DNS resolving to your Traefik LoadBalancer IP
- kubectl, Helm 3.14+, ocm CLI
- `GITHUB_TOKEN` for GHCR

### 1) Credentials setup
```bash
kubectl -n ocm-system create secret docker-registry ghcr-credentials \
  --docker-server=ghcr.io --docker-username="apeirora" --docker-password="$GITHUB_TOKEN" --dry-run=client -o yaml | kubectl apply -f -

kubectl -n ocm-system create serviceaccount ocm-repo-access --dry-run=client -o yaml | kubectl apply -f -
kubectl -n ocm-system patch serviceaccount ocm-repo-access -p '{"imagePullSecrets":[{"name":"ghcr-credentials"}]}'

# Reposity for OCM to access resources
kubectl apply -f - <<'YAML'
apiVersion: delivery.ocm.software/v1alpha1
kind: Repository
metadata:
  name: apeirora-repository
  namespace: ocm-system
spec:
  repositorySpec:
    baseUrl: ghcr.io/apeirora/ocm
    type: OCIRegistry
  interval: 1m
  ocmConfig:
    - kind: Secret
      name: ghcr-credentials
YAML
```

### 2) Prepare your values.yaml file
```yaml
component:
  semver: "<YOUR-VERSION>"

operator:
  targetNamespace: private-llm-operator
  publicHost: <YOUR_HOSTNAME>
  publicScheme: https
  imagePullSecretName: ghcr-credentials
  traefikEnabled: true # or false if installed externally
  tlsSecretName: "private-llm"
  ingressExtraAnnotations: {}
```

### 3) Prepare workload namespace and image pull secret
```bash
# Workload namespace to pull operator image
kubectl create ns private-llm-operator || true
kubectl -n private-llm-operator create secret docker-registry ghcr-credentials \
  --docker-server=ghcr.io --docker-username="apeirora" --docker-password="$GITHUB_TOKEN" --dry-run=client -o yaml | kubectl apply -f -
```

### 4) Render and apply manifests with Helm
```bash
helm template private-llm charts/private-llm-operator-ocm/ \
  -f charts/private-llm-operator-ocm/values.yaml \
  -f ./values.yaml \
  | kubectl apply -f -
```

### 5) Create an LLMInstance and call the API
```bash
kubectl apply -f - <<'YAML'
apiVersion: llm.privatellms.msp/v1alpha1
kind: LLMInstance
metadata:
  name: llminstance-sample
  namespace: default
spec:
  model: tinyllama
YAML

ENDPOINT=$(kubectl -n default get llminstances.llm.privatellms.msp llminstance-sample -o jsonpath='{.status.endpoint}')
kubectl -n default apply -f - <<'YAML'
apiVersion: llm.privatellms.msp/v1alpha1
kind: APITokenRequest
metadata:
  name: demo-token
spec:
  instanceName: llminstance-sample
YAML
# fix: we do not change phase in CR
kubectl -n default wait apitokenrequest/demo-token --for=jsonpath='{.status.phase}'=Ready --timeout=60s
SECRET=$(kubectl -n default get apitokenrequest demo-token -o jsonpath='{.status.secretName}')
BEARER_TOKEN=$(kubectl -n default get secret "$SECRET" -o jsonpath='{.data.OPENAI_API_KEY}' | base64 -D)
curl -sSik "${ENDPOINT}/health" -H "Authorization: Bearer $BEARER_TOKEN"
```