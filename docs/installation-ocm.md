# OCM Installation

Before installing or upgrading, prepare the required [operator-owned slug
policy](slug-policy.md), including the fixed cutover and every legacy MSP identity.
Use `private-llm-operator.slugPolicy` for the MSP umbrella, `slugPolicy` for the
standalone operator, or `operator.slugPolicy` for the OCM installer. Pass that
reviewed values file to the commands below. The empty default is intentionally
not deployable. Stop competing old managers for the Recreate migration.

The development pilot uses direct Helm; it does not validate the OCM route or
component publication. The RC example below is an explicit development opt-in
only after independently verifying that exact component and its referenced
artifacts. Do not use a broad version constraint or deploy the RC to production.
Use the local installer chart and bootstrap from a clean checkout of the reviewed
`v2.17.1-rc.3` tag; selecting a component version does not update those local files.

Deploy the Private LLM Operator using the [Open Component Model](https://ocm.software/) (OCM) supply chain. The OCM controller resolves the component version from an OCI registry, extracts the Helm chart via a `Resource`, and hands it to a `FluxDeployer` that creates a Flux `HelmRelease`.

---

## Prerequisites

- Kubernetes cluster with:
  - [OCM Controller](https://github.com/open-component-model/open-component-model) installed
  - [Flux](https://fluxcd.io/) controllers (source-controller, helm-controller)
- `kubectl`, `helm` 3.14+
- `GITHUB_TOKEN` with read access to GHCR packages

> **Note**: KRO is **not** required. The chart uses the OCM controller's built-in `FluxDeployer` to create the `HelmRelease` directly.

## Architecture

```mermaid
graph LR
    CV[ComponentVersion] --> R[Resource<br>Helm chart ref]
    R --> S[Snapshot]
    S --> FD[FluxDeployer]
    FD --> HR[Flux HelmRelease]
    HR --> Op[Private LLM Operator<br>in target namespace]
```

The Helm chart creates three OCM custom resources:

| Resource | Kind | Purpose |
|----------|------|---------|
| `private-llm-operator` | `ComponentVersion` | Points to the OCM component in the OCI registry |
| `private-llm-operator-chart` | `Resource` | Extracts the Helm chart artifact from the component |
| `private-llm-operator` | `FluxDeployer` | Creates a Flux `HelmRelease` from the extracted chart |

All three resources are created in the `ocm-system` namespace.

## Step 1: Create GHCR Credentials

The OCM controller needs pull access to `ghcr.io/apeirora`:

```sh
kubectl -n ocm-system create secret docker-registry ghcr-credentials \
  --docker-server=ghcr.io \
  --docker-username="apeirora" \
  --docker-password="$GITHUB_TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f -
```

If the operator image is also private, create the same secret in the target namespace:

```sh
kubectl create namespace private-llm-system || true
kubectl -n private-llm-system create secret docker-registry ghcr-credentials \
  --docker-server=ghcr.io \
  --docker-username="apeirora" \
  --docker-password="$GITHUB_TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f -
```

## Step 2: Configure Values

Create a `values.yaml` override file. Separately prepare
`reviewed-ocm-values.yaml` with the reviewed `operator.slugPolicy` cutoff and
identity map; the example does not invent either value:

```yaml
component:
  semver: "2.17.1-rc.3" # Development only, after separate OCM artifact verification.

operator:
  targetNamespace: private-llm-system
  publicHost: llm.example.com
  publicScheme: https
  imagePullSecretName: ghcr-credentials
  traefikEnabled: true             # false if Traefik is managed externally
  tlsSecretName: "private-llm"
  ingressExtraAnnotations: {}
```

### All Values

| Parameter | Description | Default |
|-----------|-------------|---------|
| `component.name` | Name for the OCM resources | `private-llm-operator` |
| `component.namespace` | Namespace for OCM resources | `ocm-system` |
| `component.componentName` | OCM component identity | `llm.privatellms.msp/private-llm` |
| `component.repositoryUrl` | OCI registry URL | `ghcr.io/apeirora/ocm` |
| `component.secretRefName` | Secret with registry credentials | `ghcr-credentials` |
| `component.semver` | Override with an exact reviewed component version | Historical `>=2.0.1`; must be overridden |
| `component.interval` | Reconciliation interval | `1m` |
| `component.chartResourceName` | Name of the chart resource in the component | `oci-helm-chart-private-llm-operator` |
| `operator.targetNamespace` | Namespace where the operator is deployed | `private-llm-system` |
| `operator.publicHost` | Public hostname for the LLM API | `localhost` |
| `operator.publicScheme` | `http` or `https` | `http` |
| `operator.imagePullSecretName` | Image pull secret name | `ghcr-credentials` |
| `operator.traefikEnabled` | Deploy bundled Traefik | `true` |
| `operator.tlsSecretName` | TLS secret for ingress | `""` |
| `operator.ingressExtraAnnotations` | Extra ingress annotations | `{}` |
| `operator.slugPolicy` | Required fixed cutoff and verified MSP identity map | Empty cutoff; prevents startup |

## Step 3: Install via Helm

```sh
helm template private-llm charts/private-llm-operator-ocm/ \
  -f charts/private-llm-operator-ocm/values.yaml \
  -f ./values.yaml \
  -f reviewed-ocm-values.yaml \
  | kubectl apply -f -
```

This creates three resources in `ocm-system`:

1. **ComponentVersion** `private-llm-operator` -- resolves the component from `ghcr.io/apeirora/ocm`
2. **Resource** `private-llm-operator-chart` -- extracts the `oci-helm-chart-private-llm-operator` artifact
3. **FluxDeployer** `private-llm-operator` -- creates a `HelmRelease` targeting `private-llm-system`

The OCM controller reconciles the chain: `ComponentVersion` -> `Resource` -> `Snapshot` -> `FluxDeployer` -> `HelmRelease` -> operator pods.

## Step 4: Verify

```sh
# 1. ComponentVersion should be Ready
kubectl get componentversion -n ocm-system

# 2. Resource should be Ready with a Snapshot
kubectl get resource -n ocm-system
kubectl get snapshot -n ocm-system

# 3. FluxDeployer should have created a HelmRelease
kubectl get fluxdeployer -n ocm-system
kubectl get helmrelease -n ocm-system

# 4. Operator pods should be running
kubectl get pods -n private-llm-system
```

> **Tip**: If the `ComponentVersion` stays in a non-ready state, check that the `ghcr-credentials` secret exists in `ocm-system` and has valid credentials.

## Upgrading

Update `component.semver` to the exact reviewed release after its artifact and
migration checks, preserving the reviewed policy, then re-apply:

```sh
# Edit values.yaml: component.semver must be an exact reviewed version.
helm template private-llm charts/private-llm-operator-ocm/ \
  -f charts/private-llm-operator-ocm/values.yaml \
  -f ./values.yaml \
  -f reviewed-ocm-values.yaml \
  | kubectl apply -f -
```

The OCM controller detects the version change, resolves the new chart, and the `FluxDeployer` rolls out the upgrade via the `HelmRelease`.

## Manual Deployment via bootstrap.yaml

Render `ocm/bootstrap.yaml` locally first; do not pipe its incomplete defaults
directly into the cluster:

```sh
export GH_OWNER=apeirora
export VERSION=2.17.1-rc.3 # Development only, after separate OCM verification.

envsubst '${GH_OWNER} ${VERSION}' < ocm/bootstrap.yaml > bootstrap-rendered.yaml
```

Before applying, replace the FluxDeployer's
`.spec.helmReleaseTemplate.values.slugPolicy` with the reviewed cutoff and map,
and review its other operator settings, including HTTPS/TLS. The generated
empty cutoff cannot start the manager. Only after that review and the migration
prerequisites are complete:

```sh
kubectl apply -f bootstrap-rendered.yaml
```

## Publishing a Custom OCM Component

If you need to publish your own builds:

### 1. Build and push the operator image

```sh
export GH_OWNER=<your-github-username>
export SHA=$(git rev-parse --short HEAD)
export IMG=ghcr.io/$GH_OWNER/private-llm-controller:$SHA

docker build -t "$IMG" .
docker push "$IMG"
```

### 2. Package and push the Helm chart

```sh
cd charts/private-llm-operator
helm dependency update
export CHART_VER=0.0.0-$SHA
helm package . --version "$CHART_VER" --app-version "$CHART_VER"
echo "$GITHUB_TOKEN" | helm registry login ghcr.io -u "$GH_OWNER" --password-stdin
helm push ./private-llm-operator-$CHART_VER.tgz oci://ghcr.io/$GH_OWNER/charts
cd -
```

### 3. Create the OCM component version

```sh
export VERSION=$CHART_VER
export IMAGE_TAG=$SHA
export CHART_TAG=$CHART_VER
export OCM_REPOSITORY=oci://ghcr.io/$GH_OWNER/ocm

echo "$GITHUB_TOKEN" | ocm login ghcr.io -u "$GH_OWNER" -p-
ocm add componentversions --create --file dist/ctf .ocm/component-constructor.yaml \
  VERSION="$VERSION" GITHUB_REPOSITORY_OWNER="$GH_OWNER" IMAGE_TAG="$IMAGE_TAG" CHART_TAG="$CHART_TAG"
ocm transfer commontransportarchive dist/ctf "$OCM_REPOSITORY" --copy-resources --overwrite
```

### 4. Verify

```sh
ocm get components ghcr.io/$GH_OWNER/ocm//llm.privatellms.msp/private-llm:$VERSION
```

## Deploying Without OCM

If you prefer to skip OCM entirely and install the chart directly:

```sh
VERSION=2.17.1-rc.3 # Explicit development opt-in after artifact verification.
helm upgrade --install private-llm-operator \
  oci://ghcr.io/apeirora/charts/private-llm-operator \
  --version "$VERSION" \
  -f reviewed-operator-values.yaml \
  --namespace private-llm-system --create-namespace \
  --set PUBLIC_HOST=llm.example.com \
  --set PUBLIC_SCHEME=https \
  --set tls.secretName=private-llm \
  --set image.repository=ghcr.io/apeirora/private-llm-controller \
  --set image.tag="$VERSION" \
  --set 'imagePullSecrets[0].name=ghcr-credentials' \
  --set traefik.enabled=false
```

See [Helm Installation](installation-helm.md) for full details.

## Next Steps

- [Create your first LLMInstance](resources.md)
- [Set up Platform Mesh integration](user-guide.md)
