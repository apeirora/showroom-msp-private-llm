# Remote Deployment

Deploy the Private LLM Operator to a remote Kubernetes cluster using Flux GitOps, as used in the ApeiroRA Platform Mesh production and development environments.

---

## Overview

In the Platform Mesh deployment model, three Helm charts are deployed via Flux to separate namespaces:

```
MSP Cluster
├── private-llm-operator (namespace)
│   ├── HelmRelease: private-llm-operator      ← Core operator + portal content
│   └── HelmRelease: private-llm-pm-integration ← Platform Mesh metadata (via kubeConfig)
└── api-syncagent (namespace)
    └── HelmRelease: private-llm-sync-agent     ← Sync agent + PublishedResources
```

## Prerequisites

- MSP cluster provisioned via Gardener (or any Kubernetes 1.26+ cluster)
- Flux installed on the cluster
- Access to the Helm chart repository (`ghcr.io/apeirora/charts`)
- KCP kubeconfig for Platform Mesh integration (stored in a Secret)
- GHCR image pull secret

## GitOps Structure

The deployment is defined in `showroom-msp-cluster-infra`:

```
apps/private-llm/
├── base/
│   ├── kustomization.yaml
│   ├── namespace.yaml
│   ├── pm-kubeconfig-external-secret.yaml    # KCP kubeconfig from OpenBao
│   ├── ghcr-showroom-external-secret.yaml    # GHCR pull secret from OpenBao
│   ├── operator-helm.yaml                    # HelmRelease for the operator
│   ├── sync-agent-helm.yaml                  # HelmRelease for the sync agent
│   ├── pm-integration-helm.yaml              # HelmRelease for PM metadata
│   └── pm-integration-kustomization.yaml     # Kustomization for PM metadata overlays
└── overlays/
    ├── dev/
    │   ├── kustomization.yaml
    │   ├── operator-values.yaml
    │   ├── sync-agent-values.yaml
    │   └── pm-integration-values.yaml
    └── prod/
        ├── kustomization.yaml
        ├── operator-values.yaml
        ├── sync-agent-values.yaml
        └── pm-integration-values.yaml
```

## Operator Configuration (Dev Example)

The dev overlay configures the operator for the cc-d2 environment:

```yaml
# operator-values.yaml
PUBLIC_HOST: llm.private-llm.msp02.dev.showroom.apeirora.eu
PUBLIC_SCHEME: https

tls:
  secretName: private-llm

image:
  repository: ghcr.io/apeirora/private-llm-controller
  pullPolicy: Always

imagePullSecrets:
  - name: ghcr-showroom-secret

traefik:
  enabled: false    # Using cluster-level ingress controller

ingress:
  extraAnnotations:
    dns.gardener.cloud/class: "garden"
    dns.gardener.cloud/dnsnames: "llm.private-llm.msp02.dev.showroom.apeirora.eu"
    cert.gardener.cloud/purpose: "managed"

portalIntegration:
  enabled: true
  contentPath: "/pm-content.json"
```

> **Note:** On Gardener clusters, DNS and TLS certificates are managed automatically via the `dns.gardener.cloud` and `cert.gardener.cloud` annotations.

## Sync Agent Configuration

```yaml
# sync-agent-values.yaml
syncAgentOperator:
  enabled: true
  apiExportName: llm.privatellms.msp
  agentName: llm-agent
  kcpKubeconfig: pm-kubeconfig
  extraFlags:
    - --log-debug
    - --published-resource-selector=app.kubernetes.io/name=private-llm-sync-agent

publishedResources:
  enabled: true
  namespace: private-llm-operator
```

The sync agent:
- Connects to KCP using the `pm-kubeconfig` Secret
- Watches for `LLMInstance` and `APITokenRequest` resources created in KCP workspaces
- Syncs them to the MSP cluster as namespaced resources
- Syncs status and related Secrets back to KCP

## Platform Mesh Integration Configuration

```yaml
# pm-integration-values.yaml
publicHost: llm.private-llm.msp02.dev.showroom.apeirora.eu
publicScheme: https
contentPath: /pm-content.json
```

This chart is applied to the KCP control plane (not the MSP cluster) using `kubeConfig.secretRef` in the HelmRelease. It creates:

- **APIExport** `llm.privatellms.msp` with permission claims for namespaces, events, and secrets
- **ProviderMetadata** with display name, description, and icon for the marketplace
- **ContentConfiguration** pointing at the portal content server URL

## Deployment Topology

### Development (cc-d2)

| Component | MSP Cluster | Namespace |
|-----------|-------------|-----------|
| Operator | msp02 | `private-llm-operator` |
| Sync Agent | msp02 | `private-llm-operator` |
| Portal Integration | msp02 (applied to KCP) | Provider workspace |

### Production (cc-two)

| Component | MSP Cluster | Namespace |
|-----------|-------------|-----------|
| Operator | msp02 | `private-llm-operator` |
| Sync Agent | msp02 | `private-llm-operator` |
| Portal Integration | msp02 (applied to KCP) | Provider workspace |

## Manual Deployment (Without Flux)

If deploying without Flux:

### 1. Deploy the Operator

```sh
kubectl create namespace private-llm-operator

helm upgrade --install private-llm-operator \
  oci://ghcr.io/apeirora/charts/private-llm-operator \
  --namespace private-llm-operator \
  --set PUBLIC_HOST=llm.example.com \
  --set PUBLIC_SCHEME=https \
  --set tls.secretName=private-llm \
  --set traefik.enabled=false \
  --set portalIntegration.enabled=true \
  --set 'imagePullSecrets[0].name=ghcr-credentials'
```

### 2. Deploy the Sync Agent

```sh
# Ensure KCP kubeconfig Secret exists
kubectl -n private-llm-operator get secret pm-kubeconfig

helm upgrade --install private-llm-sync-agent \
  oci://ghcr.io/apeirora/charts/private-llm-sync-agent \
  --namespace private-llm-operator \
  --set syncAgentOperator.enabled=true \
  --set syncAgentOperator.apiExportName=llm.privatellms.msp \
  --set syncAgentOperator.kcpKubeconfig=pm-kubeconfig \
  --set publishedResources.enabled=true \
  --set publishedResources.namespace=private-llm-operator
```

### 3. Deploy Platform Mesh Metadata

Apply to the KCP control plane (using a kubeconfig for the provider workspace):

```sh
helm upgrade --install private-llm-portal \
  oci://ghcr.io/apeirora/charts/private-llm-pm-integration \
  --kubeconfig=<kcp-provider-kubeconfig> \
  --set publicHost=llm.example.com \
  --set publicScheme=https
```

## Verification

```sh
MSP_KUBECONFIG=zz_clusters/openmcp/mcp/envs/cc-d2/msps/msp02-kubeconfig.yaml

# Check operator pod
kubectl --kubeconfig=$MSP_KUBECONFIG get pods -n private-llm-operator

# Check sync agent
kubectl --kubeconfig=$MSP_KUBECONFIG get pods -n private-llm-operator -l app.kubernetes.io/name=api-syncagent

# Check PublishedResources
kubectl --kubeconfig=$MSP_KUBECONFIG get publishedresources -n private-llm-operator

# Check synced LLMInstances
kubectl --kubeconfig=$MSP_KUBECONFIG get llminstances -A

# Check portal content is accessible
curl -sk "https://llm.private-llm.msp02.dev.showroom.apeirora.eu/pm-content.json" | head -5
```

## Troubleshooting

### Sync agent cannot connect to KCP

```sh
# Check the KCP kubeconfig Secret exists
kubectl --kubeconfig=$MSP_KUBECONFIG get secret pm-kubeconfig -n private-llm-operator

# Check sync agent logs
kubectl --kubeconfig=$MSP_KUBECONFIG logs -n private-llm-operator deploy/private-llm-sync-agent --tail=50
```

### RBAC errors in sync agent logs

The sync agent needs a ClusterRoleBinding. Check for namespace mismatches:

```sh
# Where does the RBAC binding point?
kubectl --kubeconfig=$MSP_KUBECONFIG get clusterrolebinding api-syncagent:privatellm \
  -o jsonpath='{.subjects[0].namespace}'

# Where does the sync agent actually run?
kubectl --kubeconfig=$MSP_KUBECONFIG get deploy -A | grep sync-agent
```

If they differ, patch the binding:

```sh
kubectl --kubeconfig=$MSP_KUBECONFIG patch clusterrolebinding api-syncagent:privatellm \
  --type=json -p='[{"op":"replace","path":"/subjects/0/namespace","value":"private-llm-operator"}]'
```

### Portal content not loading

```sh
# Check the portal integration pod
kubectl --kubeconfig=$MSP_KUBECONFIG get pods -n private-llm-operator -l app.kubernetes.io/component=portal-integration

# Check the ingress
kubectl --kubeconfig=$MSP_KUBECONFIG get ingress -n private-llm-operator
```
