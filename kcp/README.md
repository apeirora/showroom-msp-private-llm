## Prerequisites
- KCP installed (v1.31.6+kcp-v0.27.0)
- Krew packages for KCP installed
- api-syncagent installed (v0.2.0)

## Steps

### 0. Create a kind workload cluster (and export kubeconfig)
Create the cluster, then export the kubeconfig used by later steps. Traefik is enabled by default via the bundled subchart; you can still install it separately if preferred.
```bash
cd kcp
kind create cluster --config kind/cluster.yaml

# Option A: Use bundled Traefik (default) during operator install
#   (no action here; the main Helm install will include Traefik)

# Option B: Install Traefik separately into the cluster
helm repo add traefik https://traefik.github.io/charts
helm repo update
helm upgrade --install traefik traefik/traefik \
  --namespace traefik --create-namespace \
  -f kind/traefik-values.yaml

# If you disable the bundled Traefik: --set traefik.enabled=false

# export kubeconfig for the workload cluster to project root
kind get kubeconfig --name llm-workload > kubeconfig.yaml
kubectl --kubeconfig kubeconfig.yaml get nodes
```


### 1. Install the LLM CRD and operator. Prepare a kubeconfig and save it as *kubeconfig.yaml*. Install the CRD for the syncagent. Apply the PublishedResource for the LLM CRD.

Got to ../README-ocm-bootstrap.md, follow the guide

```bash
kubectl apply -f syncagent/syncagent.kcp.io_publishedresources.yaml
kubectl apply -f syncagent/resources-llminstance.yaml
```

### 2. Start KCP in a separate terminal.
```bash 
cd kcp
~/.local/bin/kcp start
```

### 3. Create the provider workspace in a separate terminal. Apply the APIExport CR.
```bash
cd kcp
export KUBECONFIG=.kcp/admin.kubeconfig
kubectl ws create provider --enter
cp .kcp/admin.kubeconfig .kcp/admin-provider.kubeconfig

kubectl apply -f marketplace/llminstance-export.yaml
```

### 4. Create a customer workspace. Grant access.
```bash
kubectl ws use :
kubectl ws create customer-1 --enter

# Renaming terminal to Customer's

kubectl kcp bind apiexport root:provider:llm.example.com --accept-permission-claim secrets.core,namespaces.core
```

### 5. Run the syncagent in a separate terminal.
```bash
cd kcp
~/.local/bin/api-syncagent \
  --kubeconfig kubeconfig.yaml \
  --kcp-kubeconfig=.kcp/admin-provider.kubeconfig \
  --namespace default \
  --apiexport-ref llm.example.com
```

### 6. In the terminal with `customer-1`, apply the CR for the LLM.
```bash
export KUBECONFIG=.kcp/admin.kubeconfig
kubectl apply -f llm_v1alpha1_llminstance.yaml
```
