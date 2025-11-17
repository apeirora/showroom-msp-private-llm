# API Guide

## Overview
The private-llm operator provisions a [llama.cpp](https://github.com/ggerganov/llama.cpp) HTTP server for every `LLMInstance`. Each instance is published through Traefik with a unique slugged path such as `https://<public-host>/llm/<slug>`. This guide shows how to discover the endpoint, issue a token, and call the API.

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

The endpoint ends without a trailing slash, e.g. `http://private-llm.msp/llm/abc123xyz`.

## 2. Issue an access token
Create an `APITokenRequest` referencing the instance:

```yaml
apiVersion: llm.privatellms.msp/v1alpha1
kind: APITokenRequest
metadata:
  name: demo-token
  namespace: ${NAMESPACE}
spec:
  instanceName: ${INSTANCE}
  description: "Demo token for API testing"
```

```sh
kubectl apply -f apitokenrequest-demo.yaml
kubectl wait apitokenrequest/demo-token -n "$NAMESPACE" --for=jsonpath='{.status.phase}'=Ready --timeout=30s
TOKEN_SECRET=$(kubectl get apitokenrequest demo-token -n "$NAMESPACE" -o jsonpath='{.status.secretName}')
OPENAI_API_KEY=$(kubectl get secret "$TOKEN_SECRET" -n "$NAMESPACE" -o jsonpath='{.data.OPENAI_API_KEY}' | base64 -d)
OPENAI_API_URL=$(kubectl get secret "$TOKEN_SECRET" -n "$NAMESPACE" -o jsonpath='{.data.OPENAI_API_URL}' | base64 -d)
echo "Secret $TOKEN_SECRET now contains OPENAI_API_KEY and OPENAI_API_URL"
```

Every request to the public endpoint must include `Authorization: Bearer $OPENAI_API_KEY`. Traefik validates the header through the operator’s lightweight auth service, so the llama.cpp pod never sees raw secrets.

## 3. Call the API
The llama.cpp server exposes straightforward HTTP endpoints.

**Health check**
```sh
curl -s "$OPENAI_API_URL/health"
```

**Minimal text completion**
```sh
curl -sS "$OPENAI_API_URL/completion" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
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
kubectl delete apitokenrequest demo-token -n "$NAMESPACE"
kubectl delete secret "$TOKEN_SECRET" -n "$NAMESPACE"
```

The `LLMInstance` keeps running until you delete it. Removing the instance automatically tears down the Deployment, Service, Ingress, and associated middleware resources.

