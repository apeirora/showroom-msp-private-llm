## Release Flow

1. Feature branch → open PR to `main`.
   - CI runs: Helm lint, golangci-lint, unit tests, compact coverage summary (HTML + artifacts).
   - Commit messages and PR title must be Conventional Commits.
2. Merge PR to `main`.
   - release-please (PR mode) evaluates commits on `main` and opens a single Release PR proposing the next version (`vX.Y.Z`), CHANGELOG, and manifest updates.
   - Review and merge the Release PR to publish the GitHub Release and tag.
   - Tag workflow builds and publishes:
     - Docker image: `ghcr.io/<owner>/private-llm-controller:X.Y.Z`; stable releases also update `:latest`.
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
  - First release bootstrap: if there are no prior releases, add `bootstrap-sha` (the first commit SHA on `main`) to `release-please-config.json` or include a commit with the footer `Release-As: vX.Y.Z`.

- Pre-1.0 breaking changes bump minor:
  - Add `"bump-minor-pre-major": true` to `release-please-config.json` to bump MINOR instead of MAJOR when `<1.0.0`.

- Do not push tags manually:
  - Repo rules may block tag creation; let release-please create the tag via the Release PR merge.

### Where to find artifacts

- Docker: GHCR package page for the repo owner
- Helm: `helm pull oci://ghcr.io/<owner>/charts/private-llm-operator --version X.Y.Z`
- OCM: `oci://ghcr.io/<owner>/ocm`


### Development RC for the BYOC HTTPS pilot

The temporary package configuration requests exactly `2.17.1-rc.1` through
`release-as` and marks its GitHub release as a prerelease. The existing GitHub
App and reviewed Release Please PR remain the publication route. The config
change itself does not publish an image, chart, tag or release.

1. Merge the reviewed provider changes, including the prerelease image-tag guard
   and chart image-version packaging fix, before publishing the RC. Merge this
   configuration through the normal PR checks. A main-branch push lets the
   existing Release Please workflow open or update its release PR.
2. Inspect that generated PR at its final head. Its title must be
   `chore(main): release 2.17.1-rc.1`. Expect a `CHANGELOG.md` entry, the release
   manifest, all five charts' `version` and `appVersion`, the operator image tag,
   and the ORD described-system version to change to `2.17.1-rc.1`.
   No chart dependencies or application source need to change for versioning.
3. Merge the release PR only after its normal checks and reviews pass. The
   existing App creates `v2.17.1-rc.1`; the tag-triggered workflow publishes the
   versioned image, five Helm charts, OCM component and release assets. Do not
   create an unprefixed tag or bypass the protected `v*` rule.
4. Verify the exact tagged source commit, successful publication run, GitHub
   prerelease flag, image digest, all five chart versions, and release checksums.
   Confirm `build-metadata.json` has `image.latest: false`, the stable image
   `:latest` digest is unchanged, and GitHub's latest stable release is unchanged.
   Render the published MSP umbrella with the development values and verify its
   operator image tag and BYOC TLS configuration before merging the dev pin.

This deliberately puts the next release PR on the RC version while development
continues on `main`. Until that PR merges, tracked chart and manifest versions
remain at `2.17.0`. The release PR then advances those files to the RC, so anyone
building from Git must be aware of that source state. Published stable artifacts
remain separate; this proposal neither changes production GitOps pins nor
proves any live environment's adoption.

`prerelease: true` controls GitHub release classification; `release-as` supplies
the exact version independently of conventional-commit bumps. With the stable
`2.17.0` baseline, the flag alone would still allow a stable calculated version.
Keep both settings for this first RC. After it is published, a new reviewed
configuration should advance `release-as` to the agreed next RC before another
release PR is merged. Leaving the override unchanged reuses the same version;
removing it does not mean “next RC number” (the pinned Release Please version
calculates `2.17.2-rc.1` for a fix after `2.17.1-rc.1`). Restoring stable release
configuration and publishing a stable version require a separate decision.

The generated changelog covers unreleased commits since the previous release;
it is not an acceptance report for the development pilot. Publication, rollout
and live HTTPS/key-rotation acceptance are separate checks.

References: [Release Please manifest options](https://github.com/googleapis/release-please/blob/v17.1.2/docs/manifest-releaser.md),
[the pinned action dependency](https://github.com/googleapis/release-please-action/blob/v4.3.0/package-lock.json),
and [prerelease classification in the pinned implementation](https://github.com/googleapis/release-please/blob/v17.1.2/src/manifest.ts).
