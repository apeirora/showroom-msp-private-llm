# Architecture

This document describes the Private LLM Operator's architecture, its components, and how they interact within the ApeiroRA Platform Mesh ecosystem.

---

## System Overview

The Private LLM Operator follows a layered architecture that separates concerns across three distinct planes:

```mermaid
graph TB
    subgraph "Platform Mesh Control Plane (KCP)"
        AE[APIExport<br>llm.privatellms.msp]
        PM[ProviderMetadata]
        CC[ContentConfiguration]
        PW[Provider Workspace]
        CW[Customer Workspace]
        AB[APIBinding]
        CW --> AB --> AE
        PW --> AE
        PW --> PM
        PW --> CC
    end

    subgraph "MSP Cluster"
        SA[Sync Agent]
        OP[Private LLM Operator]
        AS[Auth Server]
        PR[PublishedResource<br>LLMInstance + APITokenRequest]

        subgraph "Per LLMInstance"
            DEP[Deployment<br>llama.cpp server]
            SVC[Service :8000]
            ING[Ingress /llm/slug]
            MW1[Middleware<br>StripPrefix]
            MW2[Middleware<br>ForwardAuth]
        end

        SA <--> PR
        OP --> DEP
        OP --> SVC
        OP --> ING
        OP --> MW1
        OP --> MW2
        MW2 --> AS
    end

    SA <-->|"bidirectional sync"| CW
    CC -->|"portal UI loads"| ING
```

## Components

### Private LLM Operator

The core component. A Go-based Kubernetes operator built with [Kubebuilder](https://book.kubebuilder.io/) and [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime).

**Two controllers:**

| Controller | Watches | Creates/Manages |
|-----------|---------|-----------------|
| `LLMInstanceReconciler` | `LLMInstance` | Deployment, Service, Ingress, Traefik Middlewares |
| `APITokenRequestReconciler` | `APITokenRequest`, `LLMInstance` | Secret (with OPENAI_API_KEY and OPENAI_API_URL) |

**Key design decisions:**

- **Slug-based routing** -- A required operator policy preserves reviewed legacy URLs and derives new names from the immutable MSP instance UID. Tenant annotations and status are not authority; see [slug migration](slug-policy.md).
- **Owner references** -- All child resources (Deployment, Service, Ingress, Middleware) are owned by the LLMInstance CR, enabling garbage collection on deletion.
- **Finalizers** -- Both controllers use finalizers to ensure cleanup of associated resources.
- **Health probing** -- The operator probes the llama.cpp `/health` endpoint before marking an instance as Ready.

### Auth Server

A lightweight HTTP server embedded in the operator binary, listening on `:8090`.

```mermaid
sequenceDiagram
    participant Client
    participant Traefik
    participant AuthServer as Auth Server (:8090)
    participant K8s as Kubernetes API
    participant LLM as llama.cpp

    Client->>Traefik: GET /llm/<slug>/v1/chat/completions<br>Authorization: Bearer <token>
    Traefik->>AuthServer: GET /auth/verify?namespace=...&instance=...&uid=...&slug=...<br>Authorization: Bearer <token>
    AuthServer->>K8s: Get bound instance; list Secrets in its namespace
    K8s-->>AuthServer: Instance and candidate Secrets
    AuthServer->>AuthServer: Constant-time token comparison
    AuthServer->>K8s: For a match: verify APITokenRequest owner and credential binding
    AuthServer-->>Traefik: 200 OK / 401 Unauthorized
    Traefik->>LLM: Forward request (strip /llm/<slug> prefix)
    LLM-->>Client: Response
```

For hosted routes, the auth server validates tokens by:
1. Requiring a single namespace, instance name, UID and slug from the operator's
   forwardAuth URL. Client-forwarded headers cannot supply the binding.
2. Reading that instance and checking its UID, policy-derived slug and deletion state, then
   listing slug-labeled Secrets only within its namespace.
3. Comparing the bearer token against candidate `OPENAI_API_KEY` values in constant
   time before fetching any token-request owner.
4. Requiring a matching Opaque Secret controlled by the current APITokenRequest
   for this instance, with matching labels, published Secret name, UID and observed
   generation. Deleting resources and API errors deny access.
5. Resolving the same operator-owned slug policy used by reconciliation. Tenant
   annotation collisions cannot select routes; approved legacy entries must not
   overlap each other or the reserved namespace for derived identities.

Direct API reads make revocation checks independent of the manager cache, but
per-request API load requires deployment validation. Existing owned middleware
URLs are upgraded before model-setting and workload gates. The one-replica
Recreate migration can deny requests until reconciliation completes. See the
README's authentication upgrade notes. BYOC inference uses a separate API-key
file and does not pass through this forwardAuth handler.

### Sync Agent

The [KCP API Sync Agent](https://github.com/kcp-dev/api-syncagent) bridges resources between the Platform Mesh control plane (KCP) and the MSP cluster.

**Published Resources:**

| Resource | Direction | Related Resources |
|----------|-----------|-------------------|
| `LLMInstance` | KCP <-> MSP | None |
| `APITokenRequest` | KCP <-> MSP | Secret (synced from MSP to KCP) |

The sync agent uses namespace-per-workspace mapping: each KCP workspace's resources land in a dedicated namespace on the MSP cluster (named after the workspace's cluster name).

### Portal Integration

Three components enable the Platform Mesh portal UI:

1. **Portal content server** -- An nginx pod serving `pm-content.json`, which defines the Luigi micro-frontend navigation structure for the portal UI (list views, create forms, field mappings).

2. **ProviderMetadata** -- KCP resource providing display name, description, icon, and contact information for the marketplace.

3. **ContentConfiguration** -- KCP resource pointing the portal at the content server's URL to load the UI definition.

## LLMInstance Lifecycle

```mermaid
stateDiagram-v2
    [*] --> Created: kubectl apply
    Created --> SlugAssigned: Resolve operator-owned slug policy
    SlugAssigned --> Provisioning: Create Deployment + Service + Ingress + Middlewares
    Provisioning --> Provisioning: Requeue every 5s
    Provisioning --> Ready: Deployment available + Endpoints ready + /health passes
    Ready --> Provisioning: spec.model or spec.replicas changed
    Ready --> Deleting: kubectl delete
    Deleting --> [*]: Finalizer cleanup + ownerRef GC
```

**Detailed provisioning steps:**

1. **Slug assignment** -- The operator policy selects a reviewed legacy slug or derives a new slug from the immutable MSP UID. `status.slug` records the selected value; the old annotation is ignored and may retain stale or tenant-supplied text
2. **Deployment creation** -- An init container downloads the GGUF model from HuggingFace, then the llama.cpp server starts
3. **Service creation** -- ClusterIP service on port 8000
4. **Middleware creation** -- Traefik StripPrefix (removes `/llm/<slug>`) and ForwardAuth (validates bearer token)
5. **Ingress creation** -- Routes `<PUBLIC_HOST>/llm/<slug>` to the service
6. **Readiness evaluation** -- Checks deployment replicas, endpoint readiness, and HTTP health probe
7. **Status update** -- Sets `status.phase=Ready` and `status.endpoint`

## APITokenRequest Lifecycle

```mermaid
stateDiagram-v2
    [*] --> Created: kubectl apply
    Created --> Pending: LLMInstance not found or not Ready
    Pending --> Pending: Requeue every 10s
    Created --> SecretCreated: LLMInstance is Ready
    Pending --> SecretCreated: LLMInstance becomes Ready
    SecretCreated --> TokenReady: Secret with OPENAI_API_KEY + OPENAI_API_URL
    TokenReady --> [*]: Delete cleans up Secret
```

The token controller:
1. Validates that the referenced `LLMInstance` exists and is Ready
2. Generates a cryptographically random 32-byte token (base64url encoded)
3. Creates a Secret containing `OPENAI_API_KEY` (the token) and `OPENAI_API_URL` (the instance endpoint)
4. Labels the Secret with the instance's slug for auth server lookup
5. Touches the APITokenRequest annotation on Secret updates to trigger sync agent re-sync

## Deployment Topology

### Standalone (without Platform Mesh)

```
┌─────────────────────────────────────┐
│           Kubernetes Cluster        │
│                                     │
│  ┌─────────────────────────────┐    │
│  │  private-llm-operator ns    │    │
│  │                             │    │
│  │  Operator + Auth Server     │    │
│  │  Traefik (optional)         │    │
│  └─────────────────────────────┘    │
│                                     │
│  ┌─────────────────────────────┐    │
│  │  user namespace             │    │
│  │                             │    │
│  │  LLMInstance CRs            │    │
│  │  APITokenRequest CRs        │    │
│  │  llama.cpp Deployments      │    │
│  └─────────────────────────────┘    │
└─────────────────────────────────────┘
```

### Platform Mesh (production)

```
┌────────────────────┐     ┌──────────────────────────────────────────┐
│    KCP Control      │     │          MSP Cluster (Gardener shoot)    │
│    Plane            │     │                                          │
│                     │     │  ┌─ private-llm-operator ns ──────────┐  │
│  Provider WS:       │     │  │  Operator + Auth Server             │  │
│   APIExport         │◄───►│  │  Portal Integration (nginx)         │  │
│   ProviderMetadata  │     │  │  Sync Agent                         │  │
│   ContentConfig     │     │  │  PublishedResources                  │  │
│                     │     │  └────────────────────────────────────-┘  │
│  Customer WS:       │     │                                          │
│   LLMInstance       │     │  ┌─ workspace namespace ──────────────┐  │
│   APITokenRequest   │     │  │  LLMInstance (synced from KCP)      │  │
│   Secret (synced)   │     │  │  llama.cpp Deployment + Service     │  │
│                     │     │  │  Ingress + Middlewares               │  │
│                     │     │  │  APITokenRequest + Secret            │  │
│                     │     │  └────────────────────────────────────-┘  │
└────────────────────┘     └──────────────────────────────────────────┘
```

## Relationship with Chat UI

The Private LLM operator provides the backend inference endpoints that the [Chat UI operator](https://github.com/apeirora/showroom-msp-chat-ui) connects to. The flow is:

1. User creates an `LLMInstance` -- the Private LLM operator provisions a llama.cpp server
2. User creates an `APITokenRequest` -- gets `OPENAI_API_KEY` and `OPENAI_API_URL`
3. User creates a Chat UI instance referencing those credentials -- Chat UI connects to the LLM endpoint

The two operators are independent: Private LLM knows nothing about Chat UI, and Chat UI simply uses the OpenAI-compatible API endpoint exposed by Private LLM.

## Observability

The operator integrates OpenTelemetry for distributed tracing:

- **OTLP export** -- When `OTEL_EXPORTER_OTLP_ENDPOINT` is set, traces are exported via OTLP/HTTP
- **Stdout fallback** -- Without an OTLP endpoint, traces are printed to stdout (development mode)
- **Trace context** -- Each reconcile loop creates a span with trace/span IDs logged for correlation
- **Prometheus metrics** -- Standard controller-runtime metrics exposed on `:8080`
- **Health probes** -- `/healthz` and `/readyz` on `:8081`

## See Also

- [Release Flow](release-flow.md) -- how releases are cut and delivered
- [Versioning](versioning.md) -- version numbering and compatibility policy

## Runtime namespace identity

The sync agent uses a bounded `llm-` namespace derived from the account and the KCP source namespace.
The readable part is at most 36 characters. A 20-character SHA3 hash covers the full account/source-namespace tuple.
LLMInstances and APITokenRequests keep their original names in the same runtime namespace.
BYOC and token Secrets follow their primary object's namespace.
Chat UI uses a separate `chat-` namespace and receives its own credential replica through KCP.
The public endpoint uses the operator's status URL and slug, not an assumed runtime namespace.
Do not change this mapping after workloads exist without an explicit migration.
