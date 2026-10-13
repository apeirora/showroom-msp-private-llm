#!/usr/bin/env bash
set -euo pipefail
chart_dir="${1:?Chart directory required}"
result_dir="$(mktemp -d)"
trap 'rm -rf "$result_dir"' EXIT
helm template shared-crd-test "$chart_dir" --include-crds \
  --set syncAgentOperator.crds.enabled=false > "$result_dir/render.yaml"
if grep -Eq 'name:.*publishedresources\.syncagent\.kcp\.io' "$result_dir/render.yaml"; then
  echo 'The wrapper must not install the shared PublishedResource CRD' >&2
  exit 1
fi
grep -q '^kind: PublishedResource$' "$result_dir/render.yaml"
