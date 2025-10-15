## Versioning

We use SemVer with Conventional Commits to infer versions. Defaults:

- `feat`: MINOR
- `fix`, `perf`: PATCH
- `!` or `BREAKING CHANGE:` footer: MAJOR

Examples:
- `feat(chart): add traefik dependency` → minor
- `fix(controller): prevent nil pointer` → patch
- `feat(api)!: drop deprecated field` or footer → major (pre-1.0 → MINOR thanks to config)

### Pre-1.0 behavior

- `.release-please-config.json` sets `"bump-minor-pre-major": true`; BREAKING changes while `<1.0.0` increment MINOR instead of MAJOR.

### Bootstrapping the first release

- First release uses `bootstrap-sha` (root commit) and/or `Release-As` to seed the baseline if no prior tag exists.

### Forcing a specific version

- Add a commit message footer:
```
Release-As: vX.Y.Z
```
This forces the next release version regardless of conventional types.

Hotfixes:
- For urgent patches, open a PR to `main` with a `fix:` commit.
- Release-please will produce a patch release.

Version embedding:
- Binary embeds version via `-ldflags "-X main.version=$(VERSION)"`.
- Helm chart uses the same version as the tag.


