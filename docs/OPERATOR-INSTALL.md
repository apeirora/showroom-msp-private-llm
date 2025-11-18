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

### 4) Render and apply manifests
```bash
helm template private-llm charts/private-llm-operator-ocm/ \
  -f charts/private-llm-operator-ocm/values.yaml \
  -f ./values.yaml \
  | kubectl apply -f -
```

Changing `component.semver` in `values.yaml` updates both the OCM `Component`
and the resource graph so KRO sees a new generation and reconciles
automatically. Runtime chart/image tags still come from the OCM component
(`resourceChart.status.additional.tag`, `resourceImage.status.additional.tag`),
so everything deployed remains exactly what was published via OCM.

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
OPENAI_API_KEY=$(kubectl -n default get secret "$SECRET" -o jsonpath='{.data.OPENAI_API_KEY}' | base64 -D)
OPENAI_API_URL=$(kubectl -n default get secret "$SECRET" -o jsonpath='{.data.OPENAI_API_URL}' | base64 -D)
curl -sSik "${OPENAI_API_URL}/health" -H "Authorization: Bearer $OPENAI_API_KEY"
```

### 6) (Optional) Deploy without OCM
If you want to deploy the operator before the OCM controller supports your published component (or if you simply prefer to manage the release yourself), you can install the same chart directly with Helm. This bypasses OCM/KRO entirely, so you are responsible for keeping image and chart tags in sync.

```bash
# 1. Prepare namespace and pull secret
kubectl create namespace private-llm-operator --dry-run=client -o yaml | kubectl apply -f -
kubectl -n private-llm-operator create secret docker-registry ghcr-credentials \
  --docker-server=ghcr.io --docker-username="<GH_USERNAME>" --docker-password="$GITHUB_TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f -

# 2. Render/apply the chart with your desired version overrides.
helm upgrade --install private-llm-operator charts/private-llm-operator \
  --namespace private-llm-operator --create-namespace \
  --set PUBLIC_HOST=<YOUR_HOSTNAME> \
  --set PUBLIC_SCHEME=https \
  --set tls.secretName=private-llm \
  --set image.repository=ghcr.io/<GH_OWNER>/private-llm-controller \
  --set image.tag=<CONTROLLER_TAG> \
  --set 'imagePullSecrets[0].name=ghcr-credentials' \
  --set traefik.enabled=false \
  --set portalIntegration.enabled=<true|false>

# 3. Verify the controller is running with the expected image tag.
kubectl -n private-llm-operator get pods
```

Helm release notes:
- To roll forward to a new image, run `helm upgrade` with the updated `image.tag`.
- Apply the `charts/private-llm-pm-integration` chart separately inside the KCP control plane (or via Flux) whenever you need to update the `APIExport`, `ProviderMetadata`, or `ContentConfiguration`.