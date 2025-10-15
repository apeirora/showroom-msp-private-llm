## Release Flow

1. Feature branch → open PR to `main`.
   - CI runs: Helm lint, golangci-lint, unit tests, compact coverage summary (HTML + artifacts).
   - Commit messages and PR title must be Conventional Commits.
2. Merge PR to `main`.
   - release-please scans commits on `main`, determines the next SemVer (feat/fix/perf and BREAKING CHANGE), then creates tag `vX.Y.Z` and a GitHub Release with generated notes (no Release PR).
   - CHANGELOG and release manifest are updated by release-please.
   - Tag workflow builds and publishes:
     - Docker image: `ghcr.io/<owner>/private-llm-controller:vX.Y.Z` and `:latest`.
     - Helm chart to `oci://ghcr.io/<owner>/charts`.
     - Build metadata and checksums as release assets.

### Main channel

- Every merge to `main` is immediately released by release-please (SemVer derived from Conventional Commits). Pre-release main-only artifacts are not produced; artifacts are built from tags.

### Versioning

- SemVer based on Conventional Commits:
  - `feat`: minor (or major if `!`/BREAKING CHANGE)
  - `fix`/`perf`: patch
  - `!` or `BREAKING CHANGE:` footer: major

### Where to find artifacts

- Docker: GHCR package page for the repo owner
- Helm: `helm pull oci://ghcr.io/<owner>/charts/private-llm-operator --version X.Y.Z`
- OCM: `oci://ghcr.io/<owner>/ocm`


