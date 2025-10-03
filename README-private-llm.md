# Private LLM

An example Kubernetes operator (Operator SDK, Go) that provisions a llama.cpp server per `LLMInstance` CR. For each CR it now:

- creates a Deployment using `ghcr.io/ggml-org/llama.cpp:server`
- uses an `initContainer` to download the TinyLlama model into an `emptyDir` mounted at `/models`
- exposes the Pod via a ClusterIP Service on port 8000
- creates an Ingress with host set from `PUBLIC_HOST` and a unique path prefix `/llm/<slug>/<instance-name>`
- updates the CR status to Ready with the public URL `http://$PUBLIC_HOST/<instance-name>`

## Description
// TODO(user): An in-depth paragraph about your project and overview of use

## Getting Started

### Prerequisites
- go version v1.21.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
#### Using Helm (Traefik enabled by default)
Install the operator with Helm. Traefik is bundled and enabled by default. You can disable it with `--set traefik.enabled=false` if you manage Traefik separately.

Local chart path:

```sh
helm upgrade --install private-llm charts/private-llm-operator \
  --namespace private-llm-system --create-namespace \
  --dependency-update \
  --set PUBLIC_HOST=localhost
```

For kind/local clusters, you may prefer NodePort:

```sh
helm upgrade --install private-llm charts/private-llm-operator \
  --namespace private-llm-system --create-namespace \
  --dependency-update \
  --set PUBLIC_HOST=localhost \
  --set traefik.service.type=NodePort \
  --set traefik.ports.web.nodePort=30080 \
  --set traefik.ports.websecure.nodePort=30443
```

If you install the operator from an OCI registry, first ensure the chart was packaged with dependencies (see below), or install Traefik separately.
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/private-llm:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

#### Build for x86 (linux/amd64)

- Using the Makefile (buildx, pushes when used with your registry):

```sh
make docker-buildx PLATFORMS=linux/amd64,linux/arm64 IMG=<some-registry>/private-llm:tag
```

- Using regular docker-build (local build, then push):

```sh
DOCKER_DEFAULT_PLATFORM=linux/amd64 make docker-build IMG=<some-registry>/private-llm:tag
make docker-push IMG=<some-registry>/private-llm:tag
```

- Optional: raw docker buildx (loads into local docker):

```sh
docker buildx build --platform linux/amd64 --load -t <some-registry>/private-llm:tag .
```

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/private-llm:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create an instance**
Apply the provided sample:

```sh
kubectl apply -k config/samples/
```

This will create:

- a `LLMInstance` named `llminstance-sample`
- a llama.cpp Deployment and Service
- an Ingress at `http://$PUBLIC_HOST/llm/<slug>/<name>` (e.g., `http://localhost/abc123xyz/llminstance-sample`)

Inspect status:

```sh
kubectl get llminstances.llm.example.com -o wide
kubectl get llminstance llminstance-sample -o yaml | sed -n '1,150p'
```

Scaling is supported via `spec.replicas`. The `spec.model` field is currently ignored; the operator downloads TinyLlama by default via the init container.

### LLMInstance CR

- spec:
  - model: optional string (currently ignored; reserved for future model selection)
  - replicas: optional int32, defaults to 1
- status:
  - phase: string (e.g., "Ready")
  - endpoint: public URL exposed via Ingress
  - observedGeneration: int64
  - conditions: standard Kubernetes conditions

Example:

```yaml
apiVersion: llm.example.com/v1alpha1
kind: LLMInstance
metadata:
  name: llminstance-sample
  namespace: default
spec:
  replicas: 1
  model: tinyllama
```

The Deployment uses:

- image: `ghcr.io/ggml-org/llama.cpp:server`
- command: `/app/llama-server -m /models/tinyllama.gguf --port 8000 --host 0.0.0.0`
- volume: `emptyDir` mounted at `/models`
- initContainer: `curlimages/curl` to download TinyLlama into `/models`

Ingress:

- class: `traefik`
- host: `$PUBLIC_HOST`
- path: `/llm/<slug>/<instance-name>`

Configuring the public host:

- Set the env var when installing (via install.yaml), under the manager container:

```yaml
env:
- name: PUBLIC_HOST
  value: localhost
```

- Or patch after install:

```sh
kubectl -n private-llm-system set env deploy/private-llm-controller-manager PUBLIC_HOST=localhost
```

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following are the steps to build the installer and distribute this project to users.

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/private-llm:tag
```

NOTE: The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without
its dependencies.

2. Using the installer

Users can just run kubectl apply -f <URL for YAML BUNDLE> to install the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/private-llm/<tag or branch>/dist/install.yaml
```

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

## OCM publishing (GHCR)

This publishes an OCM component that contains your Helm chart and image, to GHCR.

Prereqs:
- Docker, Helm, OCM CLI installed
- GitHub username in `GH_OWNER`, token in `GITHUB_TOKEN`

1) Build and push operator image to GHCR
```sh
export GH_OWNER=<github-username>
export SHA=$(git rev-parse --short HEAD)
export IMG=ghcr.io/$GH_OWNER/private-llm:$SHA

docker build -t "$IMG" -t ghcr.io/$GH_OWNER/private-llm:latest .
docker push "$IMG"
docker push ghcr.io/$GH_OWNER/private-llm:latest
```

2) Package and push Helm chart (OCI) to GHCR
```sh
cd charts/private-llm-operator
helm dependency update
export CHART_VER=0.0.0-$SHA
helm package . --version "$CHART_VER" --app-version "0.4.0"
echo "$GITHUB_TOKEN" | helm registry login ghcr.io -u "$GH_OWNER" --password-stdin
helm push ./private-llm-operator-$CHART_VER.tgz oci://ghcr.io/$GH_OWNER/private-llm
cd -
```

3) Create and push OCM component version
```sh
export VERSION=$CHART_VER
export IMAGE_TAG=$SHA
export CHART_TAG=$CHART_VER
export OCM_REPOSITORY=oci://ghcr.io/$GH_OWNER/ocm

echo "$GITHUB_TOKEN" | ocm login ghcr.io -u "$GH_OWNER" -p-
ocm add componentversions --create --file dist/ctf .ocm/component-constructor.yaml \
  VERSION="$VERSION" GITHUB_REPOSITORY_OWNER="$GH_OWNER" IMAGE_TAG="$IMAGE_TAG" CHART_TAG="$CHART_TAG"
ocm transfer commontransportarchive dist/ctf "$OCM_REPOSITORY" --overwrite
```

4) Verify
```sh
ocm get components ghcr.io/$GH_OWNER/ocm//llm.example.com/private-llm:$VERSION
```

## Deploy with OCM Bootstrap (RGD)

Follow `README-ocm-bootstrap.md` for the default RGD‑based bootstrap:
- build/push image and chart to GHCR
- publish the OCM component (image + chart + RGD)
- install kro + OCM controller + Flux
- apply ComponentVersion and bootstrap the RGD via Deployer
- create the instance CR from the RGD to deploy the operator

See: README-ocm-bootstrap.md
