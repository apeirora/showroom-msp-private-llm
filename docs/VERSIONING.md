## Versioning

We use SemVer with Conventional Commits to infer versions:

- `feat`: MINOR
- `fix`, `perf`: PATCH
- `!` or `BREAKING CHANGE:` footer: MAJOR

Examples:
- `feat(chart): add traefik dependency` → minor
- `fix(controller): prevent nil pointer` → patch
- `feat(api)!: drop deprecated field` or footer → major

Hotfixes:
- For urgent patches, open a PR to `main` with a `fix:` commit.
- Release-please will produce a patch release.

Version embedding:
- Binary embeds version via `-ldflags "-X main.version=$(VERSION)"`.
- Helm chart uses the same version as the tag.


