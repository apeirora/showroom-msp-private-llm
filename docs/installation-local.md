# Local Installation

Install the Private LLM provider on a local [Platform Mesh](https://github.com/platform-mesh/helm-charts) setup with two `helm install` commands.

## Prerequisites

- [Platform Mesh local-setup](https://github.com/platform-mesh/helm-charts/tree/main/local-setup) running (`task local-setup`)
- [kubectl-kcp plugin](https://github.com/kcp-dev/kcp/releases) — provides `kubectl kcp workspace`
- [Helm](https://helm.sh/) 3.14+

Export the admin kubeconfig path and KCP URL (only needed once per shell):

```sh
export HELM_CHARTS_DIR="$(pwd)"       # assumes CWD is the platform-mesh/helm-charts repo
export KCP="$HELM_CHARTS_DIR/.secret/kcp/admin.kubeconfig"
export KCP_URL="https://localhost:8443"
```

## Install

Clone this repo and run the two installs from its root. The MSP-side umbrella
installs the operator and sync agent on the Kind cluster; the KCP-side
umbrella installs the APIExport and portal metadata in a provider workspace on
KCP.

### 1. Create the provider workspace on KCP

```sh
kubectl kcp workspace create providers --type=root:providers --ignore-existing --kubeconfig=$KCP
kubectl kcp workspace use root:providers --kubeconfig=$KCP
kubectl kcp workspace create private-llm --type=root:provider --ignore-existing --kubeconfig=$KCP
```

### 2. Install the MSP-side umbrella (Kind cluster)

```sh
helm install private-llm charts/private-llm-msp-app \
  --namespace private-llm --create-namespace \
  --set-file kcpKubeconfig.adminContent=$KCP
```

This creates the operator Deployment, the sync-agent Deployment, and a
`pm-kubeconfig` Secret for the sync-agent — built from the admin kubeconfig
you passed in, rewritten to point at KCP's in-cluster front-proxy under
`root:providers:private-llm`.

### 3. Install the KCP-side umbrella (KCP workspace)

```sh
helm install private-llm-pm charts/private-llm-pm-app \
  --kubeconfig=$KCP \
  --kube-apiserver="$KCP_URL/clusters/root:providers:private-llm" \
  --namespace private-llm --create-namespace
```

This creates the `llm.privatellms.msp` APIExport, ProviderMetadata, and
ContentConfiguration in the `root:providers:private-llm` workspace.

## Verify

```sh
# Operator + sync agent on the Kind cluster
kubectl -n private-llm get pods

# APIExport in KCP
kubectl get apiexports --kubeconfig=$KCP \
  --server="$KCP_URL/clusters/root:providers:private-llm"
```

Sync-agent logs should show a successful connection to KCP:

```sh
kubectl -n private-llm logs deploy/llm-agent --tail=10
```

## Create an LLMInstance (consumer side)

Create a consumer workspace, bind to the APIExport, and provision an LLM:

```sh
kubectl kcp workspace create orgs --type=root:orgs --ignore-existing --kubeconfig=$KCP
kubectl kcp workspace use root:orgs --kubeconfig=$KCP
kubectl kcp workspace create demo --type=root:org --ignore-existing --kubeconfig=$KCP

kubectl apply --kubeconfig=$KCP --server="$KCP_URL/clusters/root:orgs:demo" -f - <<'EOF'
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: llm-binding
spec:
  reference:
    export:
      path: root:providers:private-llm
      name: llm.privatellms.msp
---
apiVersion: llm.privatellms.msp/v1alpha1
kind: LLMInstance
metadata:
  name: demo-llm
  namespace: default
spec:
  model: tinyllama
  replicas: 1
EOF

kubectl get llminstances --kubeconfig=$KCP \
  --server="$KCP_URL/clusters/root:orgs:demo" -w
```

The sync-agent mirrors the CR to the Kind cluster, the operator downloads the
model and starts llama.cpp, and `phase: Ready` reflects back to KCP.

## Access the LLM

```sh
NS=$(kubectl get llminstances -A -o jsonpath='{.items[0].metadata.namespace}')
kubectl port-forward -n "$NS" svc/demo-llm-llama 8000:8000
curl http://localhost:8000/v1/models
```

## Cleanup

```sh
helm uninstall private-llm -n private-llm
helm uninstall private-llm-pm -n private-llm \
  --kubeconfig=$KCP \
  --kube-apiserver="$KCP_URL/clusters/root:providers:private-llm"
```

## Troubleshooting

### Sync-agent can't reach KCP

Check the generated kubeconfig Secret:

```sh
kubectl -n private-llm get secret pm-kubeconfig -o jsonpath='{.data.kubeconfig}' | base64 -d | grep server:
```

Should point at `frontproxy-front-proxy.platform-mesh-system.svc.cluster.local:6443/clusters/root:providers:private-llm`, not `localhost:8443`. If it still says `localhost`, check that you passed `--set-file kcpKubeconfig.adminContent=$KCP` to the MSP-side install.

### `kubectl kcp workspace: command not found`

Install the [kubectl-kcp plugin](https://github.com/kcp-dev/kcp/releases) and put it on your `PATH`.

## Development Mode

For iterating on controller code, see [development.md](./development.md).
