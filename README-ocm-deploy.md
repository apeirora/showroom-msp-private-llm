# OCM Bootstrap with RGD (default)

This guide walks operators through packaging the private-llm controller into an [Open Component Model](https://github.com/open-component-model) (OCM) component, publishing it to GHCR, and bootstrapping a cluster using the Kubernetes “bootstrap” flow with a ResourceGraphDefinition (RGD).

At a high level you will:

1. Build and push the controller image.
2. Package and push the Helm chart.
3. Publish an OCM component that bundles the image, chart, and RGD staging logic.
4. Install OCM, kro, and Flux on the target cluster.
5. Apply the bootstrap bundle that wires Flux to install the chart.
6. Create an instance resource to roll out the operator.

The canonical OCM repository for this project is `oci://ghcr.io/<GH_OWNER>/ocm`, with the component path `llm.example.com/private-llm`. Replace `<GH_OWNER>` with your GitHub user or organization when following the steps.

For a Pure Helm workflow or API usage details, refer to `README.md` and `README-api.md`.

**CI releases**
- build and push controller image → `ghcr.io/<GH_OWNER>/private-llm-controller:<version>`
- package and push Helm chart → `oci://ghcr.io/<GH_OWNER>/charts`
- publish OCM component → `oci://ghcr.io/<GH_OWNER>/ocm`

Use the manual workflow later in this document for local testing or ad-hoc publishing.

## Prereqs
- kubectl, docker, ocm CLI
- helm 3.14+ (for `helm push` to OCI)
- kind (optional, for local test)
- Export: `GH_OWNER` (GitHub user/org) and `GITHUB_TOKEN` (GHCR token)

## Deploy using a published release

Use this flow when the CI release pipeline (see `.github/workflows/build-push.yml`) has already published the version you want to install.

### 0) Set release context
```bash
export GH_OWNER=platform-mesh
export VERSION=v0.0.0-test8
export GITHUB_TOKEN=<ghcr-token-with-package-read>
```

- The release workflow builds `ghcr.io/${GH_OWNER}/private-llm-controller:${VERSION}`, packages the chart at `oci://ghcr.io/${GH_OWNER}/charts/private-llm-operator:${VERSION}`, and publishes the OCM component to `oci://ghcr.io/${GH_OWNER}/ocm`.
- `VERSION` must match the chart/app version that was produced (the Git tag without the leading `v`).

### 1) Create a fresh cluster (optional)
```bash
kind delete cluster --name kro-ocm || true
kind create cluster --name kro-ocm
```

### 2) Install controllers (kro + OCM + Flux)
```bash
# OCM registry TLS secret
TMP=$(mktemp -d); cd "$TMP"
cat > cert.conf <<'EOF'
[req]
distinguished_name=req
x509_extensions = v3_req
prompt = no
[dn]
CN = registry.ocm-system.svc.cluster.local
[v3_req]
subjectAltName = @alt_names
basicConstraints=CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
[alt_names]
DNS.1 = registry.ocm-system.svc.cluster.local
DNS.2 = registry
EOF
openssl genrsa -out ca.key 2048 >/dev/null 2>&1
openssl req -x509 -new -nodes -key ca.key -sha256 -days 3650 -subj "/CN=OCM Registry CA" -out ca.crt >/dev/null 2>&1
openssl genrsa -out tls.key 2048 >/dev/null 2>&1
openssl req -new -key tls.key -subj "/CN=registry.ocm-system.svc.cluster.local" -out server.csr >/dev/null 2>&1
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out tls.crt -days 3650 -sha256 -extensions v3_req -extfile cert.conf >/dev/null 2>&1
kubectl create ns ocm-system || true
kubectl -n ocm-system create secret generic ocm-registry-tls-certs \
  --from-file=tls.crt=tls.crt --from-file=tls.key=tls.key --from-file=ca.crt=ca.crt
cd -

# OCM controller
kubectl apply -k https://github.com/open-component-model/open-component-model/kubernetes/controller/config/default?ref=main

# KRO
export KRO_VERSION=$(curl -sL \
    https://api.github.com/repos/kubernetes-sigs/kro/releases/latest | \
    jq -r '.tag_name | ltrimstr("v")'
  )

helm install kro oci://ghcr.io/kro-run/kro/kro \
  --namespace kro \
  --create-namespace \
  --version=${KRO_VERSION}

# Flux controllers
kubectl apply -f https://github.com/fluxcd/flux2/releases/latest/download/install.yaml
```

### 3) Create credentials
```bash
# OCM to read GHCR component
kubectl -n ocm-system create secret docker-registry ghcr-credentials \
  --docker-server=ghcr.io --docker-username="$GH_OWNER" --docker-password="$GITHUB_TOKEN" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n ocm-system create serviceaccount ocm-repo-access --dry-run=client -o yaml | kubectl apply -f -
kubectl -n ocm-system patch serviceaccount ocm-repo-access -p '{"imagePullSecrets":[{"name":"ghcr-credentials"}]}'

# Workload namespace to pull operator image
kubectl create ns private-llm-system || true
kubectl -n private-llm-system create secret docker-registry ghcr-credentials \
  --docker-server=ghcr.io --docker-username="$GH_OWNER" --docker-password="$GITHUB_TOKEN" --dry-run=client -o yaml | kubectl apply -f -
```

### 4) Bootstrap the RGD and deploy
```bash
# Apply bootstrap without editing tracked files (requires envsubst for GH_OWNER and VERSION)
envsubst < ocm/bootstrap.yaml | kubectl apply -f -

# Wait for the generated CRD (from RGD)
kubectl wait --for=condition=established --timeout=60s crd/resourcegraphdefinitions.kro.run
kubectl wait --for=condition=established --timeout=60s crd/privatellmoperatorreleases.kro.run

# Adjust ocm/instance.yaml if you need a different public host or namespace
envsubst < ocm/instance.yaml | kubectl apply -f -
```

### 5) Verify
```bash
kubectl -n ocm-system get repository,component,resource,deployers -o wide
kubectl -n private-llm-system rollout status deploy/private-llm-controller-manager --timeout=5m
kubectl -n private-llm-system get deploy,po
```

## Manual publishing workflow (local testing)

Use this when you need to build and publish artifacts yourself (for example, before cutting an official release). After completing the steps below, continue with the deployment flow above starting from **Install controllers (kro + OCM + Flux)**.

1) Build and push the controller image
```bash
export GH_OWNER=ifdotpy
export GITHUB_TOKEN=<ghcr-token>
export SHA=$(git rev-parse --short HEAD)
export VERSION=0.0.0-$SHA
export IMG=ghcr.io/$GH_OWNER/private-llm-controller:$VERSION

docker build -t "$IMG" .
echo "$GITHUB_TOKEN" | docker login ghcr.io -u "$GH_OWNER" --password-stdin
docker push "$IMG"
```

2) Package and push the Helm chart (OCI)
```bash
cd charts/private-llm-operator
helm package . --version "$VERSION" --app-version "$VERSION"
echo "$GITHUB_TOKEN" | helm registry login ghcr.io -u "$GH_OWNER" --password-stdin
helm push ./private-llm-operator-$VERSION.tgz oci://ghcr.io/$GH_OWNER/charts
cd -
```

3) Publish the OCM component (image + chart)
```bash
mkdir -p dist/ctf
export OCM_REPOSITORY=oci://ghcr.io/$GH_OWNER/ocm
ocm add componentversions --create --file dist/ctf .ocm/component-constructor.yaml \
  VERSION="$VERSION" GITHUB_REPOSITORY_OWNER="$GH_OWNER" IMAGE_TAG="$VERSION" CHART_TAG="$VERSION"
ocm transfer commontransportarchive dist/ctf "$OCM_REPOSITORY" --copy-resources --overwrite
```

Notes
- Helm OCI push requires helm 3.14+ (or enable OCI in older versions).
- The RGD generates a `PrivateLLMOperatorRelease` CRD. Inspect `kubectl get private...` to see release history.
- All artifacts remain housed under `ghcr.io/<GH_OWNER>`; they can be moved to another registry by adjusting the env vars before running the publish steps.
