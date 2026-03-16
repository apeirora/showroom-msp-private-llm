# Helm Installation

Deploy the Private LLM Operator to a Kubernetes cluster using Helm.

---

## Prerequisites

- Kubernetes 1.26+
- Helm 3.14+
- kubectl configured for your cluster
- (Optional) Traefik ingress controller if not using the bundled one

## Install from OCI Registry

The chart is published to GHCR as an OCI artifact:

```sh
helm upgrade --install private-llm \
  oci://ghcr.io/apeirora/charts/private-llm-operator \
  --version 2.8.1 \
  --namespace private-llm-system --create-namespace \
  --set PUBLIC_HOST=llm.example.com \
  --set PUBLIC_SCHEME=https
```

## Install from Local Source

```sh
helm upgrade --install private-llm charts/private-llm-operator \
  --namespace private-llm-system --create-namespace \
  --dependency-update \
  --set PUBLIC_HOST=llm.example.com
```

## Configuration

### Core Values

| Parameter | Description | Default |
|-----------|-------------|---------|
| `PUBLIC_HOST` | Hostname for Ingress rules and status endpoints | `localhost` |
| `PUBLIC_SCHEME` | URL scheme (`http` or `https`) | `http` |
| `tls.secretName` | TLS Secret for Ingress (required for HTTPS) | `""` |
| `image.repository` | Operator container image | `ghcr.io/apeirora/private-llm-controller` |
| `image.tag` | Image tag | `2.8.1` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `imagePullSecrets` | Image pull secrets | `[]` |

### Traefik Configuration

The chart bundles Traefik as an optional dependency. When enabled, it deploys a Traefik instance dedicated to the LLM operator.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `traefik.enabled` | Deploy bundled Traefik | `true` |
| `traefik.service.type` | Traefik service type | `LoadBalancer` |
| `ingress.ports.web.nodePort` | NodePort for HTTP (if type=NodePort) | `30080` |
| `ingress.ports.websecure.nodePort` | NodePort for HTTPS (if type=NodePort) | `30443` |

> **Tip:** If your cluster already has a Traefik (or another IngressClass-compatible controller), set `traefik.enabled=false` and configure `ingress.className` to match your controller.

### Ingress Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `ingress.className` | IngressClass for the portal/content server Ingress only (LLM instance Ingresses always use `traefik`) | `""` (defaults to `traefik`) |
| `ingress.extraAnnotations` | Additional annotations for managed Ingresses | `{}` |

For Gardener-managed DNS and certificates:

```yaml
ingress:
  extraAnnotations:
    dns.gardener.cloud/class: "garden"
    dns.gardener.cloud/dnsnames: "llm.example.com"
    cert.gardener.cloud/purpose: "managed"
```

### Auth Server

| Parameter | Description | Default |
|-----------|-------------|---------|
| `auth.externalURL` | External URL for Traefik to reach the auth server | Auto-derived from POD_NAMESPACE |

### Portal Integration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `portalIntegration.enabled` | Deploy portal content server (nginx) | `false` |
| `portalIntegration.contentPath` | URL path for `pm-content.json` | `/pm-content.json` |
| `portalIntegration.contentVariant` | Content variant (`default` or `legacy`) | `default` |
| `portalIntegration.image.repository` | Nginx image | `nginx` |
| `portalIntegration.image.tag` | Nginx image tag | `1.25` |

### Extra Environment Variables

```yaml
extraEnv:
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "http://otel-collector:4318"
```

## Example Configurations

### Production with HTTPS

```sh
helm upgrade --install private-llm \
  oci://ghcr.io/apeirora/charts/private-llm-operator \
  --namespace private-llm-operator --create-namespace \
  --set PUBLIC_HOST=llm.production.example.com \
  --set PUBLIC_SCHEME=https \
  --set tls.secretName=llm-tls-cert \
  --set traefik.enabled=false \
  --set portalIntegration.enabled=true \
  --set 'imagePullSecrets[0].name=ghcr-credentials'
```

### Development with NodePort

```sh
helm upgrade --install private-llm charts/private-llm-operator \
  --namespace private-llm-system --create-namespace \
  --dependency-update \
  --set PUBLIC_HOST=localhost \
  --set traefik.service.type=NodePort \
  --set ingress.ports.web.nodePort=30080 \
  --set ingress.ports.websecure.nodePort=30443
```

### Custom Image

```sh
helm upgrade --install private-llm charts/private-llm-operator \
  --namespace private-llm-system --create-namespace \
  --set image.repository=my-registry.example.com/private-llm-controller \
  --set image.tag=v2.8.1 \
  --set image.pullPolicy=Always \
  --set 'imagePullSecrets[0].name=my-registry-secret'
```

## Verifying the Installation

```sh
# Check the operator pod
kubectl get pods -n private-llm-system

# Check CRDs are installed
kubectl get crd llminstances.llm.privatellms.msp
kubectl get crd apitokenrequests.llm.privatellms.msp

# Check operator logs
kubectl logs -n private-llm-system deploy/private-llm-controller-manager -f
```

## Uninstalling

```sh
# Delete all LLMInstances first (this cleans up child resources)
kubectl delete llminstances --all -A

# Remove the Helm release
helm uninstall private-llm -n private-llm-system

# (Optional) Remove CRDs manually
kubectl delete crd llminstances.llm.privatellms.msp
kubectl delete crd apitokenrequests.llm.privatellms.msp

# (Optional) Remove the namespace
kubectl delete namespace private-llm-system
```

> **Warning:** Deleting CRDs will remove all LLMInstance and APITokenRequest resources across the cluster.

## Next Steps

- [Create your first LLMInstance](resources.md)
- [Set up Platform Mesh integration](user-guide.md)
- [Deploy via OCM](installation-ocm.md)
