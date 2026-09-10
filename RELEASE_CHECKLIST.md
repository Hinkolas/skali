# v0.1.0-alpha.1 release checklist

Findings from the pre-release audit (2026-08-24), grouped by where the work
happens: in the codebase, or outside it (repo settings, hosting, release
day). Within each group: what blocks the tag, what should be decided
consciously before it, and the deferred backlog. Finished items are
collected at the end.

## A. Changes in the codebase

### Non-optional

(none left)

### Strongly recommended

(none left)

### Optional (hardening backlog, fine after 0.1.0)

- [x] Volume restore uses rooted filesystem operations, defers symlinks,
      rejects unsafe archives, and preserves numeric ownership.
- [ ] `godotenv.Load()` runs unconditionally in `skalid serve`
      (`internal/config/config.go:214`); a stray `.env` in the container
      workdir can supply missing config. Gate on dev builds.
- [ ] No expiry sweep for sessions and login challenges; tables grow
      unbounded on long-lived installs.
- [ ] Password policy is 8 characters minimum with no other checks
      (`internal/auth/auth.go:505-510`); make it a conscious default.
- [x] Cookie-authenticated WebSockets enforce same-origin checks before
      opening exec sessions; CLI bearer clients remain supported.
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

Repo settings, hosting, and release-day steps; the setup script is versioned,
but these settings must be applied on GitHub.

### Non-optional

- [ ] **Verify channel installation, version pinning, and schema URLs after publication.**
      README installation fetches `install.sh` from `main` with
      `SKALI_CHANNEL=beta` set on `sh`; verify it selects the newest published
      version, and that the separate `SKALI_VERSION` example pins the alpha.
      The editor schema URL uses the `v0.1.0-alpha.1` tag on
      raw.githubusercontent.com. No custom domain is needed to install.
      Schema `$id` values remain stable identifiers under `skali.dev`; they
      are not the download URLs used by the README or installed agent skill.
- [ ] **Make the repository public** before the tag. `install.sh`
      downloads from public release URLs and the README clones it.
- [ ] **Flip GHCR packages public after the first push.** Packages created
      from a private repo default to private; the released CLI defaults to
      `ghcr.io/hinkolas/skalid` with no fallback
      (the image resolution paths in `cmd/skali`), so
      `skali dev` and `skali cluster init` fail on every fresh machine
      until this manual one-time setting is made.
- [ ] **Rehearse the release end to end** on a prerelease tag
      (`task release:tag V=v0.1.0-alpha.1`; tags are cut with that task, never
      `git tag` by hand): `install.sh` on a clean Linux server and
      a clean Mac, `sudo skali cluster` through init with the published
      images, `skali dev` outside the repo, `skali cluster upgrade` from a
      released binary, `skali upgrade` from the previous released binary
      (`sudo` on the Linux server, plain on the Mac), a `skali cluster join`
      from a released CLI with no local `skali-hostd` (it downloads the
      release's asset and caches it), and `skali cluster reset-password`
      (never run on a live cluster yet). Then tag `v0.1.0-alpha.2` and update to it from the console
      (System / Software update on the beta channel): every node's hostd
      and k3s move, the bundle rolls, and the console reconnects.
- [ ] **Cluster nodes reach GitHub.** Console updates download
      `skali-hostd` and k3s from github.com on every node and the daemon
      scans api.github.com; confirm egress or set `SKALI_RELEASE_BASE`
      in `/etc/skali/hostd.env` and `SKALI_UPDATE_FEED_URL` to a mirror.

### Before accepting public contributions

- [ ] **Enable public GitHub protections.** Review
      `.github/main-protection.json`, then run
      `scripts/configure-public-repo.sh --apply` immediately after making the
      repository public. It enables private vulnerability reporting, secret
      scanning, push protection, and required CI on main, including for admins;
      force pushes and deletion are blocked. Pull requests need passing checks
      and resolved conversations; zero required approvals supports a solo
      maintainer. Existing branch protection is never overwritten.
      Confirm that Security / Report a vulnerability is available and that
      the five required check names match an actual PR run (the changes job
      must be required: a skipped job counts as passing).
      On 2026-09-09 GitHub refused branch protection for the private repository
      on the current plan (403); private vulnerability reporting returned 404.
      Dependabot alerts were enabled successfully.

### Optional history cleanup

- [ ] Historical guestbook binaries remain in old commits (roughly 21 MB in
      total). They are not credentials and do not block publication. A history
      rewrite is optional, irreversible for existing clones, and requires a
      separate decision. Do not run the old root-binary filter command: the
      earlier root `skali` blob is no longer the relevant finding.

## Done

- [x] **Public-readiness fixes (2026-09-09).** Token config saves repair file
      permissions atomically. Forwarded client addresses require trusted peers;
      platform proxy discovery uses exact pod IPs and the console has a Traefik
      ingress policy. Regression tests cover spoofed headers, discovery failure,
      and existing loose token files. Documented encryption-key preservation and
      the trusted-workload boundary; removed the unsupported install domain and
      safety claims. Added project-owned Compose Postgres and `task test:db`.
      Full-history Gitleaks runs in CI with exact synthetic-fixture exceptions;
      realistic-looking seed tokens were replaced with obvious fixture strings.

- [x] **Squash migrations before the first alpha tag.**
      `00001_baseline.sql` creates the final schema directly and has a complete
      Down path. A baseline marker rejects the old development history without
      changing it. Future releases append migrations and preserve this baseline;
      no further history squash after `v0.1.0-alpha.1`.
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
      `build_executor` API error). The migration squash also removed the
      remaining historical R4/R5 notes.
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
      `latest` manifest keep prereleases from moving the stable image tag. New `.github/workflows/ci.yml` (go vet + go test
      against a Postgres service, `npm test` + `npm run check` + `npm run lint`,
      `goreleaser check`) runs on every pull request, and
      on tags via `release.yml`, which publishes only when it passes.
      2026-09-08: gofmt, `go mod tidy`, and generated-code drift checks
      added; govulncheck and `npm audit` moved to the daily `audit.yml`
      (run it by hand before tagging). Made the suite green for CI: the
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
      a release script, local run, server setup, deploy, manifest,
      docs index. Contributor material moved to `docs/development.md`
      (now uses project-owned Docker Compose);
      `.env.example` names database `skali` to match the Taskfile; the
      docs pages are linked.
- [x] **Readable dev versions** (2026-08-26): dev default is `v0.0.0-dev`,
      matching the `v`-prefixed tags and ldflags; `Taskfile.yml` dropped
      `--always`.

The six P1 fixes and their prerelease compatibility contract are described in
[prerelease safety](docs/prerelease-safety.md). Scheduled backup policies remain
inactive and now produce deployment warnings.
