# Local Development

Set up a local development environment using Kind for testing and development.

---

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/)
- [Kind](https://kind.sigs.k8s.io/) (Kubernetes in Docker)
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- [Helm](https://helm.sh/) 3.14+
- [kubectl-kcp plugin](https://github.com/kcp-dev/kcp/releases) — provides `kubectl kcp workspace` for KCP workspace management
- [Platform Mesh local-setup](https://github.com/platform-mesh/helm-charts/tree/main/local-setup) — provides KCP, the portal UI, and workspace hierarchy
- Go 1.23+ (only if building from source)

### Install kubectl-kcp

Download the plugin for your platform from [kcp releases](https://github.com/kcp-dev/kcp/releases) and place `kubectl-kcp` on your `PATH`:

```sh
# macOS (Apple Silicon)
wget -qO- https://github.com/kcp-dev/kcp/releases/latest/download/kubectl-kcp-plugin_*_darwin_arm64.tar.gz | tar xz -C /usr/local/bin bin/kubectl-kcp --strip-components=1

# Linux (amd64)
wget -qO- https://github.com/kcp-dev/kcp/releases/latest/download/kubectl-kcp-plugin_*_linux_amd64.tar.gz | tar xz -C /usr/local/bin bin/kubectl-kcp --strip-components=1

# Verify
kubectl kcp workspace --help
```

### Run the Platform Mesh local-setup

```sh
# From the helm-charts repo
task local-setup
```

After completion:
- KCP API at `https://localhost:8443`
- Admin kubeconfig at `.secret/kcp/admin.kubeconfig`
- Portal UI at `https://portal.localhost:8443`

## Key Concepts

If you're new to Platform Mesh, these resources explain the core concepts used in this guide:

- [**KCP Workspaces**](https://docs.kcp.io/kcp/main/concepts/workspaces/) — multi-tenant control plane that hosts provider and consumer workspaces
- [**APIExport & APIBinding**](https://docs.kcp.io/kcp/main/concepts/apis/) — how providers expose APIs and consumers bind to them
- [**API Sync Agent & PublishedResource**](https://docs.kcp.io/api-syncagent/) — how CRs created in KCP are synced to workload clusters
- [**Architecture overview**](./architecture.md) — how the operator, sync agent, and KCP fit together

## Quick Start

### 1. Install the operator

The operator installs on the Kind cluster created by the local-setup. Make sure your `KUBECONFIG` points to the Kind cluster (not the KCP admin kubeconfig):

```sh
unset KUBECONFIG  # ensure we target the Kind cluster, not KCP

helm upgrade --install private-llm charts/private-llm-operator \
  --namespace private-llm-system --create-namespace \
  --dependency-update \
  --set PUBLIC_HOST=localhost \
  --set traefik.enabled=false \
  --set kubeRbacProxy.enabled=false
```

### 2. Create a [provider workspace](https://docs.kcp.io/kcp/main/concepts/apis/) in KCP

Switch to the KCP admin kubeconfig for workspace management:

```sh
export KUBECONFIG=.secret/kcp/admin.kubeconfig
export KCP_URL="https://localhost:8443"

kubectl kcp workspace create providers --type=root:providers --ignore-existing
kubectl kcp workspace create private-llm --type=root:provider \
  --server="$KCP_URL/clusters/root:providers"
kubectl create ns default --server="$KCP_URL/clusters/root:providers:private-llm"
```

### 3. Install the PM integration chart in KCP

This registers the [APIExport](https://docs.kcp.io/kcp/main/concepts/apis/), [ProviderMetadata, and ContentConfiguration](./architecture.md) so the provider appears in the marketplace:

```sh
helm upgrade --install private-llm-pm charts/private-llm-pm-integration \
  --namespace default \
  --kubeconfig .secret/kcp/admin.kubeconfig \
  --kube-apiserver "$KCP_URL/clusters/root:providers:private-llm" \
  --set publicHost=localhost \
  --set publicScheme=http
```

### 4. Install the [sync agent](https://docs.kcp.io/api-syncagent/)

The sync agent bridges KCP and the Kind cluster. It watches for LLMInstance CRs created in KCP workspaces and syncs them to the cluster where the operator runs via [PublishedResources](https://docs.kcp.io/api-syncagent/).

Switch back to the Kind kubeconfig:

```sh
unset KUBECONFIG  # target the Kind cluster

# Create a kubeconfig for the sync agent with in-cluster KCP access
FRONTPROXY="https://frontproxy-front-proxy.platform-mesh-system.svc.cluster.local:6443"
sed "s|https://localhost:8443/clusters/root|${FRONTPROXY}/clusters/root:providers:private-llm|" \
  .secret/kcp/admin.kubeconfig > /tmp/sync-agent-kubeconfig.yaml

kubectl create namespace api-syncagent --dry-run=client -o yaml | kubectl apply -f -
kubectl -n api-syncagent create secret generic pm-kubeconfig \
  --from-file=kubeconfig=/tmp/sync-agent-kubeconfig.yaml \
  --dry-run=client -o yaml | kubectl apply -f -

# Install the sync agent
helm upgrade --install private-llm-sync-agent charts/private-llm-sync-agent \
  --namespace api-syncagent --create-namespace \
  --dependency-update \
  --set syncAgentOperator.enabled=true \
  --set syncAgentOperator.apiExportName=llm.privatellms.msp \
  --set syncAgentOperator.agentName=llm-agent \
  --set syncAgentOperator.kcpKubeconfig=pm-kubeconfig \
  --set publishedResources.enabled=true \
  --set publishedResources.namespace=api-syncagent

# Patch sync agent to resolve KCP virtual workspace endpoints inside the cluster
TRAEFIK_IP=$(kubectl get svc traefik -o jsonpath='{.spec.clusterIP}')
kubectl -n api-syncagent patch deployment private-llm-sync-agent \
  --type=json -p="[{\"op\":\"add\",\"path\":\"/spec/template/spec/hostAliases\",\"value\":[{\"ip\":\"$TRAEFIK_IP\",\"hostnames\":[\"root.kcp.localhost\"]}]}]"
```

Verify it's running:

```sh
kubectl -n api-syncagent get pods
kubectl -n api-syncagent logs deploy/llm-agent --tail=20
```

### 5. Create an LLMInstance via KCP

Create resources through KCP as a customer would via the portal:

```sh
export KUBECONFIG=.secret/kcp/admin.kubeconfig
export KCP_URL="https://localhost:8443"

# Create a demo workspace and bind to the LLM APIExport
kubectl kcp workspace create demo --type=root:org --server="$KCP_URL/clusters/root:orgs"
kubectl create ns default --server="$KCP_URL/clusters/root:orgs:demo"

# Bind to the LLM APIExport (see https://docs.kcp.io/kcp/main/concepts/apis/)
kubectl apply --server="$KCP_URL/clusters/root:orgs:demo" -f - <<'EOF'
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: llm-binding
spec:
  reference:
    export:
      path: root:providers:private-llm
      name: llm.privatellms.msp
EOF

# Create an LLMInstance
kubectl apply --server="$KCP_URL/clusters/root:orgs:demo" -f - <<'EOF'
apiVersion: llm.privatellms.msp/v1alpha1
kind: LLMInstance
metadata:
  name: demo-llm
spec:
  model: tinyllama
  replicas: 1
EOF

# Watch until Ready (1-3 min for model download)
kubectl get llminstances --server="$KCP_URL/clusters/root:orgs:demo" -w
```

### 6. Access the LLM

```sh
unset KUBECONFIG  # target the Kind cluster

# Find the namespace created by the sync agent
NS=$(kubectl get llminstances -A -o jsonpath='{.items[0].metadata.namespace}')

# Port-forward the llama.cpp service
kubectl port-forward -n "$NS" svc/demo-llm-llama 8000:8000

# Test
curl http://localhost:8000/v1/models
curl http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "/models/tinyllama.gguf", "messages": [{"role": "user", "content": "Hello"}]}'
```

The full flow: **KCP workspace** → sync agent → **Kind cluster** → operator reconciles → status synced back → **KCP / Portal UI**.

### 7. Connect Chat UI (optional)

To test both operators together, see the [Chat UI local installation guide](https://github.com/apeirora/showroom-msp-chat-ui/blob/main/docs/installation-local.md).

## Development Mode

For rapid iteration on controller code without the full Platform Mesh stack.

### 1. Create a Kind Cluster

```sh
kind delete cluster --name private-llm || true
kind create cluster --name private-llm
```

### 2. Install CRDs

```sh
make install
```

### 3. Run the Operator

```sh
# Runs the controller outside the cluster, connected via kubeconfig
PUBLIC_HOST=localhost make run
```

The operator starts with:
- Metrics on `:8080`
- Health probes on `:8081`
- Auth server on `:8090`

### 4. Create Test Resources

In a separate terminal:

```sh
kubectl apply -f config/samples/llm_v1alpha1_llminstance.yaml
kubectl get llminstances -w
```

> **Tip:** When running locally, the operator can create Deployments and Services but the health check will fail because it tries to probe the in-cluster service DNS. For full end-to-end testing, use the Quick Start above.

> For testing the OCM delivery pipeline locally, see [OCM Installation](installation-ocm.md).

## Building from Source

### Build the Operator Binary

```sh
make build
```

### Build the Docker Image

```sh
# For the current platform
make docker-build IMG=private-llm-controller:dev

# For linux/amd64 (e.g., for Kind on Apple Silicon)
DOCKER_DEFAULT_PLATFORM=linux/amd64 make docker-build IMG=private-llm-controller:dev

# Multi-platform
make docker-buildx PLATFORMS=linux/amd64,linux/arm64 IMG=private-llm-controller:dev
```

### Load into Kind

```sh
kind load docker-image private-llm-controller:dev --name private-llm

helm upgrade --install private-llm charts/private-llm-operator \
  --namespace private-llm-system --create-namespace \
  --dependency-update \
  --set PUBLIC_HOST=localhost \
  --set image.repository=private-llm-controller \
  --set image.tag=dev \
  --set image.pullPolicy=Never \
  --set traefik.enabled=false \
  --set kubeRbacProxy.enabled=false
```

## Running Tests

```sh
# Unit tests
make test

# Lint
make lint
```

## Useful Make Targets

| Target | Description |
|--------|-------------|
| `make build` | Build the operator binary |
| `make run` | Run the operator locally against the current kubeconfig |
| `make install` | Install CRDs into the cluster |
| `make uninstall` | Remove CRDs from the cluster |
| `make deploy IMG=<img>` | Deploy the operator via Kustomize |
| `make undeploy` | Remove the operator deployment |
| `make docker-build IMG=<img>` | Build the container image |
| `make docker-push IMG=<img>` | Push the container image |
| `make test` | Run unit tests |
| `make lint` | Run golangci-lint |
| `make help` | Show all available targets |

## Troubleshooting

### Model download is slow or fails

The init container downloads GGUF models from HuggingFace. If your network is restricted:

```sh
# Check init container logs
kubectl logs <pod-name> -c download-model
```

### Operator can't create Traefik Middleware

If Traefik CRDs are not installed, the operator gracefully skips middleware creation. This is expected when using `traefik.enabled=false`.

### Health check fails in development mode

When running `make run`, the operator tries to probe `http://<service>.svc.cluster.local:8000/health` which is not reachable from outside the cluster. This is expected -- use the Quick Start for end-to-end testing.

### KUBECONFIG confusion

The Quick Start switches between two kubeconfigs:
- **Kind cluster** (default, `unset KUBECONFIG`) — for installing operators, sync agents, port-forwarding
- **KCP admin** (`export KUBECONFIG=.secret/kcp/admin.kubeconfig`) — for creating workspaces, applying APIBindings, creating CRs in KCP

If a command fails with "unauthorized" or targets the wrong cluster, check which `KUBECONFIG` is active.

### Sync agent can't connect to KCP

Check the kubeconfig secret exists and contains a valid kubeconfig:

```sh
kubectl -n api-syncagent get secret pm-kubeconfig -o jsonpath='{.data.kubeconfig}' | base64 -d | head -5
```

The sync agent kubeconfig should use the in-cluster front-proxy address (`frontproxy-front-proxy.platform-mesh-system.svc.cluster.local:6443`), not `localhost:8443` which is not reachable from inside Kind pods.

### kubectl kcp workspace: command not found

Install the [kubectl-kcp plugin](https://github.com/kcp-dev/kcp/releases). The `kubectl kcp workspace` command is provided by the `kubectl-kcp` binary which must be on your `PATH`. See Prerequisites.
