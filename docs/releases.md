# Releases

Moa releases use [Semantic Versioning](https://semver.org/spec/v2.0.0.html):
major versions contain incompatible changes, minor versions add compatible
functionality, and patch versions contain compatible fixes. Development builds
are labelled `dev`; only valid stable SemVer tags are offered as updates.

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/)
(`feat:`, `fix:`, `docs:`, and so on). The changelog is curated rather than
generated, and normal feature/fix PRs do not edit it. When preparing a release,
inspect the commits since the previous tag and write one coherent dated section
covering the user-visible changes in [CHANGELOG.md](../CHANGELOG.md).

## Release checklist

1. Confirm the version and release scope follow SemVer; inspect every commit
   since the previous tag and curate the new dated section in `CHANGELOG.md`.
2. Rebuild the embedded frontend. CI uses `npm ci && npm run build` in
   `pkg/serve/frontend`; locally, `bun esbuild.mjs --prune` from that directory
   is an equivalent alternative when npm is unavailable. Pruning is reserved
   for offline release builds; live watch mode retains immutable prior trees
   until `make clean` so in-flight requests cannot lose their selected bundle.
3. Verify the tree: `gofmt` changed Go files, then run `go test ./...`, `go vet
   ./...`, and `go build ./...` (plus relevant frontend tests).
4. Build the release with version, commit, and date injected through ldflags.
   Check `moa version` reports the expected metadata.
5. Create and push the annotated SemVer tag, push the release commit, and check
   the GitHub release/action completed successfully.

## Update checks and privacy

Release builds make a best-effort request to GitHub's public
`e-aleixandre/moa` latest-release endpoint. The request is timeout-bounded,
cached locally for six hours, and uses an ETag on cache refresh; no usage or
installation telemetry is sent. Disable it with `"update_check": false` in
Moa config or `MOA_NO_UPDATE_CHECK=1`. Update notices only link to the release;
they never download, install, or restart Moa.

Installing an update is always an explicit act, and never restarts anything:
see [`moa update`](./cli.md#update-subcommand).

## Distribution channels

Releases are published by GoReleaser (`.goreleaser.yml`) from a stable SemVer
tag: per-platform archives plus `checksums.txt` on the GitHub release, and a
Homebrew formula pushed to the `e-aleixandre/homebrew-tap` repository. The tap
push needs a `TAP_GITHUB_TOKEN` secret with write access to that repository;
without it the release still succeeds and only the tap update is skipped.

[`scripts/install.sh`](../scripts/install.sh) is the `curl | sh` installer
served from `https://letmoa.run/install.sh`. It resolves the latest tag from the
GitHub API, verifies the archive checksum, and installs to `/usr/local/bin` or
`~/.local/bin` — never with `sudo`.
