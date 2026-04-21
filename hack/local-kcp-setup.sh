#!/usr/bin/env bash
#
# Prepare a freshly-provisioned local Platform Mesh (`task local-setup`)
# for provider installs.
#
# Runs three idempotent fixes that `task local-setup` itself does not do:
#
# 1. Patch CoreDNS so in-cluster pods resolve `root.kcp.localhost` and the
#    other localhost-based KCP URLs to the Traefik ClusterIP (the only
#    in-cluster service listening on 8443). Without this, sync-agents and
#    the portal fail to reach KCP from inside the cluster.
#
# 2. Disable the security workspace initializer. The security operator
#    cannot manage Keycloak realms locally (403 Forbidden), which leaves
#    new workspaces stuck in "Initializing".
#
# 3. Ensure the `root:providers` KCP workspace exists so this and every
#    other provider tutorial can go straight to creating its own
#    workspace under it.
#
# Usage:
#   export KCP=/abs/path/to/helm-charts/.secret/kcp/admin.kubeconfig
#   export KCP_URL=https://localhost:8443   # optional, this is the default
#   ./hack/local-kcp-setup.sh

set -euo pipefail

KCP="${KCP:-${KCP_KUBECONFIG:-}}"
KCP_URL="${KCP_URL:-https://localhost:8443}"

if [ -z "$KCP" ] || [ ! -r "$KCP" ]; then
  echo "error: KCP must be set to the path of the helm-charts .secret/kcp/admin.kubeconfig file" >&2
  echo "  example: export KCP=\$(pwd)/../helm-charts/.secret/kcp/admin.kubeconfig" >&2
  exit 1
fi

COL='\033[0;36m'
COL_RES='\033[0m'

echo -e "${COL}[1/3] Patching CoreDNS to resolve kcp.localhost hostnames to Traefik${COL_RES}"
TRAEFIK_IP=$(kubectl get svc -n default traefik -o jsonpath='{.spec.clusterIP}')
kubectl get configmap coredns -n kube-system -o json | \
  python3 -c "
import sys, json
cm = json.load(sys.stdin)
ip = '$TRAEFIK_IP'
hosts_block = f'''hosts {{
           {ip} localhost portal.localhost kcp.localhost root.kcp.localhost
           fallthrough
        }}
        '''
corefile = cm['data']['Corefile']
if 'root.kcp.localhost' not in corefile:
    cm['data']['Corefile'] = corefile.replace(
        'kubernetes cluster.local', hosts_block + 'kubernetes cluster.local')
json.dump(cm, sys.stdout)
" | kubectl apply -f - >/dev/null

kubectl rollout restart deploy coredns -n kube-system >/dev/null
kubectl rollout status deploy coredns -n kube-system --timeout=60s >/dev/null

echo -e "${COL}[2/3] Disabling security workspace initializer (Keycloak realm creation not supported locally)${COL_RES}"
kubectl scale deploy -n platform-mesh-system security-operator-initializer --replicas=0 >/dev/null
kubectl --kubeconfig="$KCP" patch workspacetype security \
  --server="$KCP_URL/clusters/root" \
  --type=merge -p '{"spec":{"initializer":false}}' >/dev/null

echo -e "${COL}[3/3] Ensuring root:providers workspace exists${COL_RES}"
kubectl kcp workspace create providers \
  --type=root:providers --ignore-existing \
  --kubeconfig="$KCP" \
  --server="$KCP_URL/clusters/root" >/dev/null

echo -e "${COL}done — local KCP ready for provider installs${COL_RES}"
