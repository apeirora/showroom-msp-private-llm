## Release Flow

1. Feature branch → open PR to `main`.
   - CI runs: Helm lint, golangci-lint, unit tests, compact coverage summary (HTML + artifacts).
   - Commit messages and PR title must be Conventional Commits.
2. Merge PR to `main`.
   - release-please (PR mode) evaluates commits on `main` and opens a single Release PR proposing the next version (`vX.Y.Z`), CHANGELOG, and manifest updates.
   - Review and merge the Release PR to publish the GitHub Release and tag.
   - Tag workflow builds and publishes:
     - Docker image: `ghcr.io/<owner>/private-llm-controller:vX.Y.Z` and `:latest`.
     - Helm chart to `oci://ghcr.io/<owner>/charts`.
     - Build metadata and checksums as release assets.

### Main channel

- Release-please opens a Release PR after merges to `main` when there are releasable commits (e.g., `feat`, `fix`, `perf`, or BREAKING CHANGE). Artifacts are built from tags after the Release PR is merged.

### Versioning

- SemVer based on Conventional Commits:
  - `feat`: minor (or major if `!`/BREAKING CHANGE)
  - `fix`/`perf`: patch
  - `!` or `BREAKING CHANGE:` footer: major

### Troubleshooting Release Please

- No Release PR opened:
  - Ensure the workflow ran on `main` and `skip-github-pull-request` is `false`.
  - Ensure there are releasable commits since the last release/baseline (`feat`, `fix`, `perf`, or commits with `BREAKING CHANGE`). Commits like `ci`/`chore` do not trigger a release with the current config.
  - First release bootstrap: if there are no prior releases, add `bootstrap-sha` (the first commit SHA on `main`) to `.release-please-config.json` or include a commit with the footer `Release-As: vX.Y.Z`.

- Pre-1.0 breaking changes bump minor:
  - Add `"bump-minor-pre-major": true` to `.release-please-config.json` to bump MINOR instead of MAJOR when `<1.0.0`.

- Do not push tags manually:
  - Repo rules may block tag creation; let release-please create the tag via the Release PR merge.

### Where to find artifacts

- Docker: GHCR package page for the repo owner
- Helm: `helm pull oci://ghcr.io/<owner>/charts/private-llm-operator --version X.Y.Z`
- OCM: `oci://ghcr.io/<owner>/ocm`


