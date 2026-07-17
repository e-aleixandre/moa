# Releases

Moa releases use [Semantic Versioning](https://semver.org/spec/v2.0.0.html):
major versions contain incompatible changes, minor versions add compatible
functionality, and patch versions contain compatible fixes. Development builds
are labelled `dev`; only valid stable SemVer tags are offered as updates.

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/)
(`feat:`, `fix:`, `docs:`, and so on). The changelog is curated rather than
generated: before release, move the user-visible entries from **Unreleased**
into a dated version section in [CHANGELOG.md](../CHANGELOG.md).

## Release checklist

1. Confirm the version and release scope follow SemVer; curate `CHANGELOG.md`.
2. Rebuild the embedded frontend: `cd pkg/serve/frontend && bun esbuild.mjs`.
3. Verify the tree: `gofmt` changed Go files, then run `go test ./...`, `go vet
   ./...`, and `go build ./...` (plus relevant frontend tests).
4. Build the release with version, commit, and date injected through ldflags.
   Check `moa version` reports the expected metadata.
5. Create and push the annotated SemVer tag, push the release commit, and check
   the GitHub release/action completed successfully.

## Update checks and privacy

Release builds make a best-effort request to GitHub's public
`ealeixandre/moa` latest-release endpoint. The request is timeout-bounded,
cached locally for six hours, and uses an ETag on cache refresh; no usage or
installation telemetry is sent. Disable it with `"update_check": false` in
Moa config or `MOA_NO_UPDATE_CHECK=1`. Update notices only link to the release;
they never download, install, or restart Moa.

