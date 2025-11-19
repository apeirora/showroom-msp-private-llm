#!/bin/bash

set -euo pipefail

# Configuration
VERSION="${VERSION:-2.4.0}"
TOKEN_FILE="./token"
HELM_CHARTS="private-llm-operator private-llm-sync-agent private-llm-pm-integration"

# Read token
if [ ! -f "$TOKEN_FILE" ]; then
    echo "Error: Token file '$TOKEN_FILE' not found"
    exit 1
fi
GITHUB_TOKEN=$(cat "$TOKEN_FILE")

# Derive metadata like the workflow
OWNER="apeirora"  # Adjust this to your actual GitHub username/organization
IMAGE="ghcr.io/${OWNER}/private-llm-controller"
CHART_REGISTRY="oci://ghcr.io/${OWNER}/charts"
OCM_REPOSITORY="oci://ghcr.io/${OWNER}/ocm"

echo "Building version: $VERSION"
echo "Image: $IMAGE:$VERSION"
echo "Chart Registry: $CHART_REGISTRY"
echo "OCM Repository: $OCM_REPOSITORY"

# Set up Go
echo "Setting up Go..."
export PATH=$PATH:/usr/local/go/bin
go version

# Build Docker image for x86_64
echo "Building Docker image for x86_64..."
IMG="${IMAGE}:${VERSION}"
DOCKER_BUILD_ARGS="--platform linux/amd64"
make docker-build IMG="$IMG" VERSION="$VERSION" DOCKER_BUILD_ARGS="$DOCKER_BUILD_ARGS"

# Log in to GitHub Container Registry
echo "Logging in to GitHub Container Registry..."
echo "$GITHUB_TOKEN" | docker login ghcr.io --username "$OWNER" --password-stdin

# Push Docker image (immutable tag)
echo "Pushing Docker image (version tag)..."
if make docker-push IMG="$IMG"; then
    echo "Successfully pushed version tag"

    # Push Docker image (latest)
    echo "Pushing Docker image (latest tag)..."
    docker tag "$IMG" "${IMAGE}:latest"
    docker push "${IMAGE}:latest"
    echo "Successfully pushed latest tag"
    DOCKER_PUSH_SUCCESS=true
else
    echo "Failed to push Docker image. Token may not have correct permissions."
    echo "Please ensure the token has 'write:packages' scope for GHCR."
    echo "Continuing with Helm charts and OCM build..."
    DOCKER_PUSH_SUCCESS=false
fi

# Capture image digest
echo "Capturing image digest..."
DIGEST=$(docker inspect --format='{{index .RepoDigests 0}}' "$IMG" || true)
if [ -z "$DIGEST" ]; then
    echo "Repo digest not available; attempting registry lookup skipped"
    DIGEST="unknown"
fi
echo "$DIGEST" > image-digest.txt
echo "Image digest: $DIGEST"

# Package Helm charts
echo "Checking Helm installation..."
if ! command -v helm &> /dev/null; then
    echo "Helm not found. Please install Helm first."
    exit 1
fi
helm version

echo "Logging in to Helm registry..."
echo "$GITHUB_TOKEN" | helm registry login ghcr.io --username "$OWNER" --password-stdin

# Create dist directory
mkdir -p dist

# Package and push charts
HELM_PUSH_SUCCESS=true
for chart in $HELM_CHARTS; do
    echo "::group::Packaging and pushing $chart"
    chart_dir="charts/${chart}"
    out_dir="dist/${chart}"
    pkg="${chart}-${VERSION}.tgz"

    helm dependency update "$chart_dir"
    helm lint "$chart_dir"
    helm template "release-${chart}" "$chart_dir" --namespace default >/dev/null

    mkdir -p "$out_dir"
    helm package "$chart_dir" --destination "$out_dir" --version "$VERSION" --app-version "$VERSION"

    if helm push "$out_dir/$pkg" "$CHART_REGISTRY"; then
        echo "Successfully pushed $chart"
    else
        echo "Failed to push $chart (token permission issue)"
        HELM_PUSH_SUCCESS=false
    fi

    sha256sum "$out_dir/$pkg" > "$out_dir/$pkg.sha256"
    echo "::endgroup::"
done

# Generate aggregated checksums
echo "Generating aggregated checksums..."
if ls dist/charts/*.sha256 1> /dev/null 2>&1; then
    cat dist/charts/*.sha256 > SHA256SUMS.txt
else
    echo "No checksum files found" > SHA256SUMS.txt
fi

# Write build metadata
echo "Writing build metadata..."
charts_json="["
sep=""
for chart in $HELM_CHARTS; do
    charts_json="${charts_json}${sep}{\"name\":\"${chart}\",\"version\":\"${VERSION}\",\"registry\":\"${CHART_REGISTRY}\"}"
    sep=","
done
charts_json="${charts_json}]"

cat > build-metadata.json <<EOF
{
  "commit": {
    "sha": "$(git rev-parse HEAD)"
  },
  "image": {
    "name": "${IMAGE}",
    "tag": "${VERSION}",
    "latest": true
  },
  "charts": ${charts_json}
}
EOF

# Install OCM CLI
echo "Installing OCM CLI..."
if command -v ocm &> /dev/null; then
    echo "OCM CLI already installed"
elif curl -sSfL https://ocm.software/install.sh | bash; then
    echo "OCM CLI installed successfully"
else
    echo "Failed to install OCM CLI with system installer, trying local installation..."
    # Try installing to local bin directory
    mkdir -p ~/bin
    export PATH="$HOME/bin:$PATH"
    curl -L https://github.com/open-component-model/ocm/releases/latest/download/ocm-linux-amd64 -o ~/bin/ocm
    chmod +x ~/bin/ocm
    if command -v ocm &> /dev/null; then
        echo "OCM CLI installed locally"
    else
        echo "Failed to install OCM CLI"
        OCM_PUSH_SUCCESS=false
    fi
fi

# Build and Push OCM component
echo "Building and pushing OCM component..."
mkdir -p dist/ctf
if ocm add componentversions --create --file dist/ctf .ocm/component-constructor.yaml \
    VERSION="${VERSION}" GITHUB_REPOSITORY_OWNER="${OWNER}" IMAGE_TAG="${VERSION}" CHART_TAG="${VERSION}"; then
    if ocm transfer commontransportarchive dist/ctf "$OCM_REPOSITORY" --copy-resources --overwrite; then
        OCM_PUSH_SUCCESS=true
    else
        OCM_PUSH_SUCCESS=false
        echo "Failed to push OCM component (token permission issue)"
    fi
else
    OCM_PUSH_SUCCESS=false
    echo "Failed to build OCM component"
fi

echo "Build completed!"
echo ""

if [ "$DOCKER_PUSH_SUCCESS" = true ]; then
    echo "✅ Docker images pushed successfully:"
    echo "  - $IMAGE:$VERSION"
    echo "  - $IMAGE:latest"
else
    echo "❌ Docker images NOT pushed (token permission issue):"
    echo "  - $IMAGE:$VERSION (built locally)"
    echo "  - $IMAGE:latest (built locally)"
fi

echo ""
echo "Helm charts:"
if [ "$HELM_PUSH_SUCCESS" = true ]; then
    echo "✅ Helm charts packaged and pushed:"
    ls -1 dist/charts/*.tgz 2>/dev/null | sed 's/^/  - /'
else
    echo "❌ Helm charts packaged but NOT pushed (token permission issue):"
    ls -1 dist/charts/*.tgz 2>/dev/null | sed 's/^/  - /' || echo "  - none"
fi

echo ""
echo "OCM component:"
if [ "$OCM_PUSH_SUCCESS" = true ]; then
    echo "✅ OCM component built and pushed to $OCM_REPOSITORY"
else
    echo "❌ OCM component NOT pushed (token permission issue)"
fi

echo ""
echo "✅ Local artifacts created:"
echo "- Build metadata: build-metadata.json"
echo "- Checksums: SHA256SUMS.txt"
echo "- Image digest: image-digest.txt"

if [ "$DOCKER_PUSH_SUCCESS" = false ] || [ "$HELM_PUSH_SUCCESS" = false ] || [ "$OCM_PUSH_SUCCESS" = false ]; then
    echo ""
    echo "⚠️  To fix push issues:"
    echo "   1. Go to https://github.com/settings/tokens"
    echo "   2. Edit your token or create a new one"
    echo "   3. Ensure it has 'write:packages' scope"
    echo "   4. Update the token file and re-run the script"
    echo ""
    echo "   Or use a GitHub App token with appropriate permissions"
fi
