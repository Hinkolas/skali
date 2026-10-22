# v0.1.3 dependency review — 2026-10-22

Review for [#184](https://github.com/Hinkolas/skali/issues/184), based on
`2912a2b81730ea10b819f63aeb2c9167a1a3163d` plus the Vitest patch described
below. **This is a completed inventory and risk assessment, not a clean
release-security sign-off.** Runtime remediation and explicit dispositions
are still required before stable.

## Result

- Fixed Studio's two moderate npm package findings by updating Vitest and its
  test dependencies from 5.0.0 to 5.0.5. Studio and website npm audits now
  report zero findings. Studio check, 80 tests, lint, and production build pass.
- Main Go source has no vulnerable imported packages or called symbols on the
  four released OS/architecture targets. All four example modules have the
  same result on both Linux architectures, using the release's Go 1.26.10.
- Scanned 42 repository-selected image references on both Linux architectures,
  plus two freshly built skalid images: **86 successful scans, no skipped
  platforms or failed pulls**. 64 scans contain high or critical package
  findings. Counts include repeated advisories across packages and binaries;
  they are not counts of distinct vulnerabilities or exploitable paths.
- Both newly built skalid images have zero high/critical findings, one medium
  OS-package finding, two unknown-severity OS-package entries, and the
  unimported OpenPGP module advisory explained below.
- The PostgreSQL image versions and the old registry require release work.
  SeaweedFS, k3s/edge, operators, optional Longhorn, downloaded host tools, and
  build tools also need the specific dispositions listed below. Their
  findings have not been silently accepted.

The machine-readable [review evidence](dependency-review-v0.1.3.json) records
input-file SHA256 hashes, resolved image digests and image IDs, per-platform
counts, advisory IDs for source/native-binary scans, and SHA256 hashes of raw
reports. Full reports and logs are retained locally under
`.cache/dependency-audit/2026-10-22/`; future CI image runs upload them as
artifacts for 30 days. The compact evidence file is the durable record;
local raw reports and CI artifacts are not permanent storage.

## Exact inputs and coverage

| Input | Version/scope |
| --- | --- |
| Go source and release compilation | Go 1.26.10; `CGO_ENABLED=0`; linux and darwin, amd64 and arm64 |
| Source scanner | govulncheck v1.7.0; Go advisory database, last modified 2026-10-21T00:27:29Z in the recorded scan configuration |
| npm | npm 11.19.1, Node 24.22.0 locally; Studio also checked/tested/linted/built with Node 22.23.4 to match CI's major; lockfile audits include development dependencies |
| Studio change | Vitest/@vitest/mocker 5.0.0 → 5.0.5; related Vitest dependency changes only |
| Image scanner | Trivy 0.76.1; official macOS arm64 archive SHA256 `11a9b6cd2752011b54f01ea4bd0dbfa9f0c41dc6538d5a3f9d23955f471bf4e5` |
| Trivy advisory DB | Schema 2, updated 2026-10-22T13:15:26.425424764Z; initially downloaded 2026-10-22T17:03:39.632683Z |
| Image scan policy | OS and language vulnerabilities, all severities, no ignore file; remote registry resolution for repository images |
| Fresh skalid images | Release Dockerfile, current embedded Studio, linux/amd64 and linux/arm64 Go binaries; local version `v0.1.3-review-184`; no images published |
| Scan limits | Package inventory is not call-path analysis; Go binary scans identify included symbols, not exercised commands; no exploit attempts or live upgrades performed |

The k3s built-in image inventory was compared with the upstream
[`k3s-images.txt` for v1.36.3+k3s1](https://github.com/k3s-io/k3s/releases/download/v1.36.3%2Bk3s1/k3s-images.txt).
It matches the eight references in `localdev.RequiredImages`. The inventory
also includes cert-manager's controller-created ACME solver, both supported
PostgreSQL majors, and all 13 Longhorn images from arguments, environment
values, and Pod specs in the vendored manifest.

The k3s container scan covers its packaged binaries. A production host's
operating system, kernel, existing images, overridden tool versions, Lima guest
OS, externally supplied Docker/buildx, and arbitrary application dependencies
are installation-specific inputs and require their own inventory. A new tag
resolution can differ from this dated evidence even when the tag is unchanged.
The release Dockerfile also installs CA packages from the live Alpine
repository; repeat the assembled-image scan on the actual candidate.

## Fixed finding

Studio used the vulnerable `@vitest/mocker` 5.0.0 through Vitest. Upstream
advisories describe unauthenticated mock registration and a Windows redirect
allowlist traversal; the fixed range begins at 5.0.3. The patch selects 5.0.5
and updates the lockfile. This is development/test tooling; it is not a claim
that the embedded static Studio exposed a vulnerable Vitest server.

Sources: [mock registration advisory](https://github.com/vitest-dev/vitest/security/advisories/GHSA-qxg6-89mg-rqv8),
[Windows traversal advisory](https://github.com/vitest-dev/vitest/security/advisories/GHSA-xp26-3454-mjgp).
The [2026-10-22 hosted audit](https://github.com/Hinkolas/skali/actions/runs/39775148302)
failed on these same npm findings; the local post-patch audit is clean.

## Accepted source-review exceptions

These dispositions apply only to this code and the hashed dependency inputs.
Reassess them when imports or usage change. They do not suppress scanner
output and do not apply to other executables that use the same module.

| Advisory | Disposition and reason |
| --- | --- |
| GO-2026-5932 in main `golang.org/x/crypto` v0.57.0 | The unsafe/unmaintained package is `openpgp`; main source imports no affected package and calls no affected symbol on any released target. Keep the module for its other APIs. |
| GO-2026-5932 in three examples' x/crypto v0.56.0 | Same unimported-package disposition. `hello-world` has no advisory findings. |
| GO-2026-6603, 6610, 6611, 6612, 6617 in example x/net v0.57.0/v0.58.0 | These are module-only findings in guestbook, file-sharing/web, and deployment-timing: no affected package is imported on either Linux architecture. A module update is a follow-up when those examples next change; no current call-path finding requires it. |

Go advisory records are available from
[`vuln.go.dev/ID/<advisory>.json`](https://vuln.go.dev/ID/GO-2026-5932.json).
The assembled skalid scan flags OpenPGP from module build metadata; the source
analysis above is the stronger evidence for this specific binary's usage.

## Release follow-up tasks

These are local tracking entries for independently reviewable work; no new
GitHub issues or release-risk approvals are implied. Complete a row with an
implementation/verification reference or an explicit, bounded decision.

| ID | Priority and task | Required evidence before closing |
| --- | --- | --- |
| D1 | **Before stable: update PostgreSQL 17/18 system images and assess pgvector.** Current pins are 17.9 and 18.4. | Choose maintained same-major images containing at least the applicable 17.11/18.6 core fixes; scan both architectures and extension/OS packages. Verify provisioning, extensions, role isolation, SQL, logical backup/restore, and existing-pool patch upgrades. Catalog changes affect newly created pools; persisted pools retain their copied image, so demonstrate how existing installs receive the fix too. |
| D2 | **Before stable: replace or rebuild registry 2.8.3.** | Its binary uses Go 1.20.8 and its Alpine 3.18.12 base is end of support. Scan a maintained candidate and verify existing registry data, token auth/signatures, push/pull, import/blob mounting, and the slow authenticated upload regression. Distribution 3.1.2 is an upstream candidate to evaluate, not an approved drop-in upgrade. |
| D3 | **Before stable: disposition k3s and its edge/runtime dependencies.** | Inspect the k3s, containerd/runc/CNI, Traefik, CoreDNS, Helm helper, local-path and metrics-server findings. Select a supported patch bundle or document advisory-specific non-applicability; update the installer/localdev/built-in inventory together. Rehearse cluster and dev upgrades, network policies, aliases/TLS, and slow pushes. |
| D4 | **Before stable: SeaweedFS and CNPG/cert-manager operator review.** | Scan maintained candidates/rebuilds and separate used server paths from unused bundled features. For SeaweedFS verify authenticated S3, bootstrap deny identity, credential rotation/retirement, bucket deletion/quota, restore and cancellation, and metadata/replication behavior. For operators verify bootstrap, reconciliation and ACME on the chosen versions. |
| D5 | **Before a release supporting Longhorn: disposition its full image set.** | Manager/engine/instance/share/backing/support/UI and CSI images contain findings. Upgrade as a compatible set or document exact non-applicability. Verify existing-volume attach, read/write, restart, backup/restore, node disruption and upgrade. Local-path testing does not validate this driver. |
| D6 | **Before publishing: review build infrastructure and release binaries.** | BuildKit v0.33.1, binfmt/QEMU, Node build image, CI PostgreSQL, GoReleaser and sqlc have findings. Use updated/rebuilt tools or document the actual affected command/input boundary. Record exact resolved GoReleaser/Node/action versions in the final release run. Preserve generated-store consistency when changing sqlc. |
| D7 | **Before declaring installer/dev review clean: refresh or disposition downloaded k3d/Lima binaries and native networking.** | The pinned k3d 5.9.0 and Lima 2.2.0 executables contain vulnerable Go symbols, including the current x509 distrust advisory. Change versions/checksums together or provide a bounded applicability decision; retest native dev/VM creation and upgrade on supported hosts. Include socket_vmnet 1.2.2 and the guest OS in the native review. |
| D8 | **Candidate hygiene: refresh the Alpine runtime/base and record floating inputs.** | The new skalid image retains medium CVE-2026-85091 in zlib and unknown CVE-2026-107891 entries in musl/musl-utils; fixed package revisions are reported. Use a maintained patched base/package set or document an advisory-specific decision. Record builder/example image digests; rerun scans on the actual candidate after all fixes. |

D1 is supported by PostgreSQL's own notices, beyond scanner counts. For
example, core regexp issue CVE-2026-14664 is fixed in 17.11 and 18.6, and
SSL/GSS recursion issue CVE-2026-6479 is fixed in 17.10 and 18.4. The 18.4 pin
therefore does not include the later core fixes. Scan counts alone missed some
18.x core findings, which is why upstream version notices are part of this
review. [PostgreSQL security notices](https://www.postgresql.org/support/security/)

The database scans also flag pgvector CVE-2026-103484, OS libraries, Perl, and
Python packages. They include duplicated package/source matches and conditional
features such as 32-bit code. Verify vendor applicability and installed
extension versions; do not interpret every package match as a reachable
64-bit PostgreSQL exploit.

D3's Traefik critical match is CVE-2026-88007, with a reported fix in 3.7.13.
Its stated HTTP/3-backend/NTLM conditions are not configured by skali's rendered
routes. Confirm that condition against the upstream advisory and installation
before assigning a final exception; this one match does not establish that
skali exposes the exploit, or resolve its other Traefik/runtime findings.

SeaweedFS's binary was built with Go 1.25.12; its findings include Go TLS/x509,
HTTP/2, gRPC, compression and optional library APIs. The current root module's
Go/x/net update does not repair dependencies compiled into that image. The
same distinction applies to operator and Longhorn binaries.

## Downloaded and local tool binaries

Pinned k3d assets for all four supported hosts and Lima archives for both
Darwin architectures were downloaded and verified against checksums in the
repository. `limactl` was extracted without running it. Binary govulncheck
identified the following distinct advisory IDs with included symbols:

| Binary | Observed Go compiler | Symbol-level advisory IDs |
| --- | --- | --- |
| k3d 5.9.0, four hosts | 1.26.3 | 42 on each host |
| Lima 2.2.0 limactl, two Darwin hosts | 1.26.5 | 33 on each host |
| Local GoReleaser 2.18.3 (Homebrew, darwin/arm64) | 1.27.2 | 8 |
| Local sqlc 1.30.0 (darwin/arm64) | 1.26.9 | 13 |

All include GO-2026-6809, the compromised-intermediate x509 distrust update.
These are binary-symbol matches, not source call traces or exploit proofs.
Local GoReleaser/sqlc binaries are evidence about this machine, not proof of
the contents of the Linux binaries downloaded by CI. The exact CI executables
must be checked for the final release. A Trivy filesystem scan of these Mach-O
executables recognized no language files and is **not counted as a clean
audit**; govulncheck's binary results are the applicable evidence.

The Lima repository's published guest-agent-socket advisory is fixed by 2.1.3
and thus by the 2.2.0 pin. That does not cover new dependency findings.
[Lima advisories](https://github.com/lima-vm/lima/security/advisories)
The k3d and socket_vmnet public advisory endpoints returned no entries on the
review date; an empty advisory list does not establish absence of risk.
socket_vmnet's native code and the full Lima guest-agent/QEMU payload were not
binary-scanned here and remain explicit coverage limits under D7.

Other workflow inputs were inventoried: govulncheck 1.7.0, Gitleaks 8.30.1
(download checksum pinned), sqlc 1.30.0, and GoReleaser's floating `~> v2`.
Actions use moving major tags: checkout v5, setup-go v6, setup-node v5,
setup-sqlc v5, goreleaser-action v7, Docker setup-qemu/setup-buildx/login v4,
and Pages configure/upload/deploy v6/v5/v5. The new audit artifact upload uses
v5. These references are not immutable provenance; pinning/reproducibility
belongs to the existing #121/#180/#181 follow-ups. Do not label this inventory
a transitive audit of every action's JavaScript/native payload. The manual
image job adds Trivy 0.76.1 with an exact archive SHA256.

## Repeat the review

The existing daily/manual source workflow remains separate from release CI.
The Go job now explicitly preserves the release toolchain for example modules.
The new **manual `images=true` input** scans both image architectures and
uploads inventory, exact digests, DB timestamp, JSON reports, logs and failures.
It does not run the larger image inventory every day or publish anything.

After these changes are available on the intended ref:

```sh
gh workflow run audit.yml --ref <release-branch-or-tag> -f images=true
```

Run locally from the repository root with Go, Node and Trivy available:

```sh
# Inspect without pulling or scanning images.
go run scripts/audit-images.go

# Scan repository inputs on both architectures; output is ignored by Git.
TRIVY_BIN=trivy node scripts/audit-images.mjs .cache/dependency-audit/images

# Scan an actual published candidate as well as the repository inventory.
AUDIT_EXTRA_IMAGES=ghcr.io/hinkolas/skalid:<candidate> \
  node scripts/audit-images.mjs .cache/dependency-audit/candidate
```

Image scope can be narrowed with `AUDIT_SCOPES=runtime,longhorn,build,examples,candidate`
and `AUDIT_PLATFORMS=linux/amd64,linux/arm64`. `AUDIT_IMAGE_SOURCE=docker`
supports locally built candidate images. The defaults scan all scopes on both
architectures directly from registries. The runner exits 1 for high/critical
findings or an unsupported OS, 2 for a failed scan/platform mismatch, and 0
otherwise. Medium/low/unknown findings still appear and require review.

For source scans, install the scanner using the release toolchain, then set
the target environment on that native scanner executable. Setting GOOS before
`go run` would try to execute a cross-compiled scanner on the host:

```sh
GOTOOLCHAIN=go1.26.10 GOBIN=/tmp/skali-audit-tools \
  go install golang.org/x/vuln/cmd/govulncheck@v1.7.0
GOTOOLCHAIN=go1.26.10 GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  /tmp/skali-audit-tools/govulncheck -json ./cmd/... ./internal/...
# Repeat for linux/arm64, darwin/amd64, darwin/arm64.
# In each examples/**/go.mod directory, scan ./... for both Linux targets.
npm audit --prefix studio --package-lock-only --ignore-scripts --json
npm audit --prefix website --package-lock-only --ignore-scripts --json
/tmp/skali-audit-tools/govulncheck -mode=binary -json /path/to/verified/tool
```

Re-download native tools from the exact pins in `internal/localdev/k3d.go`
and `internal/installer/limavm/provision.go`, verify the repository checksums,
and scan each host binary. Check upstream security/release notices as well as
automated results. Record fixed findings, remaining applicability decisions,
new exact inputs and verification references in a dated review. Preserve raw
reports with the final release evidence before local/CI retention expires.

## Verification of this change

- Studio: `npm run check`, `npm test` (80/80), `npm run lint`, `npm run build`
  passed on both Node 24.22.0 and Node 22.23.4.
- Source: 12 scans across four main targets and eight example targets;
  no affected imported packages/called symbols.
- Images: 84 repository scans plus two newly assembled daemon images;
  expected full-run exit 1 for real findings, not a scanner failure.
- Audit runner: actual clean image returns 0; an absent local image returns 2
  and records failure; high/critical inventory findings return 1. All recorded
  actual platforms match the requested architecture.
- Inventory compilation/vet, Node syntax check, actionlint 1.7.12, and diff
  whitespace checks pass. No runtime behavior or image pins changed.
- Hosted execution of the new optional image job remains for manual execution
  after publication of the branch. The runner was exercised locally.

## Image inventory and findings

The following per-platform counts are package findings. The JSON evidence
contains full digests and report hashes. `C/H/M/L/U` means critical, high,
medium, low and unknown severity. Each architecture was scanned independently;
where the counts agree they are shown once for readability.

| Reference | Scope | Architectures | C/H/M/L/U |
| --- | --- | --- | --- |
| `alpine:3.22` | examples | amd64, arm64 | 0/0/0/0/0 |
| `alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6` | build | amd64, arm64 | 0/0/1/0/2 |
| `chrislusf/seaweedfs:4.39` | runtime | amd64, arm64 | 0/23/28/28/5 |
| `docker.io/library/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6` | build | amd64, arm64 | 0/0/1/0/2 |
| `docker.io/library/postgres:17.11@sha256:2d2b8998d31037bf721cfdf764d76ba74171b4fab3431b7f72c27c56ddbdf9e3` | build | amd64, arm64 | 2/86/140/141/9 |
| `docker.io/longhornio/backing-image-manager:v1.12.1` | longhorn | amd64, arm64 | 1/37/30/1/4 |
| `docker.io/longhornio/csi-attacher:v4.12.0` | longhorn | amd64, arm64 | 0/35/26/4/1 |
| `docker.io/longhornio/csi-node-driver-registrar:v2.17.0` | longhorn | amd64, arm64 | 0/25/19/1/0 |
| `docker.io/longhornio/csi-provisioner:v5.3.0` | longhorn | amd64, arm64 | 2/37/45/3/2 |
| `docker.io/longhornio/csi-resizer:v2.2.1` | longhorn | amd64, arm64 | 0/24/19/4/1 |
| `docker.io/longhornio/csi-snapshotter:v8.6.0` | longhorn | amd64, arm64 | 0/26/22/4/1 |
| `docker.io/longhornio/livenessprobe:v2.19.0` | longhorn | amd64, arm64 | 0/25/19/1/0 |
| `docker.io/longhornio/longhorn-engine:v1.12.1` | longhorn | amd64, arm64 | 1/69/53/3/4 |
| `docker.io/longhornio/longhorn-instance-manager:v1.12.1` | longhorn | amd64, arm64 | 1/73/49/3/4 |
| `docker.io/longhornio/longhorn-manager:v1.12.1` | longhorn | amd64, arm64 | 1/39/26/1/4 |
| `docker.io/longhornio/longhorn-share-manager:v1.12.1` | longhorn | amd64, arm64 | 1/35/25/1/3 |
| `docker.io/longhornio/longhorn-ui:v1.12.1` | longhorn | amd64, arm64 | 1/16/11/0/3 |
| `docker.io/longhornio/support-bundle-kit:v0.0.92` | longhorn | amd64, arm64 | 0/50/41/5/3 |
| `docker.io/moby/buildkit:v0.33.1@sha256:cec9f139f45e93c5c69c60f8b07cfad9f43f4ef6b6a6cd917527fea5ff2e3dea` | build | amd64, arm64 | amd64: 0/115/119/12/9; arm64: 0/111/115/8/9 |
| `docker.io/tonistiigi/binfmt:qemu-v10.2.3@sha256:400a4873b838d1b89194d982c45e5fb3cda4593fbfd7e08a02e76b03b21166f0` | build | amd64, arm64 | 0/13/11/2/1 |
| `ghcr.io/cloudnative-pg/cloudnative-pg:1.29.2` | runtime | amd64, arm64 | 0/38/30/2/1 |
| `ghcr.io/cloudnative-pg/postgresql:17.9-system-trixie` | runtime | amd64, arm64 | 17/253/437/266/7 |
| `ghcr.io/cloudnative-pg/postgresql:18.4-system-trixie` | runtime | amd64, arm64 | 15/163/289/203/7 |
| `ghcr.io/hinkolas/mirror/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6` | build | amd64, arm64 | 0/0/1/0/2 |
| `golang:1.26-alpine` | examples | amd64, arm64 | 0/0/0/0/0 |
| `golang:1.26.10-alpine` | build | amd64, arm64 | 0/0/0/0/0 |
| `node:22-alpine` | build | amd64, arm64 | 0/11/9/1/0 |
| `python:3.12-alpine` | examples | amd64, arm64 | 0/0/5/1/0 |
| `quay.io/jetstack/cert-manager-acmesolver:v1.21.1` | runtime | amd64, arm64 | 0/14/13/1/0 |
| `quay.io/jetstack/cert-manager-cainjector:v1.21.1` | runtime | amd64, arm64 | 0/15/15/1/1 |
| `quay.io/jetstack/cert-manager-controller:v1.21.1` | runtime | amd64, arm64 | 0/17/16/4/1 |
| `quay.io/jetstack/cert-manager-webhook:v1.21.1` | runtime | amd64, arm64 | 0/17/16/4/1 |
| `rancher/k3s:v1.36.3-k3s1` | runtime | amd64, arm64 | 0/87/76/10/3 |
| `rancher/klipper-helm:v0.13.3-build20260727` | runtime | amd64, arm64 | 0/91/80/6/5 |
| `rancher/klipper-lb:v0.4.17` | runtime | amd64, arm64 | 0/0/0/0/2 |
| `rancher/local-path-provisioner:v0.0.36` | runtime | amd64, arm64 | 0/178/145/43/3 |
| `rancher/mirrored-coredns-coredns:1.14.6` | runtime | amd64, arm64 | 0/33/22/1/2 |
| `rancher/mirrored-library-busybox:1.37.0` | runtime | amd64, arm64 | 0/0/0/0/0 |
| `rancher/mirrored-library-traefik:3.7.8` | runtime | amd64, arm64 | 1/30/28/18/3 |
| `rancher/mirrored-metrics-server:v0.9.0` | runtime | amd64, arm64 | 0/20/18/4/1 |
| `rancher/mirrored-pause:3.10.2` | runtime | amd64, arm64 | 0/0/0/0/0 |
| `registry:2.8.3` | runtime | amd64, arm64 | 2/29/50/4/0 |
| `skali-dependency-review:184-amd64` | candidate | amd64 | 0/0/1/0/3 |
| `skali-dependency-review:184-arm64` | candidate | arm64 | 0/0/1/0/3 |
