# API Reference

Complete reference for the Private LLM Operator's custom resources and HTTP API.

---

## Custom Resource Definitions

The operator registers two CRDs in the `llm.privatellms.msp` API group.

---

### LLMInstance

Requests a private llama.cpp inference endpoint. The operator reconciles it into a Deployment, Service, Ingress, and Traefik middlewares.

**API Version:** `llm.privatellms.msp/v1alpha1`

#### Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `spec.model` | `string` | `tinyllama` | Model identifier. One of: `tinyllama`, `phi-2`, `gemma-3-1b-it`, `gemma-3-4b-it`, `gemma-3-12b-it` |
| `spec.replicas` | `int32` | `1` | Number of llama.cpp server pods. Minimum: 0 (treated as 1) |

#### Status

| Field | Type | Description |
|-------|------|-------------|
| `status.phase` | `string` | High-level lifecycle state: `Provisioning` or `Ready` |
| `status.endpoint` | `string` | Public URL for the inference endpoint (e.g., `https://host/llm/abc123`) |
| `status.observedGeneration` | `int64` | Last generation processed by the controller |
| `status.conditions` | `[]Condition` | Standard Kubernetes conditions (see below) |

#### Conditions

| Type | Description |
|------|-------------|
| `Ready` | `True` when Deployment is available, endpoints are ready, and `/health` passes |

#### Print Columns

```
NAME    MODEL       PHASE        ENDPOINT
my-llm  tinyllama   Ready        https://host/llm/abc123
```

#### Annotations

| Annotation | Set By | Description |
|-----------|--------|-------------|
| `llm.privatellms.msp/slug` | Controller | Random URL slug for routing (auto-generated) |

#### Full Example

```yaml
apiVersion: llm.privatellms.msp/v1alpha1
kind: LLMInstance
metadata:
  name: production-llm
  namespace: my-team
spec:
  model: gemma-3-4b-it
  replicas: 3
```

After reconciliation:

```yaml
status:
  phase: Ready
  endpoint: https://llm.example.com/llm/aB3xK9mLp2Qz
  observedGeneration: 1
  conditions:
    - type: Ready
      status: "True"
      reason: Provisioned
      message: LLM instance is ready
```

---

### APITokenRequest

Mints a bearer token for an existing `LLMInstance`. The token is stored in a Kubernetes Secret.

**API Version:** `llm.privatellms.msp/v1alpha1`

#### Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `spec.instanceName` | `string` | Yes | Name of the target LLMInstance (must be in the same namespace) |
| `spec.description` | `string` | No | Human-friendly description for this token |

#### Status

| Field | Type | Description |
|-------|------|-------------|
| `status.phase` | `string` | Lifecycle state: `Pending` or `Ready` |
| `status.secretName` | `string` | Name of the Secret containing the generated credentials |
| `status.observedGeneration` | `int64` | Last generation processed by the controller |
| `status.conditions` | `[]Condition` | Standard Kubernetes conditions |

#### Conditions

| Type | Reason | Description |
|------|--------|-------------|
| `Ready` | `Provisioned` | Token generated and Secret created |
| `Ready` | `InstanceNotFound` | Referenced LLMInstance does not exist |
| `Ready` | `InstanceNotReady` | LLMInstance exists but is not yet Ready |

#### Print Columns

```
NAME       INSTANCE   SECRET              PHASE
my-token   my-llm     my-token-token      Ready
```

#### Generated Secret

The controller creates a Secret named `<apitokenrequest-name>-token` containing:

| Key | Description |
|-----|-------------|
| `OPENAI_API_KEY` | Cryptographically random bearer token (32 bytes, base64url) |
| `OPENAI_API_URL` | Full endpoint URL of the LLMInstance (e.g., `https://host/llm/slug`) |

**Secret labels:**

| Label | Value |
|-------|-------|
| `app.kubernetes.io/name` | `llm-token` |
| `llm.privatellms.msp/instance` | LLMInstance name |
| `llm.privatellms.msp/apitokenrequest` | APITokenRequest name |
| `llm.privatellms.msp/slug` | Instance slug (for auth server lookup) |
| `apeirora.eu/llm-api-compatibility` | `openai` |

#### Full Example

```yaml
apiVersion: llm.privatellms.msp/v1alpha1
kind: APITokenRequest
metadata:
  name: ci-token
  namespace: my-team
spec:
  instanceName: production-llm
  description: "Token for CI pipeline integration tests"
```

After reconciliation:

```yaml
status:
  phase: Ready
  secretName: ci-token-token
  observedGeneration: 1
  conditions:
    - type: Ready
      status: "True"
      reason: Provisioned
      message: Token generated
```

---

## HTTP API (llama.cpp)

Each LLMInstance exposes the standard llama.cpp HTTP API through its endpoint. All requests require the bearer token from an APITokenRequest.

### Authentication

All requests must include:

```
Authorization: Bearer <OPENAI_API_KEY>
```

Traefik's ForwardAuth middleware validates the token before the request reaches the llama.cpp server.

### Endpoints

#### Health Check

```
GET <OPENAI_API_URL>/health
```

Returns `200 OK` with status JSON when the model is loaded and ready.

#### Text Completion

```
POST <OPENAI_API_URL>/completion
Content-Type: application/json

{
  "prompt": "Hello there!",
  "stream": false
}
```

#### Chat Completion (OpenAI-compatible)

```
POST <OPENAI_API_URL>/v1/chat/completions
Content-Type: application/json

{
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "What is Kubernetes?"}
  ]
}
```

> **Tip:** The OpenAI-compatible endpoint works with any OpenAI SDK client. Set the base URL to `<OPENAI_API_URL>/v1` and the API key to `<OPENAI_API_KEY>`.

### Example with curl

```sh
# Retrieve credentials
SECRET=$(kubectl get apitokenrequest my-token -o jsonpath='{.status.secretName}')
OPENAI_API_KEY=$(kubectl get secret "$SECRET" -o jsonpath='{.data.OPENAI_API_KEY}' | base64 -d)
OPENAI_API_URL=$(kubectl get secret "$SECRET" -o jsonpath='{.data.OPENAI_API_URL}' | base64 -d)

# Health check
curl -s "$OPENAI_API_URL/health"

# Chat completion
curl -sS "$OPENAI_API_URL/v1/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

### Example with Python (OpenAI SDK)

```python
from openai import OpenAI

client = OpenAI(
    api_key="<OPENAI_API_KEY>",
    base_url="<OPENAI_API_URL>/v1",
)

response = client.chat.completions.create(
    model="tinyllama",
    messages=[{"role": "user", "content": "Explain Kubernetes in one sentence."}],
)
print(response.choices[0].message.content)
```

### Example with ChatGPT-Next-Web

```sh
docker run --rm -p 3000:3000 \
  -e OPENAI_API_KEY="$OPENAI_API_KEY" \
  -e BASE_URL="$OPENAI_API_URL" \
  -e HIDE_USER_API_KEY=1 \
  -e DISABLE_FAST_LINK=1 \
  -e DEFAULT_MODEL='/models/tinyllama.gguf' \
  -e CUSTOM_MODELS='-all,+/models/tinyllama.gguf' \
  yidadaa/chatgpt-next-web:latest
```

Open `http://localhost:3000` to chat with your private LLM.

## RBAC

The operator requires the following cluster-level permissions:

| API Group | Resources | Verbs |
|-----------|-----------|-------|
| `llm.privatellms.msp` | `llminstances`, `llminstances/status`, `llminstances/finalizers` | get, list, watch, create, update, patch, delete |
| `llm.privatellms.msp` | `apitokenrequests`, `apitokenrequests/status`, `apitokenrequests/finalizers` | get, list, watch, create, update, patch, delete |
| `apps` | `deployments` | get, list, watch, create, update, patch, delete |
| *(core)* | `services`, `secrets`, `configmaps`, `endpoints`, `events` | get, list, watch, create, update, patch, delete |
| `networking.k8s.io` | `ingresses` | get, list, watch, create, update, patch, delete |
| `traefik.io` | `middlewares` | get, list, watch, create, update, patch, delete |
| `coordination.k8s.io` | `leases` | get, list, watch, create, update, patch, delete |
