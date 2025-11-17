# Resource Guide

The operator installs two custom resources in the `llm.privatellms.msp` API group.

## LLMInstance
- **What it does:** requests a private llama.cpp endpoint. The operator turns it into a Deployment, Service, and Ingress.
- **Key fields:**
  - `spec.model` (optional) – choose `tinyllama` (default) or `phi-2`.
  - `spec.replicas` (optional) – number of pods, defaults to 1.
- **What you read back:**
  - `status.phase` – `Pending`, `Ready`, etc.
  - `status.endpoint` – public URL (`http://<host>/llm/<slug>`).

Minimal example:
```yaml
apiVersion: llm.privatellms.msp/v1alpha1
kind: LLMInstance
metadata:
  name: llminstance-sample
spec:
  model: tinyllama
```

Scale by patching `spec.replicas`. Deleting the resource removes the server and routing objects.

## APITokenRequest
- **What it does:** mints a bearer token for an existing `LLMInstance`.
- **Key fields:**
  - `spec.instanceName` – target instance in the same namespace.
  - `spec.description` (optional) – for operator notes.
- **What you read back:**
  - `status.secretName` – Secret storing `OPENAI_API_KEY` and `OPENAI_API_URL`.
  - `status.phase` – becomes `Ready` once the Secret exists.

Minimal example:
```yaml
apiVersion: llm.privatellms.msp/v1alpha1
kind: APITokenRequest
metadata:
  name: sample-client
spec:
  instanceName: llminstance-sample
```

To fetch the token and endpoint:
```sh
SECRET=$(kubectl get apitokenrequest sample-client -o jsonpath='{.status.secretName}')
OPENAI_API_KEY=$(kubectl get secret "$SECRET" -o jsonpath='{.data.OPENAI_API_KEY}' | base64 -d)
OPENAI_API_URL=$(kubectl get secret "$SECRET" -o jsonpath='{.data.OPENAI_API_URL}' | base64 -d)
printf "Key: %s\nURL: %s\n" "$OPENAI_API_KEY" "$OPENAI_API_URL"
```

## Typical Flow
1. Create an `LLMInstance`.
2. Wait for `status.endpoint` to show the URL.
3. Create an `APITokenRequest` and retrieve its token.
4. Call the API with `Authorization: Bearer <token>` (see `README-api.md`).
