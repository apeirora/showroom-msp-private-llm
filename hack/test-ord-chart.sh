#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

assert_contains() {
  local file="$1"
  local needle="$2"
  if ! grep -Fq -- "$needle" "$file"; then
    echo "expected to find '$needle' in $file" >&2
    return 1
  fi
}

operator_render="$tmpdir/operator.yaml"
helm template private-llm "$repo_root/charts/private-llm-operator" \
  --namespace private-llm-operator \
  --set portalIntegration.enabled=true \
  --set global.publicHost=llm.example.com \
  --set global.publicScheme=https \
  > "$operator_render"
assert_contains "$operator_render" 'path: "/.well-known/open-resource-discovery"'
assert_contains "$operator_render" 'path: "/ord/"'
assert_contains "$operator_render" 'mountPath: /usr/share/nginx/html/ord/documents/private-llm.json'
assert_contains "$operator_render" 'add_header Access-Control-Allow-Origin "*" always;'

metadata_render="$tmpdir/provider-metadata.yaml"
helm template private-llm-pm "$repo_root/charts/private-llm-pm-integration" \
  --set publicHost=llm.example.com \
  --set publicScheme=https \
  > "$metadata_render"
assert_contains "$metadata_render" 'displayName: ORD'
assert_contains "$metadata_render" 'configUrl: "https://llm.example.com/.well-known/open-resource-discovery"'
