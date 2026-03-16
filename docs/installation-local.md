# Local Development Setup

Set up a local development environment using Kind for testing and development.

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/)
- [Kind](https://kind.sigs.k8s.io/) (Kubernetes in Docker)
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- [Helm](https://helm.sh/) 3.14+
- Go 1.23+ (for building from source)

## Option A: Helm Install on Kind

The fastest way to get a working local setup.

### 1. Create a Kind Cluster

```sh
kind delete cluster --name private-llm || true
kind create cluster --name private-llm
```

### 2. Install the Operator

```sh
helm upgrade --install private-llm charts/private-llm-operator \
  --namespace private-llm-system --create-namespace \
  --dependency-update \
  --set PUBLIC_HOST=localhost \
  --set traefik.service.type=NodePort \
  --set ingress.ports.web.nodePort=30080 \
  --set ingress.ports.websecure.nodePort=30443
```

### 3. Create and Test an Instance

```sh
# Create an LLMInstance
kubectl apply -f config/samples/llm_v1alpha1_llminstance.yaml

# Watch until Ready
kubectl get llminstance llminstance-sample -w

# Create a token
kubectl apply -f config/samples/llm_v1alpha1_apitokenrequest.yaml

# Wait for token
kubectl wait apitokenrequest/example-apitokenrequest \
  --for=jsonpath='{.status.phase}'=Ready --timeout=120s

# Retrieve credentials
SECRET=$(kubectl get apitokenrequest example-apitokenrequest -o jsonpath='{.status.secretName}')
OPENAI_API_KEY=$(kubectl get secret "$SECRET" -o jsonpath='{.data.OPENAI_API_KEY}' | base64 -d)
OPENAI_API_URL=$(kubectl get secret "$SECRET" -o jsonpath='{.data.OPENAI_API_URL}' | base64 -d)

# Test (replace https with http and host with localhost:30080)
curl -sSk "http://localhost:30080/llm/$(kubectl get llminstance llminstance-sample -o jsonpath='{.metadata.annotations.llm\.privatellms\.msp/slug}')/health" \
  -H "Authorization: Bearer $OPENAI_API_KEY"
```

> **Note:** On Kind with NodePort, the model download takes 1-3 minutes depending on your connection. The instance stays in `Provisioning` phase until the init container completes.

## Option B: Run Operator Locally (Development Mode)

Best for rapid iteration on controller code.

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

> **Tip:** When running locally, the operator can create Deployments and Services but the health check will fail because it tries to probe the in-cluster service DNS. For full end-to-end testing, use Option A (Helm on Kind).

## Option C: OCM Bootstrap on Kind

For testing the full OCM delivery pipeline locally. See [Local Deployment with OCM](LOCAL-DEPLOY.md) for the full guide.

Quick summary:

```sh
# 1. Create cluster
kind create cluster --name private-llm

# 2. Install OCM controller + KRO + Flux
kubectl apply -k https://github.com/open-component-model/open-component-model/kubernetes/controller/config/default?ref=main
helm install kro oci://ghcr.io/kro-run/kro/kro --namespace kro --create-namespace
kubectl apply -f https://github.com/fluxcd/flux2/releases/latest/download/install.yaml

# 3. Set up credentials and bootstrap
# (see docs/LOCAL-DEPLOY.md for full instructions)
```

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
  --set traefik.service.type=NodePort
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

# For large models, increase the pod timeout or pre-download
```

### Operator can't create Traefik Middleware

If Traefik CRDs are not installed, the operator gracefully skips middleware creation. Install Traefik CRDs or use the bundled Traefik chart.

### Health check fails in local mode

When running `make run`, the operator tries to probe `http://<service>.svc.cluster.local:8000/health` which is not reachable from outside the cluster. This is expected -- use Helm on Kind for end-to-end testing.
