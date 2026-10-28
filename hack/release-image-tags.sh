#!/usr/bin/env bash
# Derive versioned image tags without promoting a prerelease to :latest.
set -euo pipefail

version=${GITHUB_REF_NAME#v}
owner=${GITHUB_REPOSITORY_OWNER,,}
number='(0|[1-9][0-9]*)'
if [[ ! $version =~ ^${number}\.${number}\.${number}(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]]; then
  echo 'Release tag must be a SemVer version without build metadata' >&2
  exit 1
fi
if [[ ! $owner =~ ^[a-z0-9-]+$ ]]; then
  echo 'Invalid repository owner' >&2
  exit 1
fi
publish_latest=true
if [[ $version == *-* ]]; then
  publish_latest=false
  IFS='.' read -ra identifiers <<< "${version#*-}"
  for identifier in "${identifiers[@]}"; do
    if [[ $identifier =~ ^0[0-9]+$ ]]; then
      echo 'Numeric prerelease identifiers must not have leading zeros' >&2
      exit 1
    fi
  done
fi

image="ghcr.io/${owner}/private-llm-controller"
{
  printf 'version=%s\n' "$version"
  printf 'publish_latest=%s\n' "$publish_latest"
  printf 'image_tags<<RELEASE_IMAGE_TAGS\n%s:%s\n' "$image" "$version"
  if [[ $publish_latest == true ]]; then
    printf '%s:latest\n' "$image"
  fi
  printf 'RELEASE_IMAGE_TAGS\n'
} >> "$GITHUB_OUTPUT"
