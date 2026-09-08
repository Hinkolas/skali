# 0.1.0 release checklist

Findings from the pre-release audit (2026-08-24), grouped by where the work
happens: in the codebase, or outside it (repo settings, hosting, release
day). Within each group: what blocks the tag, what should be decided
consciously before it, and the deferred backlog. Finished items are
collected at the end.

## A. Changes in the codebase

### Non-optional

(none left)

### Strongly recommended

- [ ] **Squash migrations to a clean 00001 before the tag.** `00013` has
      no `-- +goose Down` (breaks `goose down`) and `00014`/`00016`
      contain "pre-release wipe" DROP TABLEs. No prior installs exist, so
      squashing is free now and impossible after 0.1.0.
### Optional (hardening backlog, fine after 0.1.0)

- [ ] `realIP` trusts `X-Real-IP` / `X-Forwarded-For` with no
      trusted-proxy allowlist (`internal/api/middleware.go:135`); partial
      login rate-limit bypass for callers that reach the Service directly.
      Add a TRUSTED_PROXIES CIDR check.
- [x] Volume restore uses rooted filesystem operations, defers symlinks,
      rejects unsafe archives, and preserves numeric ownership.
- [ ] `godotenv.Load()` runs unconditionally in `skalid serve`
      (`internal/config/config.go:214`); a stray `.env` in the container
      workdir can supply missing config. Gate on dev builds.
- [ ] No expiry sweep for sessions and login challenges; tables grow
      unbounded on long-lived installs.
- [ ] Password policy is 8 characters minimum with no other checks
      (`internal/auth/auth.go:505-510`); make it a conscious default.
- [ ] WebSocket `CheckOrigin` returns true
      (`internal/api/exec_handlers.go:55`); safe today (bearer auth), must
      be revisited when the web-terminal ticket flow lands.
- [ ] `.goreleaser.yaml`: no checksum signing (cosign/gpg) or SBOM; a
      compromised release channel can serve a fake checksums.txt too. No
      `mod_timestamp`, so builds are not reproducible.
- [ ] `build/skalid.release.Dockerfile:6` and `build/skalid.Dockerfile:11`
      use floating `alpine:3.21` (EOL ~Nov 2026); bump and digest-pin.
- [x] Pin the patched Go 1.26.8 toolchain for builds and releases;
      vulnerability scanning gates CI.
- [ ] `/healthz`, `/openapi.yaml`, and `/token` are served but absent from
      `api/openapi.yaml` (the `/v1/` surface is exactly in sync); add the
      root routes, `/healthz` at minimum.
- [ ] Personal fixtures in tests: `skali.khz.dev` in
      `cmd/skali/remote_test.go:127-176`, username `nhinke` in
      `internal/installer/limavm/limavm_test.go:317,319`; swap for
      example.com-style values.
- [ ] `install.sh` private-repo language (GITHUB_TOKEN branch, lines
      11, 46-49, 76) becomes dead code once public; simplify.
- [ ] `internal/cliconfig/config.go:96`: `Save` never rechmods a
      pre-existing wider-mode token file.
- [ ] Align `web/package.json` version (`0.0.1`) with the release tag.
- [ ] **Prebaked dev node image** (cold-start follow-up to the built-in
      image imports in `internal/localdev/images.go`): publish a
      `skali-dev-node` image built FROM the pinned `rancher/k3s`, with an
      airgap tarball of the k3s built-ins, platform images, and released
      skalid dropped into `/var/lib/rancher/k3s/agent/images/` so k3s
      auto-imports everything on first boot. A fresh `skali dev` then
      needs one layer-cached ghcr pull and no save/ctr import pipe; reset
      stays fast. Bake images only, never booted cluster state (certs,
      etcd, initdb are per-install). Needs multi-arch builds, a CI
      rebuild per release, and a seam for the
      `localdev.K3sImage == installer.K3sVersion` drift test; recorded
      `state.K3sImage` drift routes existing installs through
      `dev reset`, which is the only pickup path anyway.

## B. Outside the codebase

Repo settings, hosting, and release-day steps. Nothing here is a commit.

### Non-optional

- [ ] **Register `skali.dev` and serve install.sh plus the schemas.** The
      README installs via `https://skali.dev/install.sh`; that URL must
      redirect to (or mirror) the `install.sh` release asset on GitHub.
      Serve `schemas/*.json` (four files) one-to-one at
      `https://skali.dev/schemas/v1/<file>` with
      `Content-Type: application/schema+json` and
      `Access-Control-Allow-Origin: *` (browser editors need CORS).
      Decision 2026-08-26: host manually until the docs site exists;
      re-copy after any `go generate` that changes `schemas/`. The
      installed agent skill writes the `skali.schema.json` URL into every
      manifest, so both must resolve before the tag.
- [ ] **Make the repository public** before the tag. `install.sh`
      downloads from public release URLs and the README clones it.
- [ ] **Flip GHCR packages public after the first push.** Packages created
      from a private repo default to private; the released CLI defaults to
      `ghcr.io/hinkolas/skalid` and `skali-web` with no fallback
      (`cmd/skali/cluster_flow.go:524,554`, `dev_upgrade.go:110`), so
      `skali dev` and `skali cluster init` fail on every fresh machine
      until this manual one-time setting is made.
- [ ] **Rehearse the release end to end** on a prerelease tag
      (`task release:tag V=v0.1.0-rc.1`; tags are cut with that task, never
      `git tag` by hand): `install.sh` on a clean Linux server and
      a clean Mac, `sudo skali cluster` through init with the published
      images, `skali dev` outside the repo, `skali cluster upgrade` from a
      released binary, and `skali cluster reset-password` (never run on a
      live cluster yet). Then tag `rc.2` and update to it from the console
      (System / Software update on the beta channel): every node's hostd
      and k3s move, the bundle rolls, and the console reconnects.
- [ ] **Cluster nodes reach GitHub.** Console updates download
      `skali-hostd` and k3s from github.com on every node and the daemon
      scans api.github.com; confirm egress or set `SKALI_RELEASE_BASE`
      in `/etc/skali/hostd.env` and `SKALI_UPDATE_FEED_URL` to a mirror.

### Strongly recommended

- [ ] **Rewrite history to drop the binary blob** (`c8d709a`) before the
      repo goes public, while there are no other clones:
      `git branch -rd origin/rework`, then
      `git filter-repo --path skali --invert-paths --force`, re-add
      `origin`, force-push `main`. Optionally delete the stale `rework`
      branch on GitHub.

## Done

- [x] **ROADMAP.md reconciled** (2026-08-26): status section refreshed,
      permission system, metrics, bucket backup, console cleanup, the
      console-scope decision, and skill/schema lockstep checked off; new
      items for stateful removal and the registry-node move; the security
      pass item now lists the audit's open findings.
- [x] **Project deletion guard** (2026-08-26): `project.Delete` refuses
      (409, `ErrProjectHasEnvironments`) while environments exist, in one
      transaction with the delete; OpenAPI and `docs/limitations.md`
      updated; unit test added.
- [x] **Known limitations documented** (2026-08-26): new
      `docs/limitations.md` (volumes never reclaimed, project delete does
      not purge environments, registry node not removable, manual disaster
      recovery, local builds, single management plane), linked from the
      README, `docs/storage.md`, and the reconciler comments.
- [x] **Dangling plan references** (2026-08-26): every "section N.N",
      R-milestone, "cli-dev transcript", and "later milestone" reference in
      Go code, tests, and `.dockerignore` rewritten in plain terms,
      including three user-facing strings (`--build` help and error, the
      `build_executor` API error). Only `migrations/*.sql` still carry R4/R5
      notes; they go with the migration squash.
- [x] **LICENSE and SECURITY.md** (2026-08-26): Apache-2.0 (canonical
      text from apache.org), a security policy using GitHub private
      vulnerability reporting, and a License section in the README.
- [x] **Public-copy pass** (2026-08-26): dropped the disabled Restart
      button from `WebProcessPanel` (the header's Restart works); ROADMAP
      no longer names personal infrastructure; R-milestone jargon removed
      from `docs/build-matrix.md` and `.env.example`; `examples/file-sharing`
      is labeled a reference manifest (README plus header comment) that
      validates but has no source.
- [x] **`skali cluster restore` hidden** (2026-08-26): `Hidden: true`, a
      comment documenting the planned contract, and a plain "not available
      yet" error for anyone who finds it.
- [x] **Release plumbing** (2026-08-26): `releaseVersionPattern` accepts
      alpha/beta/rc prereleases (never git-describe shapes) and
      `localdev.VersionOlder` orders them, so an rc CLI resolves its own
      images and the release rehearsal can run on `v0.1.0-rc.1`;
      goreleaser `release.prerelease: auto` plus `skip_push: auto` on the
      `latest` manifest, and the `skali-web:latest` tag is skipped for
      prerelease tags. New `.github/workflows/ci.yml` (go vet + go test
      against a Postgres service, `npm run check` + `npm run lint`,
      `goreleaser check`) runs on tags only (via `release.yml`, which
      publishes only when it passes) or by hand from the Actions tab. Made the suite green for CI: the
      cliprompt text-editing test now drives a sized real PTY (the huh
      nil-model panic came from a zero-width pty), one Svelte file
      formatted, one external-link eslint pass-through.
- [x] **Remove the service graph mock** (2026-08-26): deleted
      `src/lib/mock/`, `src/lib/components/graph/`, and the `graph/`
      route; the tab is a `stub: true` placeholder. Removed the mock-only
      `cache`/`storage`/`ingress` kinds, the legacy status vocabulary, and
      the `master`/`worker` node roles; `StatusPill`/`StatusDot` take real
      `ServiceHealth` only. Future UI testing uses seed data against real
      APIs, never a mock layer.
- [x] **Fix the login page dead controls** (2026-08-26): removed the inert
      "Forgot?" link, the "Use access token" button, the fake
      `$ skali login` footer, and the subtitle; the card now shows the
      daemon version from the `Skali-Version` header. Lockout recovery is
      `skali cluster reset-password` (a one-shot `skalid user
      reset-password` job on a server node; revokes sessions;
      `--disable-2fa` drops a lost authenticator).
- [x] **Placeholder-page wording** (2026-08-26): "coming soon" / "will
      arrive in a future version" on all four stub templates; no
      "prototype" copy remains.
- [x] **Purge the tracked binary** (2026-08-26): `git rm skali`;
      `.gitignore` covers `/skali`, `/skalid`, `/skali-hostd`. History
      rewrite is tracked in B.
- [x] **README rewritten for users** (2026-08-26): install via
      `skali.dev/install.sh`, local run, server setup, deploy, manifest,
      docs index. Contributor material moved to `docs/development.md`
      (documents the `task db` dependency on a global `~/Taskfile.yml`);
      `.env.example` names database `skali` to match the Taskfile; the
      docs pages are linked.
- [x] **Readable dev versions** (2026-08-26): dev default is `v0.0.0-dev`,
      matching the `v`-prefixed tags and ldflags; `Taskfile.yml` dropped
      `--always`.

The six P1 fixes and their prerelease compatibility contract are described in
[prerelease safety](docs/prerelease-safety.md). Scheduled backup policies remain
inactive and now produce deployment warnings.
