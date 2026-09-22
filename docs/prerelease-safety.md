# v0.1.0-alpha.1 installation baseline

`00001_baseline.sql` replaces the 30 unpublished development migrations.
It creates the final schema directly, without the retired plaintext-value
and database-backup tables or development data rewrites. This is the last
history squash: installations created from `v0.1.0-alpha.1` are the baseline
for future numbered, incremental migrations. Released migration files must
not be edited, removed, or renumbered; changes to persisted state must provide
an upgrade path from this baseline.

An existing database from the old development history cannot be upgraded to
this baseline in place, even if it has no environments. The embedded migration
runner checks a baseline marker before applying migrations or reporting status,
so the old migration `00001` cannot silently count as the new baseline.
It refuses old databases without changing their schema or data. Do not reset
their Goose version table or insert the marker to bypass this check. Follow
the export and fresh-install procedure below for any development data you need.
Use `skalid migrate up` rather than invoking Goose directly so this check runs.

Environment namespaces use `skali-<environment UUID>` and workload resource
names include an identity hash.
Existing names, volumes, and namespaces are never automatically renamed,
adopted, or deleted. Startup also refuses legacy managed namespaces before
starting controllers.

Before replacing an existing development installation, stop writes and export
its databases, buckets, and volume contents using the old installation. Keep
those exports outside that cluster and verify they can be read. Retain the
manifests and the values needed to redeploy separately. Create a separate clean
installation, deploy the manifests, and restore application data into its new
resources. Verify applications and data before explicitly retiring the old
installation. This is a manual data transfer, not an in-place upgrade or a promise
that an old installation database can be restored into this release.

Each public hostname belongs to one environment. Applications in that environment
may share paths; other environments cannot claim any path on its hostname.
Platform, registry, and S3 hosts are reserved through `SKALI_RESERVED_HOSTS`
(semicolon-separated; the managed bundle supplies these automatically).
Configuration cannot reserve a hostname already claimed by an environment.
Reservations persist conservatively after configuration changes.

Removing a route does not immediately transfer its hostname. Claims are retained
until reconciliation confirms that the old routers are absent. Failed removal
retains ownership and retries. Rollback and automatic recovery must reclaim
released hosts and can fail if another environment has acquired them. Conflicts
leave the existing target unchanged. Raw Kubernetes changes are operator actions;
the claim system governs routes managed through skali.

Automatic backups are a per-environment setting, not part of the manifest:
an environment admin sets a cron schedule (UTC) and a retention window with
`skali backup schedule set` or in the environment's settings in the console.
skalid snapshots the environment on that schedule and deletes the snapshots
the schedule took once they are older than the retention, keeping the newest
one. Every automatic backup is off after upgrading to a release with this
setting (snapshots earlier manifest policies took are kept as manual ones);
turn it on again per environment where it matters. Nothing runs until an
admin sets the backup target. A failed scheduled run is recorded like any
other run and the schedule moves on to its next fire; check the environment's
runs, the console's Backups tab, or `skali backup list`. A successful deploy
is not evidence of a successful backup. Manual snapshots (`skali backup
create`) are kept until removed with `skali backup remove`. One backup runs
at a time per installation, so environments due in the same minute are
snapshotted one after another.

Volume restore is destructive and may leave partial restored contents on failure.
It preserves numeric ownership, permissions, and timestamps on Linux, including
the volume root in new archives. Unsupported entries and metadata failures fail
the operation explicitly. Archives support regular files, directories, and
symlinks; symlink contents are never followed during extraction.

Release verification: `scripts/check-release-snapshot.sh` builds a nonpublishing
snapshot and checks its metadata against the binaries and installer pin. CI also
verifies metadata downloaded from the exact tag after a real publication.

## Reconciliation ownership protection

Environment-owned applies retry resource-version conflicts at most five times,
with a fresh ownership check on every attempt. Field-manager conflicts and
ownership failures are returned immediately; retries never enable force. A
recovered attempt is internal to the operation and creates no failed run.

The installer now installs the versioned `skali-environment-ownership-v1`
admission policy and binding before updating skalid. Kubernetes makes the
`skali.dev/managed` and `skali.dev/environment` labels and the
`skali.dev/resource-identity` annotation immutable on objects carrying the full
identity. Status, ordinary configuration, unrelated metadata, finalizers, and
deletions remain supported. Existing valid objects need no migration. Moving
ownership by editing these keys is no longer supported.

Skalid verifies the exact contract, completed type checking, and positive and
negative dry-run updates of the installer-owned probe ConfigMap. It verifies
every serving Kubernetes API endpoint and sends UID-guarded applies directly to
a verified endpoint. Those applies omit `resourceVersion`, so unrelated HPA
status updates no longer conflict with configuration writes. The UID prevents
an update from affecting a replacement object or implicitly creating one.

Policy, binding, and API membership watches invalidate this capability on
changes or observation loss; verification has a one-minute lease. Unavailable
policies, missing permissions, unreachable API endpoints, and verification
failures retain resource-version checks with bounded retries. Installations
without admission support can still run the daemon in this fallback mode;
installer-managed upgrades require the admission stage to pass. Joining servers
verify the installed policy before enrollment completes.

Admission configuration is installer-owned infrastructure, not a security
boundary against cluster administrators who can disable admission itself. Do
not remove or weaken it while UID-guarded clients are active. Roll back the
daemon to version-guarded behavior before deliberately removing the policy.
Older daemons remain compatible with the policy. No database migration or
application redeployment is required.
