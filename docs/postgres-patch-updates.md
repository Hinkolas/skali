# PostgreSQL patch updates

This is the review a new PostgreSQL patch image needs before a release ships
it. The user-facing behavior is in
[Managed databases](databases.md#patch-updates).

## What moves

- **Pools.** `internal/substrate/cnpg/catalog.go` pins one image per major.
  New pools start on `Ref`. An existing pool moves to `Ref` only when the
  image it runs is listed in `PatchesFrom`. The substrate's `patchPool`
  moves one pool at a time, and only while every live pool is settled:
  healthy, with every member's postgres container on the row's image. An
  update that never settles stops the rollout at that pool. A pool on an
  unlisted image keeps it and logs
  `pool keeps its image; no reviewed patch update from it`.
- **The platform database.** `bundle.BootstrapPostgresImage` must equal
  the catalog's PostgreSQL 18 image; `TestImagePins` enforces this. Changing
  it changes the bundle hash, so an upgrade converges the bundle and rolls
  `skali-db` before it waits for the database to become ready.
- **Local development.** `localdev.RequiredImages` follows the catalog.
  `skali dev upgrade` pre-pulls the new images and lists the change.

`PatchesFrom` lists exact images, not version ranges. Add only transitions
this review covers. An image you leave out is not stranded: the pool keeps
running it, and a later release can add the transition or provide a manual
path.

## Review

1. **Choose the image.** Use the newest patch release of the same major,
   the same variant and the same OS base (for example,
   `-system-trixie`). `TestCatalogPatchesStayInTheirLine` rejects
   transitions that change repository, major, variant or OS base, or that
   go backwards.
2. **Compare the images** on both architectures:

   ```sh
   scripts/compare-postgres-images.sh FROM TO
   PLATFORM=linux/amd64 scripts/compare-postgres-images.sh FROM TO
   ```

   A `BLOCK` line means the move is not an in-place patch:
   - A different major changes the on-disk format.
   - A different glibc or ICU version can change collation order, which
     invalidates text indexes built under the old order.

   For each extension whose packaged version changes, the script prints
   what `ALTER EXTENSION ... UPDATE` would run. "Comments only" means the
   new library carries the whole change. Otherwise, confirm that the
   existing SQL definitions keep working with the new library, or that
   Skali runs the update.
3. **Read the release notes** in the "Migration to version X.Y" section of
   every minor release after `FROM`, up to and including `TO`. Also follow
   any "if you are upgrading from a version earlier than" pointer that
   covers `FROM`. Classify each item:
   - **Blocking:** it needs a step before the new server starts, or a dump
     and restore. Do not add the transition. Design a manual path instead.
   - **Platform:** it affects what Skali owns: the platform database
     schema, the object store's metadata database, rendered configuration,
     parameters or roles. Handle it in the same change.
   - **Tenant-conditional:** it depends on what a database holds or how it
     is used. Ship the transition and list the follow-up in the release
     notes.
4. **Scan the new images** with the image audit (`audit.yml` with
   `images=true`, see [development](development.md)) and record the result in the
   release's dependency review.
5. **Test it live.** `TestLivePoolPatchUpdate` moves a pool along the
   catalog's PostgreSQL 17 transition on the k3d test cluster. It checks
   that the pool reads as unsettled until it runs the new image, that data
   survives, and that the server reports the new version. Before the
   release, rehearse an upgrade from the previous release candidate with
   pools on the old images, and confirm that the pools and the platform
   database move.

## Record

### 2026-10-22: 17.9 → 17.11 and 18.4 → 18.6

PostgreSQL 18.5 was never released.

| | 17.9 → 17.11 | 18.4 → 18.6 |
| --- | --- | --- |
| Debian base | 13.4 → 13.7 (trixie) | 13.6 → 13.7 (trixie) |
| glibc / ICU | 2.41 / 76, unchanged | 2.41 / 76, unchanged |
| pgvector | 0.8.2 → 0.8.7, upgrade scripts are comments only | 0.8.6 → 0.8.7, comments only |
| Other extensions | unchanged | unchanged |

The architectures were identical on arm64 and amd64.

The release notes for [17.10](https://www.postgresql.org/docs/release/17.10/),
[17.11](https://www.postgresql.org/docs/release/17.11/) and
[18.6](https://www.postgresql.org/docs/release/18.6/) require no dump and
restore. Neither `FROM` image is earlier than the 17.6 and 18.2 pointers.

- **Not applicable:**
  - `output_plugin_libraries` (CVE-2026-6471) restricts logical decoding
    plugins. Tenant roles have no `REPLICATION` attribute, and Skali uses
    no logical decoding.
- **Platform:**
  - The 18.6 GIN `reltuples` check and the `btree_gist` and `ltree`
    reindex items do not apply to the platform database. Its schema
    defines no GIN, GiST or `ltree` indexes.
- **Tenant-conditional, for the release notes:**
  - Reindex `btree_gist` indexes on float columns that may hold `NaN`
    (17.11 and 18.6).
  - Reindex `btree_gist` indexes on bit or varbit columns (18.6).
  - Reindex btree indexes on `ltree` values with more than about 14,653
    labels (17.11 and 18.6).
  - On 18, check the `reltuples` of tables with GIN indexes. Run `ANALYZE`
    on any whose values look wrong.
  - `pgcrypto` no longer decrypts messages that used the affected
    unsupported ciphers by default. Use `ignore-cipher-failure=1` to recover
    and re-encrypt them (CVE-2026-14663).
  - psql scripts with a failing `COPY ... FROM STDIN` need a `\.`
    terminator line after the data (CVE-2026-6464). This affects
    `skali sql` scripts.

`TestLivePoolPatchUpdate` passed on k3d with CNPG 1.29.2. The operator
restarted the single-instance primary in place. Nobody has yet rehearsed
an upgrade from v0.1.3-rc.7 with existing pools.
