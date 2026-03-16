<p align="center">
  <img src="assets/icon.png" alt="Private LLM Operator" width="96" />
</p>

<h1 align="center">Private LLM Operator</h1>

<p align="center">
  <strong>Deploy private, self-hosted LLM inference endpoints on Kubernetes -- one CR, one cluster, fully yours.</strong>
</p>

<p align="center">
  <a href="https://github.com/apeirora/showroom-msp-private-llm/releases"><img src="https://img.shields.io/github/v/release/apeirora/showroom-msp-private-llm?style=flat-square&color=blue" alt="Release"></a>
  <a href="https://pkg.go.dev/github.com/apeirora/showroom-msp-private-llm"><img src="https://img.shields.io/badge/Go-1.23-00ADD8?style=flat-square&logo=go" alt="Go 1.23"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache%202.0-green?style=flat-square" alt="License"></a>
  <a href="https://github.com/apeirora/showroom-msp-private-llm/actions"><img src="https://img.shields.io/github/actions/workflow/status/apeirora/showroom-msp-private-llm/ci.yml?style=flat-square&label=CI" alt="CI"></a>
</p>

---

## What is this?

Private LLM Operator is a Kubernetes operator that turns a simple custom resource into a fully provisioned, token-secured [llama.cpp](https://github.com/ggerganov/llama.cpp) inference server. It is designed to run inside the [ApeiroRA Platform Mesh](https://apeirora.eu/) as a Managed Service Provider (MSP), but works equally well as a standalone operator on any Kubernetes cluster.

**Create an LLM endpoint in seconds:**

```yaml
apiVersion: llm.privatellms.msp/v1alpha1
kind: LLMInstance
metadata:
  name: my-llm
spec:
  model: gemma-3-4b-it
  replicas: 2
```

The operator handles everything: model download, Deployment, Service, Ingress routing via Traefik, and bearer-token authentication -- all reconciled continuously.

## Key Features

- **Declarative LLM provisioning** -- one `LLMInstance` CR per inference endpoint
- **Token-based API security** -- `APITokenRequest` CRs mint bearer tokens backed by Kubernetes Secrets
- **OpenAI-compatible API** -- works out of the box with any OpenAI SDK client
- **Multiple model support** -- TinyLlama, Phi-2, Gemma 3 (1B, 4B, 12B) with GGUF quantization
- **Horizontal scaling** -- set `spec.replicas` and the operator handles the rest
- **Platform Mesh integration** -- sync agent, marketplace metadata, and portal UI included
- **OCM delivery** -- package and deploy via Open Component Model with KRO resource graphs
- **OpenTelemetry tracing** -- built-in OTLP export for observability

## Architecture

```
                    Platform Mesh (KCP)                          MSP Cluster
               ┌──────────────────────────┐           ┌──────────────────────────────┐
               │                          │           │                              │
 User/Portal   │  ┌──────────────────┐    │   Sync    │  ┌────────────────────────┐  │
 ───────────►  │  │ Customer         │    │ ◄────────►│  │ Sync Agent             │  │
               │  │ Workspace        │    │   Agent   │  └────────────┬───────────┘  │
               │  │                  │    │           │               │              │
               │  │  LLMInstance CR  │    │           │  ┌────────────▼───────────┐  │
               │  │  APITokenReq CR  │    │           │  │ Private LLM Operator   │  │
               │  └──────────────────┘    │           │  │                        │  │
               │                          │           │  │  ┌─ Deployment ──────┐ │  │
               │  ┌──────────────────┐    │           │  │  │ llama.cpp server  │ │  │
               │  │ Provider         │    │           │  │  │ + model download  │ │  │
               │  │ Workspace        │    │           │  │  └──────────────────-┘ │  │
               │  │                  │    │           │  │  ┌─ Service ──────────┐│  │
               │  │  APIExport       │    │           │  │  │ ClusterIP :8000   ││  │
               │  │  ProviderMeta    │    │           │  │  └───────────────────-┘│  │
               │  │  ContentConfig   │    │           │  │  ┌─ Ingress ─────────┐│  │
               │  └──────────────────┘    │           │  │  │ /llm/<slug>       ││  │
               └──────────────────────────┘           │  │  └───────────────────-┘│  │
                                                      │  │  ┌─ Auth Middleware ──┐│  │
                                                      │  │  │ ForwardAuth + Token││  │
                                                      │  │  └───────────────────-┘│  │
                                                      │  └────────────────────────┘  │
                                                      └──────────────────────────────┘
```

## Supported Models

| Model | ID | Size | Quantization | Source |
|-------|-----|------|-------------|--------|
| TinyLlama 1.1B Chat | `tinyllama` | ~0.6 GB | Q4_K_M | [HuggingFace](https://huggingface.co/TheBloke/TinyLlama-1.1B-Chat-v1.0-GGUF) |
| Phi-2 | `phi-2` | ~1.6 GB | Q4_0 | [HuggingFace](https://huggingface.co/TheBloke/phi-2-GGUF) |
| Gemma 3 1B IT | `gemma-3-1b-it` | ~0.8 GB | Q4_K_M | [HuggingFace](https://huggingface.co/ggml-org/gemma-3-1b-it-GGUF) |
| Gemma 3 4B IT | `gemma-3-4b-it` | ~2.5 GB | Q4_K_M | [HuggingFace](https://huggingface.co/ggml-org/gemma-3-4b-it-GGUF) |
| Gemma 3 12B IT | `gemma-3-12b-it` | ~7.3 GB | Q4_K_M | [HuggingFace](https://huggingface.co/ggml-org/gemma-3-12b-it-GGUF) |

## Quick Start

### Install with Helm

```sh
helm upgrade --install private-llm \
  oci://ghcr.io/apeirora/charts/private-llm-operator \
  --namespace private-llm-system --create-namespace \
  --set PUBLIC_HOST=llm.example.com
```

### Create an LLM Instance

```sh
kubectl apply -f - <<EOF
apiVersion: llm.privatellms.msp/v1alpha1
kind: LLMInstance
metadata:
  name: my-llm
spec:
  model: tinyllama
  replicas: 1
EOF
```

### Get an API Token

```sh
kubectl apply -f - <<EOF
apiVersion: llm.privatellms.msp/v1alpha1
kind: APITokenRequest
metadata:
  name: my-token
spec:
  instanceName: my-llm
EOF

# Wait for the token to be provisioned
kubectl wait apitokenrequest/my-token --for=jsonpath='{.status.phase}'=Ready --timeout=60s

# Retrieve credentials
SECRET=$(kubectl get apitokenrequest my-token -o jsonpath='{.status.secretName}')
export OPENAI_API_KEY=$(kubectl get secret "$SECRET" -o jsonpath='{.data.OPENAI_API_KEY}' | base64 -d)
export OPENAI_API_URL=$(kubectl get secret "$SECRET" -o jsonpath='{.data.OPENAI_API_URL}' | base64 -d)
```

### Call the API

```sh
curl -sS "$OPENAI_API_URL/v1/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"Hello!"}]}'
```

## Documentation

| Guide | Description |
|-------|-------------|
| [Architecture](docs/architecture.md) | System design, component interactions, data flow |
| [API Reference](docs/api-reference.md) | CRD specifications and API details |
| [Resource Guide](docs/resources.md) | CRD overview with examples |
| [User Guide](docs/user-guide.md) | Platform Mesh integration and portal usage |
| [Helm Installation](docs/installation-helm.md) | Production Helm deployment |
| [OCM Installation](docs/installation-ocm.md) | Open Component Model delivery |
| [Local Development](docs/installation-local.md) | Kind cluster setup for testing |
| [Remote Deployment](docs/installation-remote.md) | Remote cluster with Flux GitOps |
| [Release Flow](docs/RELEASE_FLOW.md) | CI/CD pipeline and versioning |
| [Contributing](CONTRIBUTING.md) | How to contribute |

## Helm Charts

This repository ships four Helm charts, each handling a distinct layer:

| Chart | Purpose |
|-------|---------|
| `private-llm-operator` | Core operator + optional Traefik + portal content server |
| `private-llm-sync-agent` | KCP sync agent + PublishedResource definitions |
| `private-llm-pm-integration` | Platform Mesh metadata (APIExport, ProviderMetadata, ContentConfiguration) |
| `private-llm-operator-ocm` | OCM Component + KRO ResourceGraphDefinition for supply-chain delivery |

## Project Structure

```
.
├── api/v1alpha1/           # CRD type definitions (LLMInstance, APITokenRequest)
├── cmd/main.go             # Operator entrypoint
├── internal/
│   ├── controller/         # Reconcilers for LLMInstance and APITokenRequest
│   └── auth/               # Lightweight bearer-token auth server
├── charts/
│   ├── private-llm-operator/        # Core Helm chart
│   ├── private-llm-sync-agent/      # Sync agent chart
│   ├── private-llm-pm-integration/  # Platform Mesh metadata chart
│   └── private-llm-operator-ocm/    # OCM delivery chart
├── config/                 # Kustomize manifests (CRDs, RBAC, samples)
├── docs/                   # Documentation
└── ocm/                    # OCM bootstrap manifests
```

## License

This project is licensed under the [Apache License 2.0](LICENSE).

---

<p align="center">
  <sub>Built with care by <a href="https://apeirora.eu/">ApeiroRA</a></sub>
</p>

<p align="center">
  <a href="https://apeirora.eu/">
    <img src="docs/assets/eu-funded.svg" alt="Funded by the European Union -- NextGenerationEU. Supported by the Federal Ministry for Economic Affairs and Energy on the basis of a decision by the German Bundestag." width="400" />
  </a>
</p>

<p align="center">
  <sub>Co-funded by the European Union. Views expressed are those of the author(s) only and do not necessarily reflect those of the EU or the granting authority. Neither the EU nor the granting authority can be held responsible for them.</sub>
</p>
