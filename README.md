<p align="center">
  <img src="assets/icon.png" alt="Private LLM Operator" width="96" />
</p>

<h1 align="center">Private LLM Operator</h1>

<p align="center">
  <strong>Deploy private, self-hosted LLM inference endpoints on Kubernetes -- one CR, one cluster, fully yours.</strong>
</p>

<p align="center">
  <a href="https://github.com/apeirora/showroom-msp-private-llm/releases"><img src="https://img.shields.io/github/v/release/apeirora/showroom-msp-private-llm?style=flat-square&color=blue" alt="Release"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/apeirora/showroom-msp-private-llm?style=flat-square" alt="Go Version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache%202.0-green?style=flat-square" alt="License"></a>
  <a href="https://github.com/apeirora/showroom-msp-private-llm/actions"><img src="https://img.shields.io/github/actions/workflow/status/apeirora/showroom-msp-private-llm/ci.yml?style=flat-square&label=CI" alt="CI"></a>
  <a href="https://api.reuse.software/info/github.com/apeirora/showroom-msp-private-llm"><img src="https://api.reuse.software/badge/github.com/apeirora/showroom-msp-private-llm" alt="REUSE"></a>
</p>

<p align="center">
  Part of the <a href="https://apeirora.eu/">ApeiroRA</a> Platform Mesh ecosystem
</p>

---

## What is Private LLM Operator?

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
| Qwen3 4B | `qwen3-4b` | ~2.4 GB | Q4_K_M | [HuggingFace](https://huggingface.co/Qwen/Qwen3-4B-GGUF) |

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

## Authentication upgrades

The internal `/auth/verify` URL must bind `namespace`, `instance`, `uid`, and
`slug` to the LLMInstance owning the route. The operator creates this URL and
updates existing forwardAuth middlewares. Slug-only URLs and forwarded headers
cannot supply the instance identity. ForwardAuth accepts only Opaque Secrets
in that namespace whose controller owner is the current APITokenRequest for the
instance and whose name matches the request's published Secret. The Secret must
also carry the current instance UID in `llm.privatellms.msp/credential-instance-uid`
and have no BYOC `llm.privatellms.msp/credential-target` binding. Old keys cannot
authenticate a replacement hosted instance while rotation is still pending.

The manager requires a reviewed [slug policy](docs/slug-policy.md) with a fixed
cutover and explicit MSP UID/namespace/name mappings for legacy endpoints. The
policy controls reconciliation and authentication; tenant annotations cannot
choose or change a route. Install its CRD/schema and policy before starting the
new manager. The deployment uses one replica and Recreate; stop any other old
manager so the two naming contracts cannot serve at once.

During an upgrade, old slug-only middlewares fail closed until their instances
reconcile. Existing owned middlewares are upgraded before model-setting or workload reconciliation gates,
but API failures can still delay the update. Verify every middleware has the four
binding parameters before accepting the rollout. Hosted middleware migration is
separate from the BYOC endpoint/key migration described below.
Unannotated legacy hosted keys are denied until their token requests reconcile.
An unchanged name and slug allow the controller to backfill the UID without
changing the key; ambiguous or changed bindings rotate it. Verify token access
after this reconciliation as part of rollout acceptance.
New slugs derive from the immutable MSP instance UID. Explicit legacy entries
preserve reviewed endpoints and reject overlapping route names. BYOC's active
slug must agree with the policy before workload or cleanup-cache writes; an
unmapped legacy instance cannot reconcile or publish a new token endpoint.
Hosted access is denied; an existing BYOC workload can keep serving its previous
key, so a missing map is not remote revocation.
The auth service reads the Kubernetes API directly. An invalid token causes an
instance GET and a namespaced Secret LIST; owner GETs run only after a token
match. API availability and per-request lookup load still require deployment validation. Unrelated forwardAuth options
are preserved when the operator updates its address and header policy.

These checks cover hosted forwardAuth routes. BYOC inference uses a separate
key-file path that requires a Secret controlled by a current APITokenRequest for
the instance; labels alone cannot supply a key.
Token-request retargeting remains supported: reconciliation updates the Secret's
instance label, slug and endpoint to the new target.

## BYOC HTTPS

BYOC endpoints require managed Gardener certificates, instance-specific DNS and a
pinned Traefik TLS listener beside the loopback-only model server. Configure the
operator's `byocTLS` chart settings before migrating existing workloads. There is
no HTTP fallback. Existing legacy resources require verified ownership migration;
upgrading the binary alone does not close an unreachable old plaintext listener.

When an endpoint is unavailable, existing token Secrets advertise
`https://unavailable.invalid`. Target or instance changes rotate model keys;
certificate renewal preserves them. Consumers that cache environment variables
must be stopped and restarted during migration.

See [the implementation](docs/byoc-https-reuse.md) and
[the development pilot](docs/dev-byoc-https-pilot.md) for release, migration and
acceptance checks. Production requires a separate rollout.

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
| [Release Flow](docs/release-flow.md) | CI/CD pipeline and versioning |
| [Versioning](docs/versioning.md) | SemVer and Conventional Commits policy |
| [Contributing](CONTRIBUTING.md) | How to contribute |

## Helm Charts

This repository ships four Helm charts, each handling a distinct layer:

| Chart | Purpose | Registry |
|-------|---------|----------|
| `private-llm-operator` | Core operator + optional Traefik + portal content server | `oci://ghcr.io/apeirora/charts/private-llm-operator` |
| `private-llm-sync-agent` | KCP sync agent + PublishedResource definitions | `oci://ghcr.io/apeirora/charts/private-llm-sync-agent` |
| `private-llm-pm-integration` | Platform Mesh metadata (APIExport, ProviderMetadata, ContentConfiguration) | `oci://ghcr.io/apeirora/charts/private-llm-pm-integration` |
| `private-llm-operator-ocm` | OCM Component + KRO ResourceGraphDefinition for supply-chain delivery | `oci://ghcr.io/apeirora/charts/private-llm-operator-ocm` |

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

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on how to contribute to this project.

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

Stable patch releases use a final `Release-As` footer to remove an inherited `-rc` suffix.
