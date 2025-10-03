# API Guide

## Overview
The private-llm operator provisions a [llama.cpp](https://github.com/ggerganov/llama.cpp) HTTP server for every `LLMInstance`. Each instance is published through Traefik with a unique slugged path such as `https://<public-host>/llm/<slug>/<instance-name>`. This guide shows how to discover the endpoint, issue a token, and call the API.

## Prerequisites
- A deployed `LLMInstance` (see `README-resources.md` if you need one).
- Access to the Kubernetes cluster with `kubectl`.

## 1. Discover the public endpoint
```sh
INSTANCE=llminstance-sample
NAMESPACE=private-llm-system
ENDPOINT=$(kubectl get llminstance "$INSTANCE" -n "$NAMESPACE" -o jsonpath='{.status.endpoint}')
echo "Endpoint: $ENDPOINT"
```

The endpoint ends without a trailing slash, e.g. `http://private-llm.msp/llm/abc123xyz/llminstance-sample`.

## 2. Issue an access token
Create a `TokenRequest` referencing the instance:

```yaml
apiVersion: llm.example.com/v1alpha1
kind: TokenRequest
metadata:
  name: demo-token
  namespace: ${NAMESPACE}
spec:
  instanceName: ${INSTANCE}
  description: "Demo token for API testing"
```

```sh
kubectl apply -f tokenrequest-demo.yaml
kubectl wait tokenrequest/demo-token -n "$NAMESPACE" --for=jsonpath='{.status.phase}'=Ready --timeout=30s
TOKEN_SECRET=$(kubectl get tokenrequest demo-token -n "$NAMESPACE" -o jsonpath='{.status.secretName}')
BEARER_TOKEN=$(kubectl get secret "$TOKEN_SECRET" -n "$NAMESPACE" -o jsonpath='{.data.OPENAI_API_KEY}' | base64 -d)
echo "Bearer token stored in Secret: $TOKEN_SECRET"
```

Every request to the public endpoint must include `Authorization: Bearer $BEARER_TOKEN`. Traefik validates the header through the operator’s lightweight auth service, so the llama.cpp pod never sees raw secrets.

## 3. Call the API
The llama.cpp server exposes straightforward HTTP endpoints.

**Health check**
```sh
curl -s "$ENDPOINT/health"
```

**Minimal text completion**
```sh
curl -sS "$ENDPOINT/completion" \
  -H "Authorization: Bearer $BEARER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"Hello there!","stream":false}'
```

Example response:
```json
{"content":[{"type":"output_text","text":"Hello! I am TinyLlama running in your private cluster. How can I assist you today?"}],"id":"cmpl-123","model":"tinyllama.gguf"}
```

For OpenAI-compatible clients, send the same bearer token to `$ENDPOINT/v1/chat/completions`.

## 4. Clean up (optional)
```sh
kubectl delete tokenrequest demo-token -n "$NAMESPACE"
kubectl delete secret "$TOKEN_SECRET" -n "$NAMESPACE"
```

The `LLMInstance` keeps running until you delete it. Removing the instance automatically tears down the Deployment, Service, Ingress, and associated middleware resources.

